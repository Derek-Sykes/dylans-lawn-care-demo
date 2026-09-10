package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCalendar struct {
	mu                 sync.Mutex
	connected          bool
	busy               []Busy
	busyErr, errorSync error
	syncCalls          int
	events             map[string]bool
	entered, release   chan struct{}
}

func (f *fakeCalendar) Connected() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.connected }
func (f *fakeCalendar) Busy(context.Context, time.Time, time.Time) ([]Busy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Busy{}, f.busy...), f.busyErr
}
func (f *fakeCalendar) Sync(ctx context.Context, b Booking) error {
	f.mu.Lock()
	f.syncCalls++
	err := f.errorSync
	entered, release := f.entered, f.release
	f.entered = nil
	f.release = nil
	f.mu.Unlock()
	if entered != nil {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		f.events = map[string]bool{}
	}
	if b.Status == "cancelled" {
		delete(f.events, b.EventID)
	} else {
		f.events[b.EventID] = true
	}
	return nil
}
func testConfig(dir string) Config {
	return Config{PublicOrigin: "http://127.0.0.1:4177", AdminOrigin: "http://127.0.0.1:4178", DataDir: dir, BootstrapToken: "test-only-operator-credential-with-enough-length", OAuthMode: "desktop", ClientID: "test.apps.googleusercontent.com", HTTPTimeout: 2 * time.Second}
}
func testApp(t *testing.T) (*App, *fakeCalendar) {
	t.Helper()
	a, err := newApp(testConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return instant("2026-09-10T12:00:00Z") }
	fake := &fakeCalendar{connected: true}
	a.calendar = fake
	t.Cleanup(func() { a.store.close() })
	return a, fake
}
func testInput(key string) BookingInput {
	return BookingInput{ServiceID: "lawn-care", Start: "2026-09-14T13:00:00Z", Name: "Sample Customer", Email: "sample@example.com", Phone: "410-555-0123", Address: "Example property", Notes: "Estimate only", IdempotencyKey: key}
}

func TestConcurrentBookingAndIdempotentRetry(t *testing.T) {
	a, _ := testApp(t)
	const count = 16
	var wg sync.WaitGroup
	results := make(chan error, count)
	ids := make(chan string, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, _, err := a.createBooking(context.Background(), testInput(randomToken(18)))
			results <- err
			if err == nil {
				ids <- b.ID
			}
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	conflict := 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, errConflict) {
			conflict++
		} else {
			t.Fatalf("unexpected booking error: %v", err)
		}
	}
	if success != 1 || conflict != 15 {
		t.Fatalf("expected one reservation: success=%d conflict=%d", success, conflict)
	}
	b, err := a.store.booking(<-ids)
	if err != nil {
		t.Fatal(err)
	}
	if b.CalendarStatus != "pending" || b.Status != "needs_followup" {
		t.Fatal("booking was confirmed before synchronization")
	}
	a2, _ := testApp(t)
	input := testInput("idempotent-request-001")
	first, created, err := a2.createBooking(context.Background(), input)
	if err != nil || !created {
		t.Fatal("first idempotent request failed")
	}
	fake := a2.calendar.(*fakeCalendar)
	fake.mu.Lock()
	fake.connected = false
	fake.mu.Unlock()
	second, created, err := a2.createBooking(context.Background(), input)
	if err != nil || created || second.ID != first.ID {
		t.Fatal("retry was not recovered while Calendar was disconnected")
	}
	input.Name = "Different Customer"
	_, _, err = a2.createBooking(context.Background(), input)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != "idempotency_mismatch" {
		t.Fatal("reused identifier with changed body was accepted")
	}
}

func TestIndependentConnectionsCannotOverlap(t *testing.T) {
	a, f := testApp(t)
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	other.now = a.now
	other.calendar = f
	var wg sync.WaitGroup
	result := make(chan error, 2)
	for _, app := range []*App{a, other} {
		wg.Add(1)
		go func(app *App) {
			defer wg.Done()
			_, _, e := app.createBooking(context.Background(), testInput(randomToken(20)))
			result <- e
		}(app)
	}
	wg.Wait()
	close(result)
	success := 0
	for e := range result {
		if e == nil {
			success++
		} else if !errors.Is(e, errConflict) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("database did not enforce a cross-connection reservation")
	}
}

