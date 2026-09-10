package main

import (
	"testing"
	"time"
)

func instant(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		panic(err)
	}
	return t
}
func TestAvailabilityDSTAndExceptions(t *testing.T) {
	v := defaultSettings()
	v.MinNoticeHours = 0
	v.BufferMinutes = 0
	v.HorizonDays = 90
	v.Weekly = []WeeklyPeriod{{0, "00:00", "04:00"}}
	for _, tc := range []struct {
		date, now string
		count     int
	}{{"2026-03-08", "2026-03-01T00:00:00Z", 5}, {"2026-11-01", "2026-10-01T00:00:00Z", 5}} {
		slots, err := scheduledSlots(v, tc.date, instant(tc.now))
		if err != nil {
			t.Fatal(err)
		}
		if len(slots) != tc.count {
			t.Fatalf("DST %s: got %d slots, want %d", tc.date, len(slots), tc.count)
		}
		for i, s := range slots {
			if s.End.Sub(s.Start) != 30*time.Minute {
				t.Fatal("DST changed appointment duration")
			}
			if i > 0 && slots[i-1].End.After(s.Start) {
				t.Fatal("DST produced overlapping appointments")
			}
			hour := s.Start.In(mustLocation(t, v.TimeZone)).Hour()
			if tc.date == "2026-03-08" && hour == 2 || tc.date == "2026-11-01" && hour == 1 {
				t.Fatal("ambiguous or missing hour was offered")
			}
		}
	}
	v.Exceptions = []DateException{{Date: "2026-03-08", Closed: true}}
	slots, err := scheduledSlots(v, "2026-03-08", instant("2026-03-01T00:00:00Z"))
	if err != nil || len(slots) != 0 {
		t.Fatal("closure did not override weekly hours")
	}
	v.Exceptions = []DateException{{Date: "2026-03-09", Start: "10:00", End: "11:00"}}
	slots, err = scheduledSlots(v, "2026-03-09", instant("2026-03-01T00:00:00Z"))
	if err != nil || len(slots) != 2 || slots[0].Start.Hour() != 14 {
		t.Fatal("date exception did not use local daylight time")
	}
}
func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, e := time.LoadLocation(name)
	if e != nil {
		t.Fatal(e)
	}
	return loc
}
func TestAvailabilityNoticeHorizonAndValidation(t *testing.T) {
	v := defaultSettings()
	now := instant("2026-09-10T14:00:00Z")
	for _, date := range []string{"2026-09-09", "2026-10-11"} {
		slots, err := scheduledSlots(v, date, now)
		if err != nil || len(slots) != 0 {
			t.Fatal("date outside horizon was offered")
		}
	}
	slots, err := scheduledSlots(v, "2026-09-11", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range slots {
		if s.Start.Before(now.Add(24 * time.Hour)) {
			t.Fatal("notice was not respected")
		}
	}
	v.Weekly = append(v.Weekly, WeeklyPeriod{1, "10:00", "12:00"})
	if validateSettings(v) == nil {
		t.Fatal("overlapping weekly ranges accepted")
	}
	v = defaultSettings()
	v.Exceptions = []DateException{{Date: "2026-09-11", Closed: true}, {Date: "2026-09-11", Closed: true}}
	if validateSettings(v) == nil {
		t.Fatal("duplicate exception date accepted")
	}
	v = defaultSettings()
	v.TimeZone = ""
	if validateSettings(v) == nil {
		t.Fatal("empty timezone accepted")
	}
	v = defaultSettings()
	v.SlotMinutes = 0
	if validateSettings(v) == nil {
		t.Fatal("zero duration accepted")
	}
}

func TestBuffersAtExactBoundaries(t *testing.T) {
	slot := Slot{instant("2026-09-14T13:00:00Z"), instant("2026-09-14T13:30:00Z")}
	// Busy records supplied here already contain their own following buffer.
	for _, tc := range []struct {
		name, start, end string
		want             bool
	}{
		{"previous Google event buffer", "2026-09-14T12:30:00Z", "2026-09-14T13:15:00Z", true},
		{"previous buffered event exactly finished", "2026-09-14T12:15:00Z", "2026-09-14T13:00:00Z", false},
		{"next event inside candidate buffer", "2026-09-14T13:35:00Z", "2026-09-14T14:15:00Z", true},
		{"next local slot exactly at buffer end", "2026-09-14T13:45:00Z", "2026-09-14T14:30:00Z", false},
	} {
		if got := conflicts(slot, 15, []Busy{{instant(tc.start), instant(tc.end)}}); got != tc.want {
			t.Errorf("%s: conflict=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestDefaultNormalWeekdaysOfferSlots(t *testing.T) {
	v := defaultSettings()
	v.Exceptions = []DateException{{Date: "2026-09-16", Closed: true}}
	now := instant("2026-09-10T20:16:00Z")
	for _, date := range []string{"2026-09-14", "2026-09-15", "2026-09-17", "2026-09-21", "2026-09-25"} {
		slots, err := scheduledSlots(v, date, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(slots) != 11 {
			t.Fatalf("normal weekday %s: got %d slots, want 11", date, len(slots))
		}
		if !slots[0].Start.Equal(instant(date+"T13:00:00Z")) || !slots[len(slots)-1].End.Equal(instant(date+"T21:00:00Z")) {
			t.Fatalf("normal weekday %s: slots do not span 09:00–17:00 New York", date)
		}
	}
	closed, err := scheduledSlots(v, "2026-09-16", now)
	if err != nil || len(closed) != 0 {
		t.Fatal("closure did not exclude the one closed date")
	}
}
