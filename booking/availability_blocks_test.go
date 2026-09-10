package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBlockedWeeklyAndDatesSubtractFromWorkingWindows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		weekly []BlockedWeeklyPeriod
		dates  []BlockedDatePeriod
		want   string
	}{
		{"working windows", nil, nil, "09:00,09:45,10:30,11:15,13:00,13:45,14:30,15:15,16:00"},
		{"weekly whole day", []BlockedWeeklyPeriod{{Weekday: 1, AllDay: true}}, nil, ""},
		{"date whole day", nil, []BlockedDatePeriod{{Date: "2026-09-14", AllDay: true}}, ""},
		{"weekly partial", []BlockedWeeklyPeriod{{Weekday: 1, Start: "10:00", End: "11:30"}}, nil, "09:00,13:00,13:45,14:30,15:15,16:00"},
		{"date partial", nil, []BlockedDatePeriod{{Date: "2026-09-14", Start: "14:00", End: "15:00"}}, "09:00,09:45,10:30,11:15,13:00,15:15,16:00"},
		{"overlapping blocks form union", []BlockedWeeklyPeriod{{Weekday: 1, Start: "10:00", End: "11:00"}, {Weekday: 1, Start: "10:30", End: "11:30"}}, []BlockedDatePeriod{{Date: "2026-09-14", Start: "14:00", End: "15:00"}, {Date: "2026-09-14", Start: "14:30", End: "15:00"}}, "09:00,13:00,15:15,16:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := defaultSettings()
			v.SlotMinutes = 30
			v.Weekly = []WeeklyPeriod{{1, "09:00", "12:00"}, {1, "13:00", "17:00"}, {2, "09:00", "10:00"}}
			v.BlockedWeekly = tc.weekly
			v.BlockedDates = tc.dates
			slots, err := scheduledSlots(v, "2026-09-14", instant("2026-09-10T20:16:00Z"))
			if err != nil {
				t.Fatal(err)
			}
			var starts []string
			loc := mustLocation(t, v.TimeZone)
			for _, s := range slots {
				starts = append(starts, s.Start.In(loc).Format("15:04"))
			}
			if got := strings.Join(starts, ","); got != tc.want {
				t.Fatalf("remaining times %s, want %s", got, tc.want)
			}
			other, err := scheduledSlots(v, "2026-09-15", instant("2026-09-10T20:16:00Z"))
			if err != nil || len(other) != 1 {
				t.Fatal("Monday block affected Tuesday")
			}
		})
	}
}

func TestBlockedTimeOverridesCustomDateHoursAndProtectsBuffer(t *testing.T) {
	for _, tc := range []struct {
		start, end string
		want       int
	}{{"09:40", "09:45", 0}, {"09:45", "10:00", 1}, {"08:55", "09:00", 1}} {
		v := defaultSettings()
		v.SlotMinutes = 30
		v.Exceptions = []DateException{{Date: "2026-09-14", Start: "09:00", End: "10:00"}}
		v.BlockedDates = []BlockedDatePeriod{{Date: "2026-09-14", Start: tc.start, End: tc.end}}
		slots, err := scheduledSlots(v, "2026-09-14", instant("2026-09-10T12:00:00Z"))
		if err != nil || len(slots) != tc.want {
			t.Fatalf("buffer block %s–%s: got %d, want %d", tc.start, tc.end, len(slots), tc.want)
		}
	}
	v := defaultSettings()
	v.Exceptions = []DateException{{Date: "2026-09-14", Start: "08:00", End: "20:00"}}
	v.BlockedWeekly = []BlockedWeeklyPeriod{{Weekday: 1, AllDay: true}}
	if slots, err := scheduledSlots(v, "2026-09-14", instant("2026-09-10T12:00:00Z")); err != nil || len(slots) != 0 {
		t.Fatal("custom date hours overrode a whole-day block")
	}
	v = defaultSettings()
	v.SlotMinutes = 30
	v.Weekly = []WeeklyPeriod{{0, "23:00", "23:59"}}
	v.BufferMinutes = 60
	v.BlockedDates = []BlockedDatePeriod{{Date: "2026-09-14", AllDay: true}}
	if slots, err := scheduledSlots(v, "2026-09-13", instant("2026-09-10T12:00:00Z")); err != nil || len(slots) != 0 {
		t.Fatal("appointment buffer entered the next blocked date")
	}
	v.BlockedDates = nil
	v.BlockedWeekly = []BlockedWeeklyPeriod{{Weekday: 1, AllDay: true}}
	if slots, err := scheduledSlots(v, "2026-09-13", instant("2026-09-10T12:00:00Z")); err != nil || len(slots) != 0 {
		t.Fatal("appointment buffer entered the next blocked weekday")
	}
}

