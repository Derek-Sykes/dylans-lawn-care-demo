package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"
)

const calendarRefreshInterval = time.Minute
const calendarRefreshMessage = "Google Calendar changes could not be checked. These are the last saved appointment times. Refresh again or check the Google connection."

// The cursor is installation-local and encrypted alongside the Google tokens.
// Read only the app-created calendar; primary-calendar details are never read.
type calendarCursor struct {
	CalendarID string `json:"calendarId"`
	Token      string `json:"token"`
	TimeZone   string `json:"timeZone"`
}

type calendarPage struct {
	Items         []googleEvent `json:"items"`
	NextPageToken string        `json:"nextPageToken"`
	NextSyncToken string        `json:"nextSyncToken"`
	TimeZone      string        `json:"timeZone"`
}

func (a *App) refreshCalendar(ctx context.Context, force bool) error {
	if !a.calendar.Connected() {
		return nil
	}
	if calendar, ok := a.calendar.(interface {
		Refresh(context.Context, bool) error
	}); ok {
		return calendar.Refresh(ctx, force)
	}
	return nil
}

func (g *Google) Refresh(ctx context.Context, force bool) error {
	// Share the operation lock with connection changes and outbound event writes.
	// A full snapshot must not mistake a concurrently inserted event for a deletion.
	g.ops.Lock()
	defer g.ops.Unlock()
	if !force && !g.lastRefresh.IsZero() && g.now().Sub(g.lastRefresh) < calendarRefreshInterval {
		return g.lastRefreshErr
	}
	g.lastRefresh = g.now()
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	g.lastRefreshErr = g.refresh(callCtx)
	if g.lastRefreshErr != nil {
		_ = g.store.putJSON("calendar_refresh_error", calendarRefreshMessage)
	} else {
		_ = g.store.putJSON("calendar_refresh_error", "")
	}
	return g.lastRefreshErr
}

func (g *Google) refresh(ctx context.Context) error {
	owner, err := g.owner()
	if err != nil || owner.CalendarID == "" {
		return errGoogle
	}
	var cursor calendarCursor
	if err = g.store.getSecret("calendar_cursor", &cursor); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if cursor.CalendarID != owner.CalendarID {
		cursor = calendarCursor{CalendarID: owner.CalendarID}
	}
	// Google expires sync tokens. Discard only the token in memory, and replace
	// the durable cursor only after the entire replacement snapshot is applied.
	for attempt := 0; attempt < 2; attempt++ {
		events, next, expired, err := g.readCalendarPages(ctx, cursor)
		if expired && cursor.Token != "" {
			cursor.Token = ""
			continue
		}
		if err != nil {
			return err
		}
		return g.store.applyCalendarSnapshot(ctx, events, next, cursor.Token == "")
	}
	return errGoogle
}

func (g *Google) readCalendarPages(ctx context.Context, cursor calendarCursor) ([]googleEvent, calendarCursor, bool, error) {
	query := url.Values{
		"maxResults": {"250"}, "showDeleted": {"true"}, "singleEvents": {"false"},
		"fields": {"items(id,status,start,end,extendedProperties,recurrence,recurringEventId),nextPageToken,nextSyncToken,timeZone"},
	}
	if cursor.Token != "" {
		query.Set("syncToken", cursor.Token)
	}
	events := []googleEvent{}
	seenPages := map[string]bool{}
	for page := 0; page < 100; page++ {
		var out calendarPage
		status, err := g.request(ctx, "GET", "/calendars/"+url.PathEscape(cursor.CalendarID)+"/events?"+query.Encode(), nil, &out)
		if err != nil {
			return nil, cursor, status == 410, err
		}
		if out.TimeZone != "" {
			if _, err = time.LoadLocation(out.TimeZone); err != nil {
				return nil, cursor, false, errGoogle
			}
			cursor.TimeZone = out.TimeZone
		}
		events = append(events, out.Items...)
		if out.NextPageToken == "" {
			if out.NextSyncToken == "" || len(out.NextSyncToken) > 8192 {
				return nil, cursor, false, errGoogle
			}
			cursor.Token = out.NextSyncToken
			return events, cursor, false, nil
		}
		if seenPages[out.NextPageToken] || len(out.NextPageToken) > 8192 {
			return nil, cursor, false, errGoogle
		}
		seenPages[out.NextPageToken] = true
		query.Set("pageToken", out.NextPageToken)
	}
	return nil, cursor, false, errGoogle
}

