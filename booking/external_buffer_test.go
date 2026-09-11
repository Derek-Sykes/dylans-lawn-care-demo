package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func minutePointer(minutes int) *int { return &minutes }

func extendedWorkday(t *testing.T, a *App) Settings {
	t.Helper()
	v := defaultSettings()
	v.Weekly = []WeeklyPeriod{{1, "09:00", "19:00"}}
	if _, err := a.saveSettings(v); err != nil {
		t.Fatal(err)
	}
	return v
}

func slotTimes(t *testing.T, slots []Slot, zone string) string {
	t.Helper()
	loc := mustLocation(t, zone)
	parts := []string{}
	for _, slot := range slots {
		parts = append(parts, slot.Start.In(loc).Format("15:04"))
	}
	return strings.Join(parts, ",")
}

func TestExternalWorkdayBufferReanchorsFirstAppointment(t *testing.T) {
	for _, tc := range []struct {
		gap  int
		want string
	}{{30, "17:30"}, {15, "17:15"}, {0, "17:00"}} {
		a, f := testApp(t)
		v := extendedWorkday(t, a)
		v.ExternalBufferMinutes = minutePointer(tc.gap)
		if _, err := a.saveSettings(v); err != nil {
			t.Fatal(err)
		}
		f.busy = []Busy{{instant("2026-09-14T13:00:00Z"), instant("2026-09-14T21:00:00Z")}}
		slots, _, err := a.availableSlots(context.Background(), "2026-09-14")
		if err != nil || slotTimes(t, slots, v.TimeZone) != tc.want {
			t.Fatalf("external gap%d: starts=%s err=%v; want%s", tc.gap, slotTimes(t, slots, v.TimeZone), err, tc.want)
		}
		in := testInput("anchored-after-work-001")
		in.Start = slots[0].Start.Format(time.RFC3339)
		booking, _, err := a.createBooking(context.Background(), in)
		if err != nil || booking.End.Sub(booking.Start) != time.Hour {
			t.Fatalf("new anchored time could not be booked: %v", err)
		}
		in.IdempotencyKey = "anchored-overlap-second-001"
		if _, _, err = a.createBooking(context.Background(), in); !errors.Is(err, errConflict) {
			t.Fatal("newly anchored appointment did not protect against double booking")
		}
	}
}

func TestExternalMultipleConflictsAndTimeOffReanchorWindows(t *testing.T) {
	a, f := testApp(t)
	v := extendedWorkday(t, a)
	f.busy = []Busy{{instant("2026-09-14T13:00:00Z"), instant("2026-09-14T14:00:00Z")}, {instant("2026-09-14T16:00:00Z"), instant("2026-09-14T17:00:00Z")}}
	slots, _, err := a.availableSlots(context.Background(), "2026-09-14")
	if err != nil || slotTimes(t, slots, v.TimeZone) != "10:30,13:30,14:45,16:00,17:15" {
		t.Fatalf("multiple conflicts did not reanchor each remaining window: %s %v", slotTimes(t, slots, v.TimeZone), err)
	}
	v.BlockedWeekly = []BlockedWeeklyPeriod{{Weekday: 1, Start: "14:30", End: "15:00"}}
	if _, err = a.saveSettings(v); err != nil {
		t.Fatal(err)
	}
	slots, _, err = a.availableSlots(context.Background(), "2026-09-14")
	if err != nil || slotTimes(t, slots, v.TimeZone) != "10:30,15:00,16:15,17:30" {
		t.Fatalf("time off retained a stale opening-time grid: %s %v", slotTimes(t, slots, v.TimeZone), err)
	}
}

func TestBusinessBufferRemainsSeparateAndUsesSavedAppointmentGap(t *testing.T) {
	a, _ := testApp(t)
	v := extendedWorkday(t, a)
	b, _, err := a.createBooking(context.Background(), testInput("business-buffer-first-001"))
	if err != nil {
		t.Fatal(err)
	}
	slots, _, err := a.availableSlots(context.Background(), "2026-09-14")
	if err != nil || len(slots) == 0 || !slots[0].Start.Equal(b.End.Add(15*time.Minute)) {
		t.Fatal("a business appointment incorrectly received the 30-minute external gap")
	}
	v.BufferMinutes = 45
	if _, err = a.saveSettings(v); err != nil {
		t.Fatal(err)
	}
	slots, _, err = a.availableSlots(context.Background(), "2026-09-14")
	if err != nil || len(slots) == 0 || !slots[0].Start.Equal(b.End.Add(15*time.Minute)) {
		t.Fatal("changing the default gap rewrote an existing appointment's saved gap")
	}
	input := testInput("business-buffer-second-001")
	input.Start = slots[0].Start.Format(time.RFC3339)
	second, _, err := a.createBooking(context.Background(), input)
	if err != nil || second.BlockedEnd.Sub(second.End) != 45*time.Minute {
		t.Fatal("new business appointment did not use the updated gap")
	}
}

func TestExternalZeroAllowsExactEdgesWithoutBusinessGapAdded(t *testing.T) {
	a, f := testApp(t)
	v := extendedWorkday(t, a)
	v.Weekly = []WeeklyPeriod{{1, "10:00", "13:00"}}
	v.ExternalBufferMinutes = minutePointer(0)
	if _, err := a.saveSettings(v); err != nil {
		t.Fatal(err)
	}
	f.busy = []Busy{{instant("2026-09-14T15:00:00Z"), instant("2026-09-14T16:00:00Z")}}
	slots, _, err := a.availableSlots(context.Background(), "2026-09-14")
	if err != nil || slotTimes(t, slots, v.TimeZone) != "10:00,12:00" {
		t.Fatalf("zero external gap still applied business padding: %s %v", slotTimes(t, slots, v.TimeZone), err)
	}
}

