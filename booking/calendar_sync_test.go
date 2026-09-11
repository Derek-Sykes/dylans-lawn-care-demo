package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func syncedGoogleBooking(t *testing.T) (*App, *googleFixture, Booking) {
	t.Helper()
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	b, _, err := a.createBooking(context.Background(), testInput("calendar-edits-request-001"))
	if err != nil {
		t.Fatal(err)
	}
	a.syncDue(context.Background())
	b, err = a.store.booking(b.ID)
	if err != nil || b.CalendarStatus != "synced" {
		t.Fatal("fixture appointment did not sync")
	}
	return a, f, b
}

func changeGoogleTimes(f *googleFixture, b Booking, start, end string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[b.EventID]["start"] = map[string]any{"dateTime": start}
	f.events[b.EventID]["end"] = map[string]any{"dateTime": end}
}

func hasSlot(slots []Slot, start time.Time) bool {
	for _, slot := range slots {
		if slot.Start.Equal(start) {
			return true
		}
	}
	return false
}

func TestCalendarMoveAndDurationReachOwnerAndAvailability(t *testing.T) {
	a, f, b := syncedGoogleBooking(t)
	start, end := instant("2026-09-14T15:30:00Z"), instant("2026-09-14T17:30:00Z")
	changeGoogleTimes(f, b, start.Format(time.RFC3339), end.Format(time.RFC3339))
	cookie, _ := testIdentitySession(t, a, Owner{Sub: "owner-sub", Email: "owner@example.com"})
	w := request(a, true, "GET", "/api/admin/bookings", nil, cookie, "", "")
	var reply struct {
		Bookings          []Booking `json:"bookings"`
		CalendarSyncError string    `json:"calendarSyncError"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil || len(reply.Bookings) != 1 || reply.CalendarSyncError != "" {
		t.Fatalf("owner refresh failed: HTTP %d", w.Code)
	}
	got := reply.Bookings[0]
	if !got.Start.Equal(start) || !got.End.Equal(end) || got.Status != "needs_followup" || got.CalendarStatus != "synced" {
		t.Fatal("owner list did not adopt the edited job time without changing its follow-up state")
	}
	saved, err := a.store.booking(b.ID)
	if err != nil || saved.BlockedEnd.Sub(saved.End) != b.BlockedEnd.Sub(b.End) {
		t.Fatal("resized appointment lost its booking buffer")
	}
	slots, _, err := a.availableSlots(context.Background(), "2026-09-14")
	if err != nil || !hasSlot(slots, b.Start) || hasSlot(slots, start) || hasSlot(slots, instant("2026-09-14T16:45:00Z")) {
		t.Fatal("availability did not free the previous time and protect the longer replacement")
	}
	v, _ := a.store.settings()
	v.SlotMinutes = 45
	if _, err = a.saveSettings(v); err != nil {
		t.Fatal(err)
	}
	if _, err = a.patchBooking(b.ID, ptr("confirmed"), ptr("Keep the longer job")); err != nil {
		t.Fatal(err)
	}
	a.syncDue(context.Background())
	saved, _ = a.store.booking(b.ID)
	if !saved.Start.Equal(start) || !saved.End.Equal(end) || saved.Status != "confirmed" || saved.AdminNotes != "Keep the longer job" || f.inserts != 1 {
		t.Fatal("changing settings or appointment notes/status replaced the customized duration")
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	stored, _ := other.store.booking(b.ID)
	var cursor calendarCursor
	if !stored.Start.Equal(start) || !stored.End.Equal(end) || other.store.getSecret("calendar_cursor", &cursor) != nil || cursor.Token == "" {
		t.Fatal("updated times or sync cursor did not survive reopening the persistent installation")
	}
}

func TestCalendarDeletedEventCancelsAndReleasesTime(t *testing.T) {
	a, f, b := syncedGoogleBooking(t)
	f.mu.Lock()
	f.events[b.EventID] = map[string]any{"id": b.EventID, "status": "cancelled"}
	f.mu.Unlock()
	slots, _, err := a.availableSlots(context.Background(), "2026-09-14")
	if err != nil || !hasSlot(slots, b.Start) {
		t.Fatal("Google cancellation did not release the previous time")
	}
	saved, _ := a.store.booking(b.ID)
	if saved.Status != "cancelled" || saved.CalendarStatus != "synced" {
		t.Fatal("Google cancellation was not reflected in the owner record")
	}
	if _, err = a.patchBooking(b.ID, ptr("confirmed"), nil); err == nil {
		t.Fatal("a deleted Google appointment was reopened by a status edit")
	}
}

func TestCalendarAllDayEditUsesCalendarTimezoneAndExclusiveEnd(t *testing.T) {
	a, f, b := syncedGoogleBooking(t)
	f.mu.Lock()
	f.events[b.EventID]["start"] = map[string]any{"date": "2026-11-01"}
	f.events[b.EventID]["end"] = map[string]any{"date": "2026-11-02"}
	f.mu.Unlock()
	if err := a.google.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	saved, _ := a.store.booking(b.ID)
	if !saved.Start.Equal(instant("2026-11-01T04:00:00Z")) || !saved.End.Equal(instant("2026-11-02T05:00:00Z")) || saved.End.Sub(saved.Start) != 25*time.Hour {
		t.Fatal("all-day event ignored the calendar's DST boundary or exclusive end date")
	}
}

func TestCalendarExternalOverlapRemainsAccurateAndBlocksFurtherBookings(t *testing.T) {
	a, f, first := syncedGoogleBooking(t)
	in := testInput("calendar-overlap-second-001")
	in.Start = "2026-09-14T15:30:00Z"
	second, _, err := a.createBooking(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	a.syncDue(context.Background())
	changeGoogleTimes(f, first, second.Start.Format(time.RFC3339), second.End.Add(time.Hour).Format(time.RFC3339))
	if err = a.google.Refresh(context.Background(), true); err != nil {
		t.Fatal("an owner-created overlap prevented importing the actual Calendar times")
	}
	saved, _ := a.store.booking(first.ID)
	if !saved.Start.Equal(second.Start) || !saved.End.Equal(second.End.Add(time.Hour)) {
		t.Fatal("external overlap was silently reverted")
	}
	in.IdempotencyKey = "calendar-overlap-third-001"
	if _, _, err = a.createBooking(context.Background(), in); !errors.Is(err, errConflict) {
		t.Fatal("overlapping edited Google appointments allowed an additional website booking")
	}
}

type calendarListTransport struct {
	mu      sync.Mutex
	base    http.RoundTripper
	respond func(http.ResponseWriter, *http.Request)
	queries []url.Values
}

type calendarRoundTripFunc func(*http.Request) (*http.Response, error)

func (f calendarRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (transport *calendarListTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/events") {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		transport.queries = append(transport.queries, r.URL.Query())
		w := httptest.NewRecorder()
		transport.respond(w, r)
		return w.Result(), nil
	}
	return transport.base.RoundTrip(r)
}

func useCalendarListTransport(a *App, respond func(http.ResponseWriter, *http.Request)) *calendarListTransport {
	transport := &calendarListTransport{base: http.DefaultTransport, respond: respond}
	a.google.client.Transport = transport
	return transport
}

func TestCalendarIncrementalPaginationIsAtomicAndExpiresSafely(t *testing.T) {
	a, f, b := syncedGoogleBooking(t)
	changeGoogleTimes(f, b, "2026-09-14T15:30:00Z", "2026-09-14T17:30:00Z")
	f.mu.Lock()
	edited := f.events[b.EventID]
	f.mu.Unlock()
	failPage := true
	transport := useCalendarListTransport(a, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("syncToken") == "fixture-sync-token" {
			w.WriteHeader(410)
			return
		}
		if r.URL.Query().Get("pageToken") == "" {
			writeJSON(w, 200, map[string]any{"items": []any{edited}, "nextPageToken": "second-page", "timeZone": "America/New_York"})
			return
		}
		if failPage {
			w.WriteHeader(503)
			return
		}
		writeJSON(w, 200, map[string]any{"items": []any{}, "nextSyncToken": "after-full-snapshot"})
	})
	if err := a.google.Refresh(context.Background(), true); err == nil {
		t.Fatal("a failed later page was reported as a successful sync")
	}
	saved, _ := a.store.booking(b.ID)
	var cursor calendarCursor
	_ = a.store.getSecret("calendar_cursor", &cursor)
	if !saved.Start.Equal(b.Start) || cursor.Token != "fixture-sync-token" {
		t.Fatal("partial results or an expired cursor were committed before the replacement snapshot completed")
	}
	failPage = false
	if err := a.google.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	saved, _ = a.store.booking(b.ID)
	_ = a.store.getSecret("calendar_cursor", &cursor)
	if !saved.Start.Equal(instant("2026-09-14T15:30:00Z")) || cursor.Token != "after-full-snapshot" {
		t.Fatal("completed replacement snapshot did not atomically save times and the new cursor")
	}
	for _, query := range transport.queries {
		if query.Get("showDeleted") != "true" || query.Get("singleEvents") != "false" || query.Get("timeMin") != "" || query.Get("privateExtendedProperty") != "" {
			t.Fatal("incremental request used incompatible filters or omitted deletion handling")
		}
	}
	// The next incremental response can be empty without deleting unchanged jobs.
	transport.respond = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("syncToken") != "after-full-snapshot" {
			t.Fatal("new cursor was not reused")
		}
		writeJSON(w, 200, map[string]any{"items": []any{}, "nextSyncToken": "next-incremental"})
	}
	if err := a.google.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	saved, _ = a.store.booking(b.ID)
	if saved.Status == "cancelled" {
		t.Fatal("empty incremental page cancelled an unchanged booking")
	}
}

func TestCalendarFullSnapshotMissingEventAndUnknownEvents(t *testing.T) {
	a, _, b := syncedGoogleBooking(t)
	_, _ = a.store.db.Exec("DELETE FROM secrets WHERE key='calendar_cursor'")
	useCalendarListTransport(a, func(w http.ResponseWriter, r *http.Request) {
		// Unrelated calendar entries may have arbitrary data; only known booking IDs are imported.
		writeJSON(w, 200, map[string]any{"items": []any{map[string]any{"id": "unrelated", "recurrence": []string{"RRULE:FREQ=DAILY"}}}, "nextSyncToken": "complete", "timeZone": "America/New_York"})
	})
	if err := a.google.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	saved, _ := a.store.booking(b.ID)
	if saved.Status != "cancelled" || saved.CalendarStatus != "synced" {
		t.Fatal("complete snapshot did not reconcile an event moved out of the booking calendar")
	}
}

func TestCalendarReadFailureFailsClosedButOwnerRetainsSavedRecords(t *testing.T) {
	a, _, b := syncedGoogleBooking(t)
	useCalendarListTransport(a, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	if _, _, err := a.availableSlots(context.Background(), "2026-09-14"); !errors.Is(err, errUnavailable) {
		t.Fatal("slots stayed bookable when edited appointment times could not be checked")
	}
	in := testInput("calendar-read-unavailable-001")
	in.Start = "2026-09-14T15:30:00Z"
	if _, _, err := a.createBooking(context.Background(), in); !errors.Is(err, errUnavailable) {
		t.Fatal("submission was accepted without refreshing changed appointment times")
	}
	cookie, _ := testIdentitySession(t, a, Owner{Sub: "owner-sub", Email: "owner@example.com"})
	w := request(a, true, "GET", "/api/admin/bookings", nil, cookie, "", "")
	var reply struct {
		Bookings          []Booking `json:"bookings"`
		CalendarSyncError string    `json:"calendarSyncError"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil || len(reply.Bookings) != 1 || reply.CalendarSyncError == "" || !reply.Bookings[0].Start.Equal(b.Start) {
		t.Fatal("provider failure hid existing appointments or showed stale data without a warning")
	}
}

func TestCalendarRetryAdoptsOwnerEditAfterLostInsertResponse(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	b, _, err := a.createBooking(context.Background(), testInput("calendar-lost-insert-001"))
	if err != nil {
		t.Fatal(err)
	}
	f.loseInsert = true
	a.syncDue(context.Background())
	changeGoogleTimes(f, b, "2026-09-14T15:30:00Z", "2026-09-14T18:00:00Z")
	_, _ = a.store.db.Exec("UPDATE bookings SET next_attempt=0 WHERE id=?", b.ID)
	a.syncDue(context.Background())
	saved, _ := a.store.booking(b.ID)
	if saved.CalendarStatus != "synced" || !saved.End.Equal(instant("2026-09-14T18:00:00Z")) || !saved.Start.Equal(instant("2026-09-14T15:30:00Z")) || f.inserts != 1 {
		t.Fatal("outbox retry reverted or duplicated an appointment edited after its insert response was lost")
	}
}

func TestCalendarAllDayRetryUsesRemoteCalendarTimezone(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	b, _, err := a.createBooking(context.Background(), testInput("calendar-all-day-retry-001"))
	if err != nil {
		t.Fatal(err)
	}
	f.loseInsert = true
	a.syncDue(context.Background())
	f.mu.Lock()
	f.events[b.EventID]["start"] = map[string]any{"date": "2026-09-14"}
	f.events[b.EventID]["end"] = map[string]any{"date": "2026-09-15"}
	f.mu.Unlock()
	a.google.client.Transport = calendarRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/calendars/fixture-booking-calendar") {
			w := httptest.NewRecorder()
			writeJSON(w, 200, map[string]string{"timeZone": "America/Los_Angeles"})
			return w.Result(), nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	_, _ = a.store.db.Exec("UPDATE bookings SET next_attempt=0 WHERE id=?", b.ID)
	a.syncDue(context.Background())
	saved, _ := a.store.booking(b.ID)
	if saved.CalendarStatus != "synced" || !saved.Start.Equal(instant("2026-09-14T07:00:00Z")) || !saved.End.Equal(instant("2026-09-15T07:00:00Z")) {
		t.Fatal("all-day retry interpreted dates using the portal timezone instead of the changed Google calendar timezone")
	}
}

func TestCalendarPortalCancellationWinsOverConcurrentTimeEdit(t *testing.T) {
	a, f, b := syncedGoogleBooking(t)
	if _, err := a.patchBooking(b.ID, ptr("cancelled"), nil); err != nil {
		t.Fatal(err)
	}
	changeGoogleTimes(f, b, "2026-09-14T15:30:00Z", "2026-09-14T17:30:00Z")
	if err := a.google.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	saved, _ := a.store.booking(b.ID)
	if saved.Status != "cancelled" || saved.CalendarStatus != "pending" || !saved.Start.Equal(instant("2026-09-14T15:30:00Z")) {
		t.Fatal("an inbound time change reopened the owner's cancelled booking")
	}
	a.syncDue(context.Background())
	saved, _ = a.store.booking(b.ID)
	if saved.Status != "cancelled" || saved.CalendarStatus != "synced" || len(f.events) != 0 {
		t.Fatal("cancelled appointment was not removed from Google after its timing change")
	}
}

func TestCalendarInvalidKnownEventDoesNotAdvanceCursor(t *testing.T) {
	for _, kind := range []string{"recurring", "reversed", "detached"} {
		t.Run(kind, func(t *testing.T) {
			a, f, b := syncedGoogleBooking(t)
			f.mu.Lock()
			switch kind {
			case "recurring":
				f.events[b.EventID]["recurrence"] = []string{"RRULE:FREQ=WEEKLY"}
			case "reversed":
				f.events[b.EventID]["end"] = map[string]any{"dateTime": b.Start.Add(-time.Hour).Format(time.RFC3339)}
			case "detached":
				delete(f.events[b.EventID], "extendedProperties")
			}
			f.mu.Unlock()
			if err := a.google.Refresh(context.Background(), true); err == nil {
				t.Fatal("unsupported or invalid known event was silently accepted")
			}
			saved, _ := a.store.booking(b.ID)
			if !saved.Start.Equal(b.Start) || !saved.End.Equal(b.End) || saved.Status == "cancelled" {
				t.Fatal("invalid incoming event damaged the last known booking")
			}
		})
	}
}

func TestCalendarBackgroundRefreshIsThrottledAndFreshReadsBypassIt(t *testing.T) {
	a, _, _ := syncedGoogleBooking(t)
	transport := useCalendarListTransport(a, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"items": []any{}, "nextSyncToken": "background"})
	})
	for n := 0; n < 12; n++ {
		if err := a.google.Refresh(context.Background(), false); err != nil {
			t.Fatal(err)
		}
	}
	if len(transport.queries) != 0 {
		t.Fatal("background loop ignored the successful refresh interval")
	}
	if err := a.google.Refresh(context.Background(), true); err != nil || len(transport.queries) != 1 {
		t.Fatal("owner/public fresh read was incorrectly served from the background cache")
	}
}

