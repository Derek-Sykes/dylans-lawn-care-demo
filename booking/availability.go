package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type WeeklyPeriod struct {
	Weekday int    `json:"weekday"`
	Start   string `json:"start"`
	End     string `json:"end"`
}
type DateException struct {
	Date   string `json:"date"`
	Closed bool   `json:"closed"`
	Start  string `json:"start,omitempty"`
	End    string `json:"end,omitempty"`
}
type BlockedWeeklyPeriod struct {
	Weekday int    `json:"weekday"`
	AllDay  bool   `json:"allDay"`
	Start   string `json:"start,omitempty"`
	End     string `json:"end,omitempty"`
}
type BlockedDatePeriod struct {
	Date   string `json:"date"`
	AllDay bool   `json:"allDay"`
	Start  string `json:"start,omitempty"`
	End    string `json:"end,omitempty"`
}
type Settings struct {
	BusinessName   string                `json:"businessName"`
	TimeZone       string                `json:"timeZone"`
	SlotMinutes    int                   `json:"slotMinutes"`
	BufferMinutes  int                   `json:"bufferMinutes"`
	MinNoticeHours int                   `json:"minNoticeHours"`
	HorizonDays    int                   `json:"horizonDays"`
	Weekly         []WeeklyPeriod        `json:"weekly"`
	Exceptions     []DateException       `json:"exceptions"`
	BlockedWeekly  []BlockedWeeklyPeriod `json:"blockedWeekly"`
	BlockedDates   []BlockedDatePeriod   `json:"blockedDates"`
}
type Slot struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
type Busy struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func defaultSettings() Settings {
	v := Settings{BusinessName: "Dylan’s Lawn Care", TimeZone: "America/New_York", SlotMinutes: 30, BufferMinutes: 15, MinNoticeHours: 24, HorizonDays: 30, Weekly: []WeeklyPeriod{}, Exceptions: []DateException{}, BlockedWeekly: []BlockedWeeklyPeriod{}, BlockedDates: []BlockedDatePeriod{}}
	for d := 1; d <= 5; d++ {
		v.Weekly = append(v.Weekly, WeeklyPeriod{d, "09:00", "17:00"})
	}
	return v
}
func clockMinute(v string) (int, error) {
	if len(v) != 5 || v[2] != ':' {
		return 0, errors.New("use HH:mm for working hours")
	}
	t, e := time.Parse("15:04", v)
	if e != nil {
		return 0, errors.New("invalid working hours")
	}
	return t.Hour()*60 + t.Minute(), nil
}
func validateSettings(v Settings) error {
	if n := len([]rune(strings.TrimSpace(v.BusinessName))); n < 1 || n > 100 {
		return errors.New("business name must contain 1–100 characters")
	}
	if len(v.TimeZone) < 1 || len(v.TimeZone) > 100 || v.TimeZone == "Local" {
		return errors.New("choose an IANA time zone")
	}
	if _, e := time.LoadLocation(v.TimeZone); e != nil {
		return errors.New("choose a valid IANA time zone")
	}
	if v.SlotMinutes < 15 || v.SlotMinutes > 240 || v.BufferMinutes < 0 || v.BufferMinutes > 120 || v.MinNoticeHours < 0 || v.MinNoticeHours > 720 || v.HorizonDays < 1 || v.HorizonDays > 90 {
		return errors.New("appointment, buffer, notice or booking horizon is outside its allowed range")
	}
	if len(v.Weekly) > 35 || len(v.Exceptions) > 365 || len(v.BlockedWeekly) > 70 || len(v.BlockedDates) > 365 {
		return errors.New("too many availability periods")
	}
	byDay := map[int][][2]int{}
	for _, p := range v.Weekly {
		a, e := clockMinute(p.Start)
		if e != nil {
			return e
		}
		b, e := clockMinute(p.End)
		if e != nil {
			return e
		}
		if p.Weekday < 0 || p.Weekday > 6 || a >= b {
			return errors.New("working hours must finish later on the same day")
		}
		byDay[p.Weekday] = append(byDay[p.Weekday], [2]int{a, b})
	}
	for _, ranges := range byDay {
		sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
		for i := 1; i < len(ranges); i++ {
			if ranges[i][0] < ranges[i-1][1] {
				return errors.New("weekly working hours overlap")
			}
		}
	}
	dates := map[string]bool{}
	for _, p := range v.Exceptions {
		if _, e := time.Parse("2006-01-02", p.Date); e != nil || len(p.Date) != 10 {
			return errors.New("use YYYY-MM-DD for exception dates")
		}
		if dates[p.Date] {
			return errors.New("each exception date can appear only once")
		}
		dates[p.Date] = true
		if !p.Closed {
			a, e := clockMinute(p.Start)
			if e != nil {
				return e
			}
			b, e := clockMinute(p.End)
			if e != nil {
				return e
			}
			if a >= b {
				return errors.New("exception hours must finish later on the same day")
			}
		}
	}
	for _, p := range v.BlockedWeekly {
		if p.Weekday < 0 || p.Weekday > 6 {
			return errors.New("choose a valid weekday for blocked time")
		}
		if _, err := blockedMinutes(p.AllDay, p.Start, p.End); err != nil {
			return err
		}
	}
	for _, p := range v.BlockedDates {
		if _, err := time.Parse("2006-01-02", p.Date); err != nil || len(p.Date) != 10 {
			return errors.New("use YYYY-MM-DD for blocked dates")
		}
		if _, err := blockedMinutes(p.AllDay, p.Start, p.End); err != nil {
			return err
		}
	}
	return nil
}