func eventTimes(event googleEvent, zone string) (time.Time, time.Time, error) {
	// One website request represents one job. Expanding a recurring series into
	// customer jobs would require separate records and is deliberately unsupported.
	if len(event.Recurrence) != 0 || event.RecurringEventID != "" {
		return time.Time{}, time.Time{}, errGoogle
	}
	var start, end time.Time
	var err error
	if event.Start.DateTime != "" && event.End.DateTime != "" && event.Start.Date == "" && event.End.Date == "" {
		start, err = time.Parse(time.RFC3339, event.Start.DateTime)
		if err == nil {
			end, err = time.Parse(time.RFC3339, event.End.DateTime)
		}
	} else if event.Start.Date != "" && event.End.Date != "" && event.Start.DateTime == "" && event.End.DateTime == "" {
		if zone == "" {
			return time.Time{}, time.Time{}, errGoogle
		}
		var loc *time.Location
		loc, err = time.LoadLocation(zone)
		if err == nil {
			start, err = time.ParseInLocation("2006-01-02", event.Start.Date, loc)
		}
		if err == nil {
			end, err = time.ParseInLocation("2006-01-02", event.End.Date, loc)
		}
	} else {
		err = errGoogle
	}
	if err != nil || start.IsZero() || !end.After(start) {
		return time.Time{}, time.Time{}, errGoogle
	}
	return start.UTC(), end.UTC(), nil
}

func (s *Store) applyCalendarSnapshot(ctx context.Context, events []googleEvent, cursor calendarCursor, full bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT "+bookingColumns+" FROM bookings")
	if err != nil {
		return err
	}
	known := map[string]Booking{}
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			rows.Close()
			return err
		}
		known[b.EventID] = b
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	seen := map[string]bool{}
	for _, event := range events {
		b, ok := known[event.ID]
		if !ok {
			continue
		}
		seen[event.ID] = true
		if err = s.applyCalendarEvent(ctx, tx, b, event, cursor.TimeZone); err != nil {
			return err
		}
	}
	if full {
		// Synced events absent from a complete snapshot were deleted or moved out
		// of this calendar. Pending insertions have no proven remote event to remove.
		for id, b := range known {
			if !seen[id] && b.CalendarStatus == "synced" && b.Status != "cancelled" {
				if err = s.applyCalendarEvent(ctx, tx, b, googleEvent{ID: id, Status: "cancelled"}, cursor.TimeZone); err != nil {
					return err
				}
			}
		}
	}
	if cursor.Token != "" {
		data, err := json.Marshal(cursor)
		if err != nil {
			return err
		}
		sealed, err := s.seal(data, "calendar_cursor")
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO secrets(key,value) VALUES('calendar_cursor',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", sealed); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) applyCalendarEvent(ctx context.Context, tx *sql.Tx, b Booking, event googleEvent, zone string) error {
	if event.Status == "cancelled" {
		if b.Status == "cancelled" && b.CalendarStatus == "synced" {
			return nil
		}
		_, err := tx.ExecContext(ctx, "UPDATE bookings SET status='cancelled',calendar_status='synced',generation=generation+1,sync_generation=generation+1,attempts=0,next_attempt=0 WHERE id=?", b.ID)
		return err
	}
	if event.Extended.Private["booking_id"] != b.ID || event.Extended.Private["installation_id"] != s.installID {
		return errGoogle
	}
	start, end, err := eventTimes(event, zone)
	if err != nil {
		return err
	}
	if b.Status == "cancelled" {
		// An owner cancellation in the portal wins over a still-present Google
		// event. Retain its true occupied time until the delete worker succeeds.
		if !start.Equal(b.Start) || !end.Equal(b.End) || b.CalendarStatus == "synced" {
			_, err = tx.ExecContext(ctx, "UPDATE bookings SET start=?,end=?,blocked_end=?,calendar_status='pending',generation=generation+1,attempts=0,next_attempt=0 WHERE id=?", start.Unix(), end.Unix(), end.Add(b.BlockedEnd.Sub(b.End)).Unix(), b.ID)
		}
		return err
	}
	if start.Equal(b.Start) && end.Equal(b.End) && b.CalendarStatus == "synced" {
		return nil
	}
	// Google is authoritative for the actual time after an event exists. Keep the
	// original booking buffer and follow-up status; settings affect new jobs only.
	_, err = tx.ExecContext(ctx, "UPDATE bookings SET start=?,end=?,blocked_end=?,calendar_status='synced',generation=generation+1,sync_generation=generation+1,attempts=0,next_attempt=0 WHERE id=?", start.Unix(), end.Unix(), end.Add(b.BlockedEnd.Sub(b.End)).Unix(), b.ID)
	return err
}