func TestCalendarFailureRetryCancelAndPersistence(t *testing.T) {
	a, f := testApp(t)
	b, _, err := a.createBooking(context.Background(), testInput("persistent-booking-001"))
	if err != nil {
		t.Fatal(err)
	}
	f.errorSync = errors.New("temporary provider failure")
	a.syncDue(context.Background())
	b, _ = a.store.booking(b.ID)
	if b.CalendarStatus != "failed" || b.Attempts != 1 {
		t.Fatal("failed sync state was not persisted")
	}
	_, err = a.patchBooking(b.ID, ptr("confirmed"), nil)
	if err == nil {
		t.Fatal("unsynced booking was confirmed")
	}
	_, _ = a.store.db.Exec("UPDATE bookings SET next_attempt=0 WHERE id=?", b.ID)
	f.errorSync = nil
	a.syncDue(context.Background())
	b, _ = a.store.booking(b.ID)
	if b.CalendarStatus != "synced" || len(f.events) != 1 {
		t.Fatal("retry did not create exactly one event")
	}
	_, err = a.patchBooking(b.ID, ptr("cancelled"), ptr("Customer requested cancellation"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = a.createBooking(context.Background(), testInput("blocked-cancel-slot-001"))
	if !errors.Is(err, errConflict) {
		t.Fatal("slot released before remote deletion")
	}
	f.errorSync = errors.New("temporary delete failure")
	a.syncDue(context.Background())
	b, _ = a.store.booking(b.ID)
	if b.Status != "cancelled" || b.CalendarStatus != "failed" {
		t.Fatal("cancellation failure lost its state")
	}
	f.errorSync = nil
	_, _ = a.store.db.Exec("UPDATE bookings SET next_attempt=0 WHERE id=?", b.ID)
	a.syncDue(context.Background())
	if len(f.events) != 0 {
		t.Fatal("remote event was not removed")
	}
	_, _, err = a.createBooking(context.Background(), testInput("released-cancel-slot-001"))
	if err != nil {
		t.Fatal("successfully cancelled slot stayed reserved")
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	saved, err := other.store.booking(b.ID)
	if err != nil || saved.CalendarStatus != "synced" || saved.Status != "cancelled" || saved.AdminNotes == "" {
		t.Fatal("booking was not recovered from persistent storage")
	}
}
func ptr(v string) *string { return &v }
func TestCancellationDuringInFlightCreate(t *testing.T) {
	a, f := testApp(t)
	b, _, err := a.createBooking(context.Background(), testInput("cancel-during-create-001"))
	if err != nil {
		t.Fatal(err)
	}
	f.entered = make(chan struct{})
	f.release = make(chan struct{})
	entered, release := f.entered, f.release
	done := make(chan struct{})
	go func() { a.syncDue(context.Background()); close(done) }()
	<-entered
	if _, err = a.patchBooking(b.ID, ptr("cancelled"), nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	b, _ = a.store.booking(b.ID)
	if b.Status != "cancelled" || b.CalendarStatus != "synced" || len(f.events) != 0 {
		t.Fatal("stale create result overrode cancellation")
	}
}
func TestBusyFailureAndBuffersFailClosed(t *testing.T) {
	a, f := testApp(t)
	f.busyErr = errors.New("provider offline")
	if _, _, err := a.availableSlots(context.Background(), "2026-09-14"); !errors.Is(err, errUnavailable) {
		t.Fatal("unsafe slots published during provider failure")
	}
	if _, _, err := a.createBooking(context.Background(), testInput("offline-calendar-001")); !errors.Is(err, errUnavailable) {
		t.Fatal("booking accepted without Calendar availability")
	}
	f.busyErr = nil
	f.busy = []Busy{{instant("2026-09-14T12:30:00Z"), instant("2026-09-14T13:00:00Z")}}
	if _, _, err := a.createBooking(context.Background(), testInput("buffer-after-event-001")); !errors.Is(err, errConflict) {
		t.Fatal("Google event's following buffer was ignored")
	}
	f.busy = nil
	first, _, err := a.createBooking(context.Background(), testInput("adjacent-first-slot-001"))
	if err != nil {
		t.Fatal(err)
	}
	input := testInput("adjacent-second-slot-001")
	input.Start = first.BlockedEnd.Format(time.RFC3339)
	if _, _, err = a.createBooking(context.Background(), input); err != nil {
		t.Fatal("adjacent local buffer was counted twice")
	}
}

func request(a *App, admin bool, method, path string, body any, cookie *http.Cookie, csrf, origin string) *httptest.ResponseRecorder {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	base := a.cfg.PublicOrigin
	if admin {
		base = a.cfg.AdminOrigin
	}
	r := httptest.NewRequest(method, base+path, bytes.NewReader(data))
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	if admin {
		a.adminHandler().ServeHTTP(w, r)
	} else {
		a.publicHandler().ServeHTTP(w, r)
	}
	return w
}
func bootstrap(t *testing.T, a *App) (*http.Cookie, string) {
	t.Helper()
	w := request(a, true, "POST", "/api/admin/bootstrap", map[string]string{"token": a.cfg.BootstrapToken}, nil, "", a.cfg.AdminOrigin)
	if w.Code != 200 {
		t.Fatalf("bootstrap status %d", w.Code)
	}
	var body struct {
		CSRF string `json:"csrfToken"`
	}
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.CSRF == "" {
		t.Fatal("missing csrf")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == a.cookieName() {
			return c, body.CSRF
		}
	}
	t.Fatal("missing owner cookie")
	return nil, ""
}
func TestHTTPIsolationCSRFHostAndPersistentSession(t *testing.T) {
	a, _ := testApp(t)
	for _, path := range []string{"/api/admin/settings", "/api/admin/session", "/admin.js", "/data/booking.sqlite", "/config/google-client.json"} {
		if w := request(a, false, "GET", path, nil, nil, "", ""); w.Code != 404 {
			t.Fatalf("public route leaked %s", path)
		}
	}
	if w := request(a, true, "GET", "/api/admin/settings", nil, nil, "", ""); w.Code != 401 {
		t.Fatal("anonymous settings allowed")
	}
	cookie, csrf := bootstrap(t, a)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe session cookie")
	}
	if w := request(a, true, "PUT", "/api/admin/settings", defaultSettings(), cookie, "", a.cfg.AdminOrigin); w.Code != 403 {
		t.Fatal("missing csrf accepted")
	}
	if w := request(a, true, "PUT", "/api/admin/settings", defaultSettings(), cookie, csrf, "https://attacker.invalid"); w.Code != 403 {
		t.Fatal("foreign origin accepted")
	}
	if w := request(a, true, "PUT", "/api/admin/settings", defaultSettings(), cookie, csrf, a.cfg.AdminOrigin); w.Code != 200 {
		t.Fatalf("valid settings status %d", w.Code)
	}
	r := httptest.NewRequest("GET", "http://attacker.invalid/api/admin/session", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	a.adminHandler().ServeHTTP(w, r)
	if w.Code != 421 {
		t.Fatal("host rebinding accepted")
	}
	other, e := newApp(a.cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer other.store.close()
	if w = request(other, true, "GET", "/api/admin/settings", nil, cookie, "", ""); w.Code != 200 {
		t.Fatal("persistent session lost")
	}
	if w = request(a, true, "POST", "/api/admin/logout", nil, cookie, csrf, a.cfg.AdminOrigin); w.Code != 204 {
		t.Fatal("logout failed")
	}
	if w = request(other, true, "GET", "/api/admin/settings", nil, cookie, "", ""); w.Code != 401 {
		t.Fatal("logged-out session still usable")
	}
}
func TestEncryptedSecretsTamperAndInstallIsolation(t *testing.T) {
	a, _ := testApp(t)
	secret := "fixture-refresh-token-not-for-logs"
	if err := a.store.putSecret("fixture", map[string]string{"refresh": secret}); err != nil {
		t.Fatal(err)
	}
	cookie, _ := bootstrap(t, a)
	var raw []byte
	if err := a.store.db.QueryRow("SELECT value FROM secrets WHERE key='fixture'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("plaintext token stored")
	}
	var sessionRaw []byte
	if err := a.store.db.QueryRow("SELECT value FROM sessions LIMIT 1").Scan(&sessionRaw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sessionRaw, []byte(cookie.Value)) {
		t.Fatal("plaintext session token stored")
	}
	if _, err := a.store.open(raw, "other-purpose"); err == nil {
		t.Fatal("encrypted data lacked purpose binding")
	}
	raw[len(raw)-1] ^= 1
	if _, err := a.store.open(raw, "fixture"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	b, _ := testApp(t)
	if a.cookieName() == b.cookieName() {
		t.Fatal("clones share a session cookie name")
	}
	if w := request(b, true, "GET", "/api/admin/settings", nil, cookie, "", ""); w.Code != 401 {
		t.Fatal("another installation accepted owner session")
	}
	wrong := a.cfg
	wrong.BootstrapToken = strings.Repeat("different", 8)
	if other, err := newApp(wrong); err == nil {
		other.store.close()
		t.Fatal("changed bootstrap credential silently replaced installation")
	}
	backup := t.TempDir()
	dbBytes, _ := os.ReadFile(filepath.Join(a.cfg.DataDir, "booking.sqlite"))
	if err := os.WriteFile(filepath.Join(backup, "booking.sqlite"), dbBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := openStore(backup, a.cfg.BootstrapToken); err == nil {
		s.close()
		t.Fatal("existing database silently received a new encryption key")
	}
}