func TestCalendarEstimateKindAndCustomDurationRoundTrip(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	in := testInput("calendar-estimate-roundtrip-001")
	in.Kind = "estimate"
	b, _, err := a.createBooking(context.Background(), in)
	if err != nil || b.Kind != "estimate" || b.End.Sub(b.Start) != 15*time.Minute {
		t.Fatal("estimate did not retain its own default duration and kind")
	}
	a.syncDue(context.Background())
	f.mu.Lock()
	event := f.events[b.EventID]
	summary, _ := event["summary"].(string)
	description, _ := event["description"].(string)
	f.mu.Unlock()
	if summary != "Estimate / callback · Lawn care · Sample Customer" || !strings.Contains(description, "not performing the service job") || !strings.Contains(description, b.Phone) {
		t.Fatal("Google estimate event was not clearly distinguished from a service appointment")
	}
	changeGoogleTimes(f, b, "2026-09-14T15:30:00Z", "2026-09-14T16:15:00Z")
	if err = a.google.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	saved, _ := a.store.booking(b.ID)
	if saved.Kind != "estimate" || saved.End.Sub(saved.Start) != 45*time.Minute || !saved.Start.Equal(instant("2026-09-14T15:30:00Z")) {
		t.Fatal("importing a custom Google duration changed the request kind or retained its old length")
	}
}
