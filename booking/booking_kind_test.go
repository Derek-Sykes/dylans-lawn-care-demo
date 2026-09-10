package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestBookingKindsHaveSeparateLengthsAndSharedOccupancy(t *testing.T) {
	a, _ := testApp(t)
	service, _, err := a.availableSlots(context.Background(), "2026-09-14")
	if err != nil || len(service) == 0 || service[0].End.Sub(service[0].Start) != time.Hour {
		t.Fatal("service duration is not one hour")
	}
	estimate, _, err := a.availableSlots(context.Background(), "2026-09-14", "estimate")
	if err != nil || len(estimate) == 0 || estimate[0].End.Sub(estimate[0].Start) != 15*time.Minute {
		t.Fatal("estimate duration is not fifteen minutes")
	}
	input := testInput("estimate-duration-job-001")
	input.Kind = "estimate"
	booking, _, err := a.createBooking(context.Background(), input)
	if err != nil || booking.Kind != "estimate" || booking.End.Sub(booking.Start) != 15*time.Minute {
		t.Fatal("estimate reservation has incorrect kind or duration")
	}
	if _, _, err = a.createBooking(context.Background(), testInput("service-overlap-job-001")); !errors.Is(err, errConflict) {
		t.Fatal("a service appointment overlapped an estimate")
	}
	input.Kind = "service"
	if _, _, err = a.createBooking(context.Background(), input); err == nil {
		t.Fatal("same key was reused for a different request kind")
	}
	status, notes := "contacted", "Property details checked"
	updated, err := a.patchBooking(booking.ID, &status, &notes)
	if err != nil || updated.Kind != "estimate" || !updated.End.Equal(booking.End) {
		t.Fatal("contact progress changed appointment kind or duration")
	}
	cookie, csrf := bootstrap(t, a)
	settings, _ := a.store.settings()
	settings.SlotMinutes = 90
	settings.EstimateMinutes = 45
	if w := request(a, true, "PUT", "/api/admin/settings", settings, cookie, csrf, a.cfg.AdminOrigin); w.Code != 200 {
		t.Fatal("separate durations could not be saved")
	}
	input.Kind = "estimate"
	retry, created, err := a.createBooking(context.Background(), input)
	if err != nil || created || !retry.End.Equal(booking.End) {
		t.Fatal("duration changes resized an existing estimate on retry")
	}
	legacySettings, _ := json.Marshal(settings)
	var legacy map[string]any
	_ = json.Unmarshal(legacySettings, &legacy)
	delete(legacy, "estimateMinutes")
	if w := request(a, true, "PUT", "/api/admin/settings", legacy, cookie, csrf, a.cfg.AdminOrigin); w.Code != 200 {
		t.Fatal("legacy settings update failed")
	}
	saved, _ := a.store.settings()
	if saved.EstimateMinutes != 45 {
		t.Fatal("legacy client overwrote configured estimate duration")
	}
	for _, kind := range []string{"service", "estimate"} {
		w := request(a, true, "GET", "/api/admin/bookings?kind="+kind, nil, cookie, "", "")
		var response struct {
			Bookings []Booking `json:"bookings"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		if w.Code != 200 || (kind == "service" && len(response.Bookings) != 0) || (kind == "estimate" && len(response.Bookings) != 1) {
			t.Fatal("admin views mixed service and estimate requests")
		}
	}
	if w := request(a, false, "GET", "/api/public/slots?date=2026-09-14&kind=unknown", nil, nil, "", ""); w.Code != 400 {
		t.Fatal("unknown appointment kind accepted")
	}
}

func TestLegacyBookingKindMigrationPreservesIdempotency(t *testing.T) {
	a, _ := testApp(t)
	input := testInput("legacy-service-retry-001")
	booking, _, err := a.createBooking(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	// Exact old BookingInput JSON, including its field order and empty key.
	const oldPayload = `{"serviceId":"lawn-care","start":"2026-09-14T13:00:00Z","name":"Sample Customer","email":"sample@example.com","phone":"410-555-0123","address":"Example property","notes":"Mow the front and back lawn","idempotencyKey":""}`
	digest := sha256.Sum256([]byte(oldPayload))
	oldHash := hex.EncodeToString(digest[:])
	if _, err = a.store.db.Exec("UPDATE bookings SET payload_hash=? WHERE id=?", oldHash, booking.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.store.putJSON("settings", map[string]any{"businessName": "Saved owner", "timeZone": "America/New_York", "slotMinutes": 30}); err != nil {
		t.Fatal(err)
	}
	a.store.close()
	legacy, err := sql.Open("sqlite3", filepath.Join(a.cfg.DataDir, "booking.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec("ALTER TABLE bookings DROP COLUMN kind")
	legacy.Close()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(a.cfg.DataDir, a.cfg.BootstrapToken)
	if err != nil {
		t.Fatal(err)
	}
	a.store = reopened
	saved, err := reopened.settings()
	if err != nil || saved.SlotMinutes != 30 || saved.EstimateMinutes != 15 {
		t.Fatal("migration replaced legacy owner settings")
	}
	for _, kind := range []string{"", "service"} {
		input.Kind = kind
		result, created, err := a.createBooking(context.Background(), input)
		if err != nil || created || result.ID != booking.ID || result.Kind != "service" || result.PayloadHash != oldHash || !result.End.Equal(booking.End) {
			t.Fatalf("legacy idempotency retry failed for kind %q: %v", kind, err)
		}
	}
	input.Kind = "estimate"
	if _, _, err = a.createBooking(context.Background(), input); err == nil {
		t.Fatal("legacy service key was reused as estimate")
	}
}

func TestConcurrentServiceAndEstimateCannotDoubleBook(t *testing.T) {
	a, _ := testApp(t)
	var group sync.WaitGroup
	errorsOut := make(chan error, 2)
	for _, kind := range []string{"service", "estimate"} {
		group.Add(1)
		go func(kind string) {
			defer group.Done()
			input := testInput("concurrent-" + kind + "-booking")
			input.Kind = kind
			_, _, err := a.createBooking(context.Background(), input)
			errorsOut <- err
		}(kind)
	}
	group.Wait()
	close(errorsOut)
	success, conflicts := 0, 0
	for err := range errorsOut {
		if err == nil {
			success++
		} else if errors.Is(err, errConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal("different request kinds bypassed the shared appointment reservation")
	}
}
