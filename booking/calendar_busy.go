package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"
)

func (g *Google) Busy(ctx context.Context, start, end time.Time) ([]Busy, error) {
	g.ops.RLock()
	defer g.ops.RUnlock()
	busy, err := g.busy(ctx, start, end)
	if err != nil {
		if errors.Is(err, errClientSecretRequired) {
			_ = g.store.putJSON("google_error", clientConfigurationError)
		} else {
			_ = g.store.putJSON("google_error", "Calendar availability could not be checked. Reconnect Google and check that the booking calendar is still available.")
		}
	} else {
		_ = g.store.putJSON("google_error", "")
	}
	return busy, err
}
func (g *Google) busy(ctx context.Context, start, end time.Time) ([]Busy, error) {
	_, calendars, err := g.busyCalendars(ctx, start, end)
	if err != nil {
		return nil, err
	}
	all := []Busy{}
	for _, busy := range calendars {
		all = append(all, busy...)
	}
	return all, nil
}

func (g *Google) busyCalendars(ctx context.Context, start, end time.Time) (Owner, map[string][]Busy, error) {
	owner, err := g.owner()
	if err != nil || owner.CalendarID == "" {
		return owner, nil, errGoogle
	}
	request := map[string]any{"timeMin": start.UTC().Format(time.RFC3339), "timeMax": end.UTC().Format(time.RFC3339), "items": []map[string]string{{"id": "primary"}, {"id": owner.CalendarID}}}
	var out struct {
		Calendars map[string]struct {
			Busy   []Busy            `json:"busy"`
			Errors []json.RawMessage `json:"errors"`
		} `json:"calendars"`
	}
	_, err = g.request(ctx, "POST", "/freeBusy", request, &out)
	if err != nil {
		return owner, nil, err
	}
	all := map[string][]Busy{}
	for _, id := range []string{"primary", owner.CalendarID} {
		c, ok := out.Calendars[id]
		if !ok || len(c.Errors) > 0 {
			return owner, nil, errGoogle
		}
		for _, b := range c.Busy {
			if b.Start.IsZero() || !b.End.After(b.Start) {
				return owner, nil, errGoogle
			}
			all[id] = append(all[id], b)
		}
	}
	return owner, all, nil
}

// Keep provenance instead of subtracting booked intervals from merged free/busy
// results: an unrelated event can overlap the very same time as a known job.
func (g *Google) ExternalBusy(ctx context.Context, start, end time.Time) ([]Busy, error) {
	g.ops.RLock()
	defer g.ops.RUnlock()
	busy, err := g.externalBusy(ctx, start, end)
	if err != nil {
		_ = g.store.putJSON("google_error", "Calendar availability could not be checked. Reconnect Google and check that the booking calendar is still available.")
	} else {
		_ = g.store.putJSON("google_error", "")
	}
	return busy, err
}

func (g *Google) externalBusy(ctx context.Context, start, end time.Time) ([]Busy, error) {
	owner, calendars, err := g.busyCalendars(ctx, start, end)
	if err != nil {
		return nil, err
	}
	external := append([]Busy{}, calendars["primary"]...)
	query := url.Values{"timeMin": {start.UTC().Format(time.RFC3339)}, "timeMax": {end.UTC().Format(time.RFC3339)}, "singleEvents": {"true"}, "showDeleted": {"false"}, "maxResults": {"250"}, "fields": {"items(id,status,start,end,extendedProperties,transparency,recurrence,recurringEventId),nextPageToken,timeZone"}}
	seenPages := map[string]bool{}
	zone := ""
	for page := 0; page < 100; page++ {
		var out calendarPage
		if _, err = g.request(ctx, "GET", "/calendars/"+url.PathEscape(owner.CalendarID)+"/events?"+query.Encode(), nil, &out); err != nil {
			return nil, err
		}
		if out.TimeZone != "" {
			zone = out.TimeZone
		}
		for _, event := range out.Items {
			if event.Status == "cancelled" || event.Transparency == "transparent" {
				continue
			}
			// Expanded recurring instances are external events, not additional
			// website jobs. Their explicit time can still protect availability.
			copy := event
			copy.Recurrence = nil
			copy.RecurringEventID = ""
			begin, finish, err := eventTimes(copy, zone)
			if err != nil {
				return nil, err
			}
			b, err := scanBooking(g.store.db.QueryRow("SELECT "+bookingColumns+" FROM bookings WHERE event_id=?", event.ID))
			known := err == nil && (b.Status != "cancelled" || b.CalendarStatus != "synced") && event.Extended.Private["booking_id"] == b.ID && event.Extended.Private["installation_id"] == g.store.installID && begin.Equal(b.Start) && finish.Equal(b.End) && len(event.Recurrence) == 0 && event.RecurringEventID == ""
			if !known {
				external = append(external, Busy{begin, finish})
			}
		}
		if out.NextPageToken == "" {
			return external, nil
		}
		if seenPages[out.NextPageToken] || len(out.NextPageToken) > 8192 {
			return nil, errGoogle
		}
		seenPages[out.NextPageToken] = true
		query.Set("pageToken", out.NextPageToken)
	}
	return nil, errGoogle
}
