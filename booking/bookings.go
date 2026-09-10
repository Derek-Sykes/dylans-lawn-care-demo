package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type Booking struct {
	ID             string    `json:"id"`
	Start          time.Time `json:"start"`
	End            time.Time `json:"end"`
	ServiceID      string    `json:"serviceId"`
	Name           string    `json:"name"`
	Email          string    `json:"email"`
	Phone          string    `json:"phone"`
	Address        string    `json:"address"`
	Notes          string    `json:"notes"`
	Status         string    `json:"status"`
	CalendarStatus string    `json:"calendarStatus"`
	CreatedAt      time.Time `json:"createdAt"`
	AdminNotes     string    `json:"adminNotes"`
	EventID        string    `json:"-"`
	BlockedEnd     time.Time `json:"-"`
	PayloadHash    string    `json:"-"`
	Generation     int       `json:"-"`
	Attempts       int       `json:"-"`
}
type BookingInput struct {
	ServiceID      string `json:"serviceId"`
	Start          string `json:"start"`
	Name           string `json:"name"`
	Email          string `json:"email"`
	Phone          string `json:"phone"`
	Address        string `json:"address"`
	Notes          string `json:"notes"`
	IdempotencyKey string `json:"idempotencyKey"`
}
type apiError struct {
	Status        int
	Code, Message string
}

func (e *apiError) Error() string { return e.Message }

var errUnavailable = &apiError{503, "calendar_unavailable", "Booking is unavailable while the calendar connection needs attention. Please call Dylan."}
var errConflict = &apiError{409, "slot_unavailable", "That time is no longer available. Please choose another appointment."}
var services = []struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}{{"lawn-care", "Lawn care"}, {"landscaping", "Landscaping"}, {"snow-ice", "Snow & ice"}}