func TestExternalEstimateAndNoticeUseSameFreeIntervals(t *testing.T) {
	a, f := testApp(t)
	v := extendedWorkday(t, a)
	v.Weekly = []WeeklyPeriod{{1, "09:00", "18:30"}}
	if _, err := a.saveSettings(v); err != nil {
		t.Fatal(err)
	}
	f.busy = []Busy{{instant("2026-09-14T13:00:00Z"), instant("2026-09-14T21:00:00Z")}}
	slots, _, err := a.availableSlots(context.Background(), "2026-09-14", "estimate")
	if err != nil || slotTimes(t, slots, v.TimeZone) != "17:30,18:00" {
		t.Fatalf("estimate free interval/cadence incorrect: %s %v", slotTimes(t, slots, v.TimeZone), err)
	}
	in := testInput("external-estimate-buffer-001")
	in.Kind = "estimate"
	in.Start = slots[0].Start.Format(time.RFC3339)
	if b, _, err := a.createBooking(context.Background(), in); err != nil || b.End.Sub(b.Start) != 15*time.Minute {
		t.Fatal("estimate did not book the reanchored 15-minute time")
	}
	a.now = func() time.Time { return instant("2026-09-13T22:02:30Z") }
	slots, _, err = a.availableSlots(context.Background(), "2026-09-14", "estimate")
	if err != nil || slotTimes(t, slots, v.TimeZone) != "18:03" {
		t.Fatalf("notice did not reanchor to the next eligible minute: %s %v", slotTimes(t, slots, v.TimeZone), err)
	}
}

func TestExternalBufferLegacyOmissionAndExplicitZeroPersist(t *testing.T) {
	a, _ := testApp(t)
	v := defaultSettings()
	raw, _ := json.Marshal(v)
	var legacy map[string]any
	_ = json.Unmarshal(raw, &legacy)
	delete(legacy, "externalBufferMinutes")
	if err := a.store.putJSON("settings", legacy); err != nil {
		t.Fatal(err)
	}
	saved, err := a.store.settings()
	if err != nil || externalBufferMinutes(saved) != 30 {
		t.Fatal("legacy installation did not receive the new external-gap default")
	}
	saved.ExternalBufferMinutes = minutePointer(0)
	if _, err = a.saveSettings(saved); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := bootstrap(t, a)
	w := request(a, true, "PUT", "/api/admin/settings", legacy, cookie, csrf, a.cfg.AdminOrigin)
	saved, _ = a.store.settings()
	if w.Code != 200 || externalBufferMinutes(saved) != 0 {
		t.Fatal("an older client's settings update overwrote an explicit zero external gap")
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	saved, _ = other.store.settings()
	if externalBufferMinutes(saved) != 0 {
		t.Fatal("explicit zero external gap did not survive reopening the data")
	}
	for _, invalid := range []int{-1, 121} {
		saved.ExternalBufferMinutes = minutePointer(invalid)
		if validateSettings(saved) == nil {
			t.Fatal("invalid external gap accepted")
		}
	}
}

func TestGoogleBusyProvenanceKeepsUnknownOverlapsExternal(t *testing.T) {
	for _, source := range []string{"business-only", "primary-overlap", "unknown-dedicated-overlap", "transparent-dedicated"} {
		t.Run(source, func(t *testing.T) {
			a, f, b := syncedGoogleBooking(t)
			if source == "primary-overlap" {
				a.google.client.Transport = calendarRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if strings.HasSuffix(r.URL.Path, "/freeBusy") {
						w := httptest.NewRecorder()
						writeJSON(w, 200, map[string]any{"calendars": map[string]any{"primary": map[string]any{"busy": []Busy{{b.Start, b.End}}}, "fixture-booking-calendar": map[string]any{"busy": []Busy{{b.Start, b.End}}}}})
						return w.Result(), nil
					}
					return http.DefaultTransport.RoundTrip(r)
				})
			}
			if source == "unknown-dedicated-overlap" || source == "transparent-dedicated" {
				f.mu.Lock()
				f.events["outside-job"] = map[string]any{"id": "outside-job", "start": map[string]any{"dateTime": b.Start.Format(time.RFC3339)}, "end": map[string]any{"dateTime": b.End.Format(time.RFC3339)}}
				if source == "transparent-dedicated" {
					f.events["outside-job"]["transparency"] = "transparent"
				}
				f.mu.Unlock()
			}
			slots, _, err := a.availableSlots(context.Background(), "2026-09-14")
			gap := 15
			if source == "primary-overlap" || source == "unknown-dedicated-overlap" {
				gap = 30
			}
			if err != nil || len(slots) == 0 || !slots[0].Start.Equal(b.End.Add(time.Duration(gap)*time.Minute)) {
				t.Fatalf("source%s used wrong gap; slots%v error%v", source, slots, err)
			}
		})
	}
}

func TestGoogleInsertRetryExcludesItsOwnLocalReservation(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	b, _, err := a.createBooking(context.Background(), testInput("outbox-excludes-own-reservation-001"))
	if err != nil {
		t.Fatal(err)
	}
	a.syncDue(context.Background())
	saved, _ := a.store.booking(b.ID)
	if saved.CalendarStatus != "synced" || f.inserts != 1 {
		t.Fatal("outbox treated its own pending local reservation as another conflicting appointment")
	}
}