func TestBlockedTimesAcrossDST(t *testing.T) {
	for _, tc := range []struct {
		date, now string
		want      int
	}{{"2026-03-08", "2026-03-01T12:00:00Z", 3}, {"2026-11-01", "2026-10-01T12:00:00Z", 2}} {
		v := defaultSettings()
		v.SlotMinutes = 30
		v.BufferMinutes = 0
		v.MinNoticeHours = 0
		v.HorizonDays = 90
		v.Weekly = []WeeklyPeriod{{0, "00:00", "04:00"}}
		v.BlockedDates = []BlockedDatePeriod{{Date: tc.date, Start: "01:15", End: "03:15"}}
		slots, err := scheduledSlots(v, tc.date, instant(tc.now))
		if err != nil || len(slots) != tc.want {
			t.Fatalf("DST blocks %s: got %d slots, want %d", tc.date, len(slots), tc.want)
		}
	}
	v := defaultSettings()
	v.BlockedWeekly = []BlockedWeeklyPeriod{{Weekday: 0, Start: "01:45", End: "02:00"}}
	blocks := compileBlocks(v)
	loc := mustLocation(t, v.TimeZone)
	for _, start := range []string{"2026-11-01T05:00:00Z", "2026-11-01T06:00:00Z"} {
		at := instant(start)
		if !blocks.overlaps(Slot{at, at.Add(30 * time.Minute)}, 30, loc) {
			t.Fatal("a repeated DST-hour block was missed by the appointment buffer")
		}
	}
}

func TestBlockedSettingsValidationAndLegacyPersistence(t *testing.T) {
	a, _ := testApp(t)
	original := defaultSettings()
	raw, _ := json.Marshal(original)
	var legacy map[string]any
	_ = json.Unmarshal(raw, &legacy)
	delete(legacy, "blockedWeekly")
	delete(legacy, "blockedDates")
	if err := a.store.putJSON("settings", legacy); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.store.settings()
	if err != nil || loaded.BlockedWeekly == nil || loaded.BlockedDates == nil {
		t.Fatal("legacy settings did not load empty block arrays")
	}
	cookie, csrf := bootstrap(t, a)
	w := request(a, true, "PUT", "/api/admin/settings", legacy, cookie, csrf, a.cfg.AdminOrigin)
	if w.Code != 200 {
		t.Fatal("legacy settings could not be saved")
	}
	loaded.BlockedWeekly = []BlockedWeeklyPeriod{{Weekday: 1, AllDay: true, Start: "ignored", End: "ignored"}}
	loaded.BlockedDates = []BlockedDatePeriod{{Date: "2026-09-15", AllDay: true, Start: "ignored", End: "ignored"}}
	w = request(a, true, "PUT", "/api/admin/settings", loaded, cookie, csrf, a.cfg.AdminOrigin)
	if w.Code != 200 {
		t.Fatal("whole-day block rejected ignored time fields")
	}
	saved, _ := a.store.settings()
	if saved.BlockedWeekly[0].Start != "" || saved.BlockedDates[0].End != "" {
		t.Fatal("whole-day times were not canonicalized")
	}
	w = request(a, true, "PUT", "/api/admin/settings", legacy, cookie, csrf, a.cfg.AdminOrigin)
	saved, _ = a.store.settings()
	if w.Code != 200 || len(saved.BlockedWeekly) != 1 || len(saved.BlockedDates) != 1 {
		t.Fatal("legacy update silently deleted existing blocks")
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	saved, err = other.store.settings()
	if err != nil || len(saved.BlockedWeekly) != 1 || len(saved.BlockedDates) != 1 {
		t.Fatal("blocked settings did not survive restart")
	}
	w = request(a, true, "PUT", "/api/admin/settings", original, cookie, csrf, a.cfg.AdminOrigin)
	saved, _ = a.store.settings()
	if w.Code != 200 || len(saved.BlockedWeekly) != 0 || len(saved.BlockedDates) != 0 {
		t.Fatal("explicit empty arrays did not remove blocks")
	}
	for _, invalid := range []Settings{
		func() Settings {
			v := defaultSettings()
			v.BlockedWeekly = []BlockedWeeklyPeriod{{Weekday: 7, AllDay: true}}
			return v
		}(),
		func() Settings {
			v := defaultSettings()
			v.BlockedWeekly = []BlockedWeeklyPeriod{{Weekday: 1, Start: "23:00", End: "02:00"}}
			return v
		}(),
		func() Settings {
			v := defaultSettings()
			v.BlockedDates = []BlockedDatePeriod{{Date: "2026-02-30", AllDay: true}}
			return v
		}(),
		func() Settings {
			v := defaultSettings()
			v.BlockedDates = []BlockedDatePeriod{{Date: "2026-09-15", Start: "9:00", End: "10:00"}}
			return v
		}(),
		func() Settings { v := defaultSettings(); v.BlockedWeekly = make([]BlockedWeeklyPeriod, 71); return v }(),
		func() Settings { v := defaultSettings(); v.BlockedDates = make([]BlockedDatePeriod, 366); return v }(),
	} {
		if validateSettings(invalid) == nil {
			t.Fatal("invalid blocked settings were accepted")
		}
	}
}

func TestBlockedSlotsCannotBeBookedAndGoogleStillWins(t *testing.T) {
	a, f := testApp(t)
	v := defaultSettings()
	v.SlotMinutes = 30
	v.BlockedWeekly = []BlockedWeeklyPeriod{{Weekday: 1, Start: "09:00", End: "09:45"}}
	if _, err := a.saveSettings(v); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.createBooking(context.Background(), testInput("explicit-block-rejection-001")); !errors.Is(err, errConflict) {
		t.Fatal("public booking bypassed an explicit block")
	}
	f.busy = []Busy{{instant("2026-09-14T13:45:00Z"), instant("2026-09-14T14:15:00Z")}}
	slots, _, err := a.availableSlots(context.Background(), "2026-09-14")
	if err != nil || len(slots) == 0 || !slots[0].Start.Equal(instant("2026-09-14T14:30:00Z")) {
		t.Fatal("explicit blocks or Google conflict were ignored")
	}
}
