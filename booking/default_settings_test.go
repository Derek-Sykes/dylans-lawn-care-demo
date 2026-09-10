package main

import (
	"context"
	"testing"
	"time"
)

func TestFreshAppointmentLengthAndSavedOwnerChoice(t *testing.T) {
	a, _ := testApp(t)
	settings, err := a.store.settings()
	if err != nil || settings.SlotMinutes != 60 {
		t.Fatalf("fresh installation duration = %d, error = %v; want 60 minutes", settings.SlotMinutes, err)
	}
	slots, _, err := a.availableSlots(context.Background(), "2026-09-14")
	if err != nil || len(slots) == 0 || slots[0].End.Sub(slots[0].Start) != time.Hour {
		t.Fatal("fresh installation did not offer hour-long service appointments")
	}
	for _, minutes := range []int{30, 90} {
		settings.SlotMinutes = minutes
		cookie, csrf := bootstrap(t, a)
		response := request(a, true, "PUT", "/api/admin/settings", settings, cookie, csrf, a.cfg.AdminOrigin)
		if response.Code != 200 {
			t.Fatalf("owner could not change appointment duration to %d minutes: %d", minutes, response.Code)
		}
		reopened, err := openStore(a.cfg.DataDir, a.cfg.BootstrapToken)
		if err != nil {
			t.Fatal(err)
		}
		saved, loadErr := reopened.settings()
		reopened.close()
		if loadErr != nil || saved.SlotMinutes != minutes {
			t.Fatalf("reopening replaced the saved %d-minute owner setting: %d, %v", minutes, saved.SlotMinutes, loadErr)
		}
		slots, _, err = a.availableSlots(context.Background(), "2026-09-14")
		if err != nil || len(slots) == 0 || slots[0].End.Sub(slots[0].Start) != time.Duration(minutes)*time.Minute {
			t.Fatalf("availability ignored the owner's %d-minute appointment length", minutes)
		}
	}
}