func canonicalSettings(v Settings) Settings {
	if v.BlockedWeekly == nil {
		v.BlockedWeekly = []BlockedWeeklyPeriod{}
	}
	if v.BlockedDates == nil {
		v.BlockedDates = []BlockedDatePeriod{}
	}
	for i := range v.BlockedWeekly {
		if v.BlockedWeekly[i].AllDay {
			v.BlockedWeekly[i].Start = ""
			v.BlockedWeekly[i].End = ""
		}
	}
	for i := range v.BlockedDates {
		if v.BlockedDates[i].AllDay {
			v.BlockedDates[i].Start = ""
			v.BlockedDates[i].End = ""
		}
	}
	return v
}

func blockedMinutes(allDay bool, start, end string) ([2]int, error) {
	if allDay {
		return [2]int{0, 24 * 60}, nil
	}
	a, errA := clockMinute(start)
	b, errB := clockMinute(end)
	if errA != nil || errB != nil || a >= b {
		return [2]int{}, errors.New("blocked hours must use HH:mm and finish later on the same day")
	}
	return [2]int{a, b}, nil
}

type blockedSchedule struct {
	weekly map[int][][2]int
	dates  map[string][][2]int
}

func compileBlocks(v Settings) blockedSchedule {
	b := blockedSchedule{weekly: map[int][][2]int{}, dates: map[string][][2]int{}}
	for _, p := range v.BlockedWeekly {
		span, _ := blockedMinutes(p.AllDay, p.Start, p.End)
		b.weekly[p.Weekday] = append(b.weekly[p.Weekday], span)
	}
	for _, p := range v.BlockedDates {
		span, _ := blockedMinutes(p.AllDay, p.Start, p.End)
		b.dates[p.Date] = append(b.dates[p.Date], span)
	}
	return b
}
func (b blockedSchedule) overlaps(slot Slot, buffer int, loc *time.Location) bool {
	if len(b.weekly) == 0 && len(b.dates) == 0 {
		return false
	}
	// Rules use minute precision. Walking real minutes handles both occurrences
	// of a repeated DST hour, missing wall times, and buffers crossing midnight.
	end := slot.End.Add(time.Duration(buffer) * time.Minute)
	for at := slot.Start; at.Before(end); at = at.Add(time.Minute) {
		local := at.In(loc)
		minute := local.Hour()*60 + local.Minute()
		for _, span := range b.weekly[int(local.Weekday())] {
			if minute >= span[0] && minute < span[1] {
				return true
			}
		}
		for _, span := range b.dates[local.Format("2006-01-02")] {
			if minute >= span[0] && minute < span[1] {
				return true
			}
		}
	}
	return false
}

// A wall time is usable only if exactly one instant maps to it. Skipping the
// repeated/missing hour avoids silently guessing during daylight-saving changes.
func wallInstant(date string, minute int, loc *time.Location) (time.Time, bool) {
	base, e := time.Parse("2006-01-02", date)
	if e != nil {
		return time.Time{}, false
	}
	want := fmt.Sprintf("%s %02d:%02d", date, minute/60, minute%60)
	guess := time.Date(base.Year(), base.Month(), base.Day(), minute/60, minute%60, 0, 0, loc)
	matches := []time.Time{}
	for offset := -180; offset <= 180; offset++ {
		candidate := guess.Add(time.Duration(offset) * time.Minute)
		if candidate.In(loc).Format("2006-01-02 15:04") == want {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return time.Time{}, false
	}
	return matches[0].UTC(), true
}

func scheduledSlots(v Settings, date string, now time.Time) ([]Slot, error) {
	if err := validateSettings(v); err != nil {
		return nil, err
	}
	loc, _ := time.LoadLocation(v.TimeZone)
	day, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil || len(date) != 10 {
		return nil, errors.New("use YYYY-MM-DD for the appointment date")
	}
	out := []Slot{}
	today := now.In(loc).Format("2006-01-02")
	last := now.In(loc).AddDate(0, 0, v.HorizonDays).Format("2006-01-02")
	if date < today || date > last {
		return out, nil
	}
	periods := [][2]int{}
	for _, p := range v.Weekly {
		if p.Weekday == int(day.Weekday()) {
			a, _ := clockMinute(p.Start)
			b, _ := clockMinute(p.End)
			periods = append(periods, [2]int{a, b})
		}
	}
	for _, p := range v.Exceptions {
		if p.Date == date {
			periods = nil
			if !p.Closed {
				a, _ := clockMinute(p.Start)
				b, _ := clockMinute(p.End)
				periods = append(periods, [2]int{a, b})
			}
			break
		}
	}
	notBefore := now.Add(time.Duration(v.MinNoticeHours) * time.Hour)
	blocks := compileBlocks(v)
	for _, p := range periods {
		for minute := p[0]; minute+v.SlotMinutes <= p[1]; minute += v.SlotMinutes + v.BufferMinutes {
			start, ok := wallInstant(date, minute, loc)
			if !ok || start.Before(notBefore) {
				continue
			}
			end, ok := wallInstant(date, minute+v.SlotMinutes, loc)
			if !ok || end.Sub(start) != time.Duration(v.SlotMinutes)*time.Minute {
				continue
			}
			slot := Slot{start, end}
			if !blocks.overlaps(slot, v.BufferMinutes, loc) {
				out = append(out, slot)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

func conflicts(slot Slot, buffer int, busy []Busy) bool {
	end := slot.End.Add(time.Duration(buffer) * time.Minute)
	for _, b := range busy {
		if slot.Start.Before(b.End) && end.After(b.Start) {
			return true
		}
	}
	return false
}
