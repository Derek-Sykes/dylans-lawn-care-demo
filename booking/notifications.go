package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type EmailSettings struct {
	Enabled          bool `json:"enabled"`
	RemindersEnabled bool `json:"remindersEnabled"`
	ReminderHours    int  `json:"reminderHours"`
}

func defaultEmailSettings() EmailSettings {
	return EmailSettings{RemindersEnabled: true, ReminderHours: 24}
}

func validEmailSettings(v EmailSettings) bool {
	switch v.ReminderHours {
	case 1, 2, 6, 12, 24, 48:
		return true
	}
	return false
}

func (s *Store) emailSettings() (EmailSettings, error) {
	v := defaultEmailSettings()
	err := s.getJSON("email_settings", &v)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return v, err
}

func emailSettingsTx(tx *sql.Tx) (EmailSettings, error) {
	v := defaultEmailSettings()
	var data []byte
	err := tx.QueryRow("SELECT value FROM meta WHERE key='email_settings'").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &v)
	}
	return v, err
}

type EmailRecord struct {
	ID          string     `json:"id"`
	To          string     `json:"to"`
	Subject     string     `json:"subject"`
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Error       string     `json:"error"`
	CreatedAt   time.Time  `json:"createdAt"`
	SentAt      *time.Time `json:"sentAt,omitempty"`
	ScheduledAt time.Time  `json:"scheduledAt"`
}

type emailJob struct {
	EmailRecord
	Text, BookingID   string
	Version, Attempts int
	Start, End        int64
}

const emailColumns = "id,recipient,subject,kind,status,error,created,sent,scheduled,body,booking_id,version,attempts,snapshot_start,snapshot_end"

func scanEmail(row scanner) (emailJob, error) {
	var v emailJob
	var created, sent, scheduled int64
	err := row.Scan(&v.ID, &v.To, &v.Subject, &v.Kind, &v.Status, &v.Error, &created, &sent, &scheduled, &v.Text, &v.BookingID, &v.Version, &v.Attempts, &v.Start, &v.End)
	v.CreatedAt, v.ScheduledAt = time.Unix(created, 0).UTC(), time.Unix(scheduled, 0).UTC()
	if sent > 0 {
		stamp := time.Unix(sent, 0).UTC()
		v.SentAt = &stamp
	}
	return v, err
}

func (s *Store) emailJob(id string) (emailJob, error) {
	return scanEmail(s.db.QueryRow("SELECT "+emailColumns+" FROM email_outbox WHERE id=?", id))
}