func serviceName(id string) string {
	for _, s := range services {
		if s.ID == id {
			return s.Name
		}
	}
	return ""
}
func validText(v string, min, max int) bool {
	n := utf8.RuneCountInString(v)
	if !utf8.ValidString(v) || n < min || n > max {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

var idemPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func normalizeInput(v *BookingInput) (time.Time, error) {
	v.Name = strings.TrimSpace(v.Name)
	v.Email = strings.ToLower(strings.TrimSpace(v.Email))
	v.Phone = strings.TrimSpace(v.Phone)
	v.Address = strings.TrimSpace(v.Address)
	v.Notes = strings.TrimSpace(v.Notes)
	if serviceName(v.ServiceID) == "" || !validText(v.Name, 1, 100) || !validText(v.Email, 3, 254) || !validText(v.Phone, 7, 40) || !validText(v.Address, 1, 500) || !validText(v.Notes, 0, 2000) || !idemPattern.MatchString(v.IdempotencyKey) {
		return time.Time{}, &apiError{400, "invalid_booking", "Please check the service and contact details, then try again."}
	}
	email, e := mail.ParseAddress(v.Email)
	if e != nil || email.Address != v.Email {
		return time.Time{}, &apiError{400, "invalid_email", "Please enter a valid email address."}
	}
	digits := 0
	for _, r := range v.Phone {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	if digits < 7 || digits > 20 {
		return time.Time{}, &apiError{400, "invalid_phone", "Please enter a valid phone number."}
	}
	t, e := time.Parse(time.RFC3339, v.Start)
	if e != nil {
		return t, &apiError{400, "invalid_start", "Please choose an available appointment."}
	}
	t = t.UTC()
	v.Start = t.Format(time.RFC3339Nano)
	return t, nil
}
func inputHash(v BookingInput) string {
	v.IdempotencyKey = ""
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func bookingID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic("cryptographic randomness unavailable")
	}
	return hex.EncodeToString(b)
}

const bookingColumns = `id,start,end,service_id,name,email,phone,address,notes,status,calendar_status,created_at,admin_notes,event_id,blocked_end,payload_hash,generation,attempts`

type scanner interface{ Scan(...any) error }

func scanBooking(row scanner) (Booking, error) {
	var b Booking
	var start, end, created, blocked int64
	err := row.Scan(&b.ID, &start, &end, &b.ServiceID, &b.Name, &b.Email, &b.Phone, &b.Address, &b.Notes, &b.Status, &b.CalendarStatus, &created, &b.AdminNotes, &b.EventID, &blocked, &b.PayloadHash, &b.Generation, &b.Attempts)
	b.Start = time.Unix(start, 0).UTC()
	b.End = time.Unix(end, 0).UTC()
	b.CreatedAt = time.Unix(created, 0).UTC()
	b.BlockedEnd = time.Unix(blocked, 0).UTC()
	return b, err
}
func (s *Store) booking(id string) (Booking, error) {
	return scanBooking(s.db.QueryRow("SELECT "+bookingColumns+" FROM bookings WHERE id=?", id))
}
func (s *Store) byIdempotency(key string) (Booking, error) {
	return scanBooking(s.db.QueryRow("SELECT "+bookingColumns+" FROM bookings WHERE idempotency_key=?", s.hash(key)))
}
func (s *Store) localBusy(start, end time.Time) ([]Busy, error) {
	rows, err := s.db.Query("SELECT start,blocked_end FROM bookings WHERE (status!='cancelled' OR calendar_status!='synced') AND start<? AND blocked_end>?", end.Unix(), start.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Busy{}
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out = append(out, Busy{time.Unix(a, 0).UTC(), time.Unix(b, 0).UTC()})
	}
	return out, rows.Err()
}

func (a *App) availableSlots(ctx context.Context, date string) ([]Slot, Settings, error) {
	v, err := a.store.settings()
	if err != nil {
		return nil, v, err
	}
	slots, err := scheduledSlots(v, date, a.now())
	if err != nil {
		return nil, v, &apiError{400, "invalid_date", err.Error()}
	}
	if !a.calendar.Connected() {
		return nil, v, errUnavailable
	}
	if len(slots) == 0 {
		return []Slot{}, v, nil
	}
	start := slots[0].Start.Add(-time.Duration(v.BufferMinutes) * time.Minute)
	end := slots[len(slots)-1].End.Add(time.Duration(v.BufferMinutes) * time.Minute)
	busy, err := a.calendar.Busy(ctx, start, end)
	if err != nil {
		return nil, v, errUnavailable
	}
	for i := range busy {
		busy[i].End = busy[i].End.Add(time.Duration(v.BufferMinutes) * time.Minute)
	}
	local, err := a.store.localBusy(start, end)
	if err != nil {
		return nil, v, err
	}
	busy = append(busy, local...)
	out := []Slot{}
	for _, s := range slots {
		if !conflicts(s, v.BufferMinutes, busy) {
			out = append(out, s)
		}
	}
	return out, v, nil
}

func (a *App) createBooking(ctx context.Context, in BookingInput) (Booking, bool, error) {
	start, err := normalizeInput(&in)
	if err != nil {
		return Booking{}, false, err
	}
	hash := inputHash(in)
	// Serialize settings changes with validation. SQL's overlap trigger additionally
	// protects independent connections/processes and racing idempotent requests.
	a.bookingMu.Lock()
	defer a.bookingMu.Unlock()
	if existing, e := a.store.byIdempotency(in.IdempotencyKey); e == nil {
		if existing.PayloadHash != hash {
			return Booking{}, false, &apiError{409, "idempotency_mismatch", "This submission identifier already belongs to different details."}
		}
		return existing, false, nil
	} else if !errors.Is(e, sql.ErrNoRows) {
		return Booking{}, false, e
	}
	v, err := a.store.settings()
	if err != nil {
		return Booking{}, false, err
	}
	loc, _ := time.LoadLocation(v.TimeZone)
	slots, err := scheduledSlots(v, start.In(loc).Format("2006-01-02"), a.now())
	if err != nil {
		return Booking{}, false, err
	}
	var selected Slot
	for _, slot := range slots {
		if slot.Start.Equal(start) {
			selected = slot
			break
		}
	}
	if selected.Start.IsZero() {
		return Booking{}, false, errConflict
	}
	if !a.calendar.Connected() {
		return Booking{}, false, errUnavailable
	}
	buffer := time.Duration(v.BufferMinutes) * time.Minute
	busy, err := a.calendar.Busy(ctx, start.Add(-buffer), selected.End.Add(buffer))
	if err != nil {
		return Booking{}, false, errUnavailable
	}
	for i := range busy {
		busy[i].End = busy[i].End.Add(buffer)
	}
	if conflicts(selected, v.BufferMinutes, busy) {
		return Booking{}, false, errConflict
	}
	id := bookingID()
	now := a.now().UTC()
	_, err = a.store.db.ExecContext(ctx, `INSERT INTO bookings(id,idempotency_key,payload_hash,start,end,blocked_end,service_id,name,email,phone,address,notes,created_at,event_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, a.store.hash(in.IdempotencyKey), hash, start.Unix(), selected.End.Unix(), selected.End.Add(buffer).Unix(), in.ServiceID, in.Name, in.Email, in.Phone, in.Address, in.Notes, now.Unix(), "d"+id)
	if err != nil {
		if existing, e := a.store.byIdempotency(in.IdempotencyKey); e == nil {
			if existing.PayloadHash == hash {
				return existing, false, nil
			}
			return Booking{}, false, &apiError{409, "idempotency_mismatch", "This submission identifier already belongs to different details."}
		}
		if strings.Contains(err.Error(), "booking_overlap") {
			return Booking{}, false, errConflict
		}
		return Booking{}, false, err
	}
	b, err := a.store.booking(id)
	a.wakeWorker()
	return b, true, err
}

func (a *App) patchBooking(id string, status, notes *string) (Booking, error) {
	if status != nil && *status != "needs_followup" && *status != "contacted" && *status != "confirmed" && *status != "cancelled" {
		return Booking{}, &apiError{400, "invalid_status", "Choose a valid follow-up status."}
	}
	if notes != nil && !validText(*notes, 0, 4000) {
		return Booking{}, &apiError{400, "invalid_notes", "Notes must contain no more than 4,000 characters."}
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return Booking{}, err
	}
	defer tx.Rollback()
	b, err := scanBooking(tx.QueryRow("SELECT "+bookingColumns+" FROM bookings WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Booking{}, &apiError{404, "not_found", "Booking not found."}
	}
	if err != nil {
		return Booking{}, err
	}
	if status != nil {
		if b.Status == "cancelled" && *status != "cancelled" {
			return Booking{}, &apiError{409, "already_cancelled", "A cancelled request cannot be reopened. Create a new appointment."}
		}
		if *status == "confirmed" && b.CalendarStatus != "synced" {
			return Booking{}, &apiError{409, "calendar_pending", "Wait for Calendar synchronization before confirming this appointment."}
		}
		if *status == "cancelled" && b.Status != "cancelled" {
			_, err = tx.Exec("UPDATE bookings SET status='cancelled',calendar_status='pending',generation=generation+1,attempts=0,next_attempt=0 WHERE id=?", id)
		} else {
			_, err = tx.Exec("UPDATE bookings SET status=? WHERE id=?", *status, id)
		}
		if err != nil {
			return Booking{}, err
		}
	}
	if notes != nil {
		if _, err = tx.Exec("UPDATE bookings SET admin_notes=? WHERE id=?", strings.TrimSpace(*notes), id); err != nil {
			return Booking{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Booking{}, err
	}
	a.wakeWorker()
	return a.store.booking(id)
}

func (a *App) wakeWorker() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}
func (a *App) worker(ctx context.Context) {
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-a.wake:
		}
		a.syncDue(ctx)
		a.store.cleanup(a.now())
	}
}
func (a *App) syncDue(ctx context.Context) {
	a.workerMu.Lock()
	defer a.workerMu.Unlock()
	for n := 0; n < 20; n++ {
		b, err := scanBooking(a.store.db.QueryRow("SELECT "+bookingColumns+" FROM bookings WHERE calendar_status!='synced' AND next_attempt<=? ORDER BY created_at LIMIT 1", a.now().Unix()))
		if errors.Is(err, sql.ErrNoRows) || ctx.Err() != nil {
			return
		}
		if err != nil {
			return
		}
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = a.calendar.Sync(callCtx, b)
		cancel()
		if err == nil {
			_, _ = a.store.db.Exec("UPDATE bookings SET calendar_status='synced',sync_generation=generation,attempts=0 WHERE id=? AND generation=?", b.ID, b.Generation)
		} else {
			delay := time.Duration(1<<min(b.Attempts, 8)) * 30 * time.Second
			_, _ = a.store.db.Exec("UPDATE bookings SET calendar_status='failed',attempts=attempts+1,next_attempt=? WHERE id=? AND generation=?", a.now().Add(delay).Unix(), b.ID, b.Generation)
		}
	}
}