func emailContent(settings Settings, b Booking, kind string) (string, string) {
	loc, err := time.LoadLocation(settings.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	when := b.Start.In(loc).Format("Monday, January 2, 2006 at 3:04 PM MST")
	end := b.End.In(loc).Format("3:04 PM MST")
	label := "service appointment"
	if b.Kind == "estimate" {
		label = "estimate/callback appointment"
	}
	var title, intro string
	switch kind {
	case "receipt":
		title, intro = "Request received", "We received your "+label+" request. The requested time still needs confirmation."
	case "confirmation":
		title, intro = "Appointment confirmed", "Your "+label+" is confirmed."
	case "reschedule":
		title, intro = "Appointment time updated", "The time for your "+label+" has changed."
		if b.Status != "confirmed" {
			intro += " The requested time still needs confirmation."
		}
	case "cancellation":
		title, intro = "Appointment cancelled", "Your "+label+" has been cancelled."
	case "reminder":
		title, intro = "Appointment reminder", "A reminder about your confirmed "+label+"."
	}
	// Subject is one line even when the configured business name contains spacing.
	subject := title + " — " + strings.Join(strings.Fields(settings.BusinessName), " ")
	text := fmt.Sprintf("Hello %s,\n\n%s\n\nService: %s\nWhen: %s\nUntil: %s\nProperty: %s\n", b.Name, intro, serviceName(b.ServiceID), when, end, b.Address)
	if b.Kind == "estimate" {
		text += "\nThis is an estimate or callback appointment, not a scheduled service job.\n"
	}
	text += "\nQuestions or changes? Reply to this email.\n\n" + settings.BusinessName
	// Neither internal admin notes nor customer freeform notes are email content.
	return subject, text
}

func (s *Store) insertBookingEmailTx(tx *sql.Tx, b Booking, version int, kind string, scheduled, now time.Time, settings Settings) error {
	subject, text := emailContent(settings, b, kind)
	_, err := tx.Exec(`INSERT INTO email_outbox(id,dedupe_key,booking_id,version,kind,recipient,subject,body,created,scheduled,snapshot_start,snapshot_end) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(dedupe_key) DO NOTHING`, randomToken(18), fmt.Sprintf("%s:%d:%s", b.ID, version, kind), b.ID, version, kind, b.Email, subject, text, now.Unix(), scheduled.Unix(), b.Start.Unix(), b.End.Unix())
	return err
}

// Called inside the same transaction as a booking mutation. Enabling emails
// never scans existing bookings: only subsequent customer/admin/Calendar events
// can enter this outbox, and idempotent/no-op changes cannot add duplicate mail.
func (s *Store) queueBookingEmailTx(tx *sql.Tx, before, after Booking, now time.Time, enroll ...bool) error {
	created := before.ID == ""
	changedTime := !before.Start.Equal(after.Start) || !before.End.Equal(after.End)
	if !created && before.Status == after.Status && !changedTime {
		return nil
	}
	if _, err := tx.Exec("UPDATE email_outbox SET status='skipped',error='The appointment changed before this email was sent.' WHERE booking_id=? AND status IN ('queued','failed')", after.ID); err != nil {
		return err
	}
	v, err := emailSettingsTx(tx)
	if err != nil || !v.Enabled || !after.Start.After(now) {
		return err
	}
	if len(enroll) == 1 && !enroll[0] {
		var count int
		if err = tx.QueryRow("SELECT COUNT(*) FROM email_booking_state WHERE booking_id=?", after.ID).Scan(&count); err != nil || count == 0 {
			return err
		}
	}
	kind := ""
	switch {
	case created:
		kind = "receipt"
	case after.Status == "cancelled" && before.Status != "cancelled":
		kind = "cancellation"
	case after.Status == "cancelled":
		return nil
	case changedTime:
		kind = "reschedule"
	case after.Status == "confirmed" && before.Status != "confirmed":
		kind = "confirmation"
	}
	// Even a transition back to follow-up invalidates an older confirmation and
	// reminder; only meaningful customer-facing events create a replacement.
	var version int
	err = tx.QueryRow("INSERT INTO email_booking_state(booking_id,version) VALUES(?,1) ON CONFLICT(booking_id) DO UPDATE SET version=version+1 RETURNING version", after.ID).Scan(&version)
	if err != nil || kind == "" {
		return err
	}
	var settings Settings
	var raw []byte
	if err = tx.QueryRow("SELECT value FROM meta WHERE key='settings'").Scan(&raw); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &settings); err != nil {
		return err
	}
	if err = s.insertBookingEmailTx(tx, after, version, kind, now, now, settings); err != nil {
		return err
	}
	if after.Status == "confirmed" && v.RemindersEnabled {
		due := after.Start.Add(-time.Duration(v.ReminderHours) * time.Hour)
		// A late confirmation already tells the customer the details; don't send
		// a second, immediately due reminder after its requested window passed.
		if due.After(now) {
			return s.insertBookingEmailTx(tx, after, version, "reminder", due, now, settings)
		}
	}
	return nil
}

func (s *Store) saveEmailSettings(v EmailSettings, now time.Time) error {
	if !validEmailSettings(v) {
		return &apiError{400, "invalid_email_settings", "Choose a supported reminder time."}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	data, _ := json.Marshal(v)
	_, err = tx.Exec("INSERT INTO meta(key,value) VALUES('email_settings',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", data)
	if err != nil {
		return err
	}
	if !v.Enabled {
		_, err = tx.Exec("UPDATE email_outbox SET status='skipped',error='Automatic emails were turned off.' WHERE kind!='test' AND status IN ('queued','failed')")
	} else if !v.RemindersEnabled {
		_, err = tx.Exec("UPDATE email_outbox SET status='skipped',error='Reminders were turned off.' WHERE kind='reminder' AND status IN ('queued','failed')")
	} else {
		// Retime only reminders already queued by an explicit future event. Never
		// backfill appointments that predate activation or resurrect skipped mail.
		_, err = tx.Exec("UPDATE email_outbox SET scheduled=snapshot_start-?,next_attempt=0 WHERE kind='reminder' AND status='queued'", v.ReminderHours*3600)
		if err == nil {
			_, err = tx.Exec("UPDATE email_outbox SET status='skipped',error='The selected reminder window has already passed.' WHERE kind='reminder' AND status='queued' AND scheduled<=?", now.Unix())
		}
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) disableEmailNotifications() error {
	v, err := s.emailSettings()
	if err != nil {
		return err
	}
	v.Enabled = false
	return s.saveEmailSettings(v, time.Now())
}

// Validity is rechecked transactionally immediately before dispatch or retry.
// A mutation after sending starts cannot retract an email already given to Gmail.
func emailStillRelevantTx(tx *sql.Tx, job emailJob, now time.Time) (bool, error) {
	if job.Kind == "test" {
		return true, nil
	}
	v, err := emailSettingsTx(tx)
	if err != nil || !v.Enabled || (job.Kind == "reminder" && !v.RemindersEnabled) {
		return false, err
	}
	b, err := scanBooking(tx.QueryRow("SELECT "+bookingColumns+" FROM bookings WHERE id=?", job.BookingID))
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var version int
	if err = tx.QueryRow("SELECT version FROM email_booking_state WHERE booking_id=?", b.ID).Scan(&version); err != nil {
		return false, err
	}
	if version != job.Version || b.Email != job.To || b.Start.Unix() != job.Start || b.End.Unix() != job.End || !b.Start.After(now) {
		return false, nil
	}
	switch job.Kind {
	case "cancellation":
		return b.Status == "cancelled", nil
	case "confirmation", "reminder":
		return b.Status == "confirmed", nil
	default:
		return b.Status != "cancelled", nil
	}
}

func (a *App) sendEmailDue(ctx context.Context) {
	a.emailMu.Lock()
	defer a.emailMu.Unlock()
	// A stopped process might have submitted a message immediately before exit.
	// Recover its expired send lease as uncertain, never as an automatic resend.
	_, _ = a.store.db.Exec("UPDATE email_outbox SET status='uncertain',error='Sending was interrupted. Check Gmail before trying again.' WHERE status='sending' AND attempt_started<=?", a.now().Add(-2*time.Minute).Unix())
	if a.mailer == nil || !a.mailer.Connected() {
		return
	}
	for n := 0; n < 10 && ctx.Err() == nil && a.mailer.Connected(); n++ {
		job, err := scanEmail(a.store.db.QueryRow("SELECT "+emailColumns+" FROM email_outbox WHERE status='queued' AND scheduled<=? AND next_attempt<=? ORDER BY scheduled,created LIMIT 1", a.now().Unix(), a.now().Unix()))
		if err != nil {
			return
		}
		if job.Kind == "reminder" {
			// A reminder must reflect the current Calendar, not an offline snapshot.
			if !a.calendar.Connected() || a.refreshCalendar(ctx, true) != nil {
				_, _ = a.store.db.Exec("UPDATE email_outbox SET next_attempt=?,error='Waiting for a current Calendar connection.' WHERE id=? AND status='queued'", a.now().Add(time.Minute).Unix(), job.ID)
				continue
			}
		}
		tx, err := a.store.db.BeginTx(ctx, nil)
		if err != nil {
			return
		}
		current, err := scanEmail(tx.QueryRow("SELECT "+emailColumns+" FROM email_outbox WHERE id=? AND status='queued' AND scheduled<=? AND next_attempt<=?", job.ID, a.now().Unix(), a.now().Unix()))
		if errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			continue
		}
		if err != nil {
			_ = tx.Rollback()
			return
		}
		if current.Status != "queued" || current.ScheduledAt.After(a.now()) {
			_ = tx.Rollback()
			continue
		}
		valid, err := emailStillRelevantTx(tx, current, a.now())
		if err != nil {
			_ = tx.Rollback()
			return
		}
		if !valid {
			_, err = tx.Exec("UPDATE email_outbox SET status='skipped',error='This message no longer matches the current appointment.' WHERE id=? AND status='queued'", current.ID)
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			if err != nil {
				return
			}
			continue
		}
		if current.Kind == "confirmation" || current.Kind == "reminder" {
			var calendarStatus string
			if err = tx.QueryRow("SELECT calendar_status FROM bookings WHERE id=?", current.BookingID).Scan(&calendarStatus); err != nil {
				_ = tx.Rollback()
				return
			}
			if calendarStatus != "synced" {
				_, err = tx.Exec("UPDATE email_outbox SET next_attempt=?,error='Waiting for Calendar synchronization.' WHERE id=? AND status='queued'", a.now().Add(30*time.Second).Unix(), current.ID)
				if err == nil {
					err = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
				if err != nil {
					return
				}
				continue
			}
		}
		_, err = tx.Exec("UPDATE email_outbox SET status='sending',attempts=attempts+1,attempt_started=?,error='' WHERE id=? AND status='queued'", a.now().Unix(), current.ID)
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			return
		}
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		providerID, sendErr := a.mailer.Send(callCtx, EmailMessage{ID: current.ID, To: current.To, Subject: current.Subject, Text: current.Text})
		cancel()
		if sendErr == nil && providerID != "" {
			_, _ = a.store.db.Exec("UPDATE email_outbox SET status='sent',sent=?,provider_id=?,error='' WHERE id=? AND status='sending'", a.now().Unix(), providerID, current.ID)
			continue
		}
		status, message, next := "uncertain", "Gmail may have received this email. Check Gmail before trying again.", int64(0)
		var failure *MailSendError
		if errors.As(sendErr, &failure) && !failure.Uncertain {
			status, message = "failed", "Gmail did not accept this email. Check the connection and try again."
			if failure.Retryable && current.Attempts < 5 {
				status, message = "queued", "Gmail temporarily declined this email. Another attempt is scheduled."
				next = a.now().Add(time.Duration(1<<min(current.Attempts, 5)) * 30 * time.Second).Unix()
			}
		}
		_, _ = a.store.db.Exec("UPDATE email_outbox SET status=?,error=?,next_attempt=? WHERE id=? AND status='sending'", status, message, next, current.ID)
	}
}

func (a *App) emailActor(w http.ResponseWriter, r *http.Request) (Session, bool) {
	actor, _, err := a.session(r)
	if err != nil || (actor.Role != "owner" && actor.Role != "operator") {
		writeError(w, &apiError{403, "workspace_access_required", "Sign in to the shared workspace to manage appointment emails."})
		return actor, false
	}
	return actor, true
}

func (a *App) handleEmailStatus(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.emailActor(w, r)
	if !ok {
		return
	}
	settings, err := a.store.emailSettings()
	if err != nil {
		writeError(w, err)
		return
	}
	rows, err := a.store.db.Query("SELECT " + emailColumns + " FROM email_outbox ORDER BY created DESC,id DESC LIMIT 100")
	if err != nil {
		writeError(w, err)
		return
	}
	history := []EmailRecord{}
	for rows.Next() {
		v, e := scanEmail(rows)
		if e != nil {
			rows.Close()
			writeError(w, e)
			return
		}
		history = append(history, v.EmailRecord)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"settings": settings, "connection": a.mailConnection(actor), "history": history, "canManage": true})
}

func (a *App) handleEmailSettings(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.emailActor(w, r)
	if !ok {
		return
	}
	var v EmailSettings
	if !readJSON(w, r, &v) {
		return
	}
	if v.Enabled && !a.mailConnection(actor).Connected {
		writeError(w, &apiError{409, "gmail_not_connected", "Connect the workspace's Gmail account before turning on appointment emails."})
		return
	}
	if err := a.store.saveEmailSettings(v, a.now()); err != nil {
		writeError(w, err)
		return
	}
	a.wakeWorker()
	writeJSON(w, 200, map[string]any{"settings": v})
}

func (a *App) handleEmailTest(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.emailActor(w, r)
	if !ok {
		return
	}
	var in struct{}
	if !readJSON(w, r, &in) {
		return
	}
	conn := a.mailConnection(actor)
	if !conn.Connected || !validAccessEmail(conn.Email) {
		writeError(w, &apiError{409, "gmail_not_connected", "Connect Gmail before sending a test email."})
		return
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM email_outbox WHERE kind='test' AND created>?", a.now().Add(-time.Minute).Unix()).Scan(&count); err != nil {
		writeError(w, err)
		return
	}
	if count > 0 {
		writeError(w, &apiError{429, "email_test_limited", "Please wait a minute before sending another test email."})
		return
	}
	id := randomToken(18)
	_, err = tx.Exec("INSERT INTO email_outbox(id,dedupe_key,kind,recipient,subject,body,created,scheduled) VALUES(?,?,'test',?,'Appointment email test',?,?,?)", id, "test:"+id, conn.Email, "Your appointment email connection is working. This test was requested from your shared admin workspace. No customer was emailed.", a.now().Unix(), a.now().Unix())
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	job, err := a.store.emailJob(id)
	if err != nil {
		writeError(w, err)
		return
	}
	a.wakeWorker()
	writeJSON(w, 202, map[string]any{"message": job.EmailRecord})
}

func (a *App) handleEmailRetry(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.emailActor(w, r)
	if !ok {
		return
	}
	var in struct {
		ConfirmUncertain bool `json:"confirmUncertain"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !a.mailConnection(actor).Connected {
		writeError(w, &apiError{409, "gmail_not_connected", "Connect Gmail before retrying an email."})
		return
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()
	job, err := scanEmail(tx.QueryRow("SELECT "+emailColumns+" FROM email_outbox WHERE id=?", r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, &apiError{404, "email_not_found", "This email could not be found."})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if job.Status != "failed" && job.Status != "uncertain" {
		writeError(w, &apiError{409, "email_not_retryable", "Only failed emails or emails with unknown delivery status can be retried."})
		return
	}
	if job.Status == "uncertain" && !in.ConfirmUncertain {
		writeError(w, &apiError{409, "confirm_uncertain_retry", "This email may already have been sent. Check Gmail and confirm before trying again."})
		return
	}
	valid, err := emailStillRelevantTx(tx, job, a.now())
	if err != nil {
		writeError(w, err)
		return
	}
	if !valid {
		writeError(w, &apiError{409, "email_outdated", "This email no longer matches the current appointment or email settings."})
		return
	}
	_, err = tx.Exec("UPDATE email_outbox SET status='queued',error='',next_attempt=0,attempt_started=0 WHERE id=?", job.ID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	job.Status, job.Error = "queued", ""
	a.wakeWorker()
	writeJSON(w, 202, map[string]any{"message": job.EmailRecord})
}
