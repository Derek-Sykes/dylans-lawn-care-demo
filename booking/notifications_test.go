package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeMailSender struct {
	mu        sync.Mutex
	connected bool
	failure   error
	messages  []EmailMessage
}

func (f *fakeMailSender) Connected() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.connected }
func (f *fakeMailSender) Send(_ context.Context, message EmailMessage) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, message)
	if f.failure != nil {
		return "", f.failure
	}
	return "gmail-accepted-" + message.ID, nil
}
func (f *fakeMailSender) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.messages) }

func emailTestApp(t *testing.T) (*App, *fakeMailSender) {
	t.Helper()
	a, _ := testApp(t)
	f := &fakeMailSender{connected: true}
	a.mailer = f
	if err := a.store.putSecret("owner", Owner{Sub: "calendar-sub", Email: "sender@example.com", CalendarID: "fixture-email-calendar"}); err != nil {
		t.Fatal(err)
	}
	return a, f
}

func enableTestEmail(t *testing.T, a *App) {
	t.Helper()
	v := defaultEmailSettings()
	v.Enabled = true
	if err := a.store.saveEmailSettings(v, a.now()); err != nil {
		t.Fatal(err)
	}
}

func emailCount(t *testing.T, a *App, kind, status string) int {
	t.Helper()
	var count int
	if err := a.store.db.QueryRow("SELECT COUNT(*) FROM email_outbox WHERE (?='' OR kind=?) AND (?='' OR status=?)", kind, kind, status, status).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func lastEmail(t *testing.T, a *App, kind string) emailJob {
	t.Helper()
	v, err := scanEmail(a.store.db.QueryRow("SELECT "+emailColumns+" FROM email_outbox WHERE kind=? ORDER BY version DESC,created DESC LIMIT 1", kind))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func createEmailBooking(t *testing.T, a *App, key, kind string) Booking {
	t.Helper()
	in := testInput(key)
	in.Kind = kind
	b, _, err := a.createBooking(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func confirmEmailBooking(t *testing.T, a *App, b Booking) Booking {
	t.Helper()
	a.syncDue(context.Background())
	b, err := a.patchBooking(b.ID, ptr("confirmed"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEmailActivationDoesNotBackfillExistingAppointments(t *testing.T) {
	a, f := emailTestApp(t)
	b := createEmailBooking(t, a, "existing-before-email", "service")
	a.syncDue(context.Background())
	if _, err := a.patchBooking(b.ID, ptr("confirmed"), nil); err != nil {
		t.Fatal(err)
	}
	v, err := a.store.emailSettings()
	if err != nil || v.Enabled || !v.RemindersEnabled || v.ReminderHours != 24 {
		t.Fatal("email defaults are not safe and practical")
	}
	enableTestEmail(t, a)
	a.sendEmailDue(context.Background())
	if f.calls() != 0 || emailCount(t, a, "", "") != 0 {
		t.Fatal("enabling emails contacted an existing customer")
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	other.mailer = f
	other.sendEmailDue(context.Background())
	if emailCount(t, other, "", "") != 0 {
		t.Fatal("restart backfilled existing work")
	}
	// A first Calendar import after activation must not backfill a stale change.
	tx, err := a.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	after := b
	after.Start = after.Start.Add(time.Hour)
	after.End = after.End.Add(time.Hour)
	err = a.store.queueBookingEmailTx(tx, b, after, a.now(), false)
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil || emailCount(t, a, "", "") != 0 {
		t.Fatal("Calendar-only history bypassed forward-only activation")
	}
}

func TestRequestReceiptIsTruthfulAtomicAndIdempotent(t *testing.T) {
	for _, kind := range []string{"service", "estimate"} {
		t.Run(kind, func(t *testing.T) {
			a, f := emailTestApp(t)
			enableTestEmail(t, a)
			b := createEmailBooking(t, a, "receipt-idempotent-test", kind)
			in := testInput("receipt-idempotent-test")
			in.Kind = kind
			if _, created, err := a.createBooking(context.Background(), in); err != nil || created {
				t.Fatal("retry changed booking")
			}
			if emailCount(t, a, "receipt", "") != 1 {
				t.Fatal("idempotent customer submission duplicated receipt")
			}
			if _, err := a.patchBooking(b.ID, nil, ptr("INTERNAL_SECRET_NOTE")); err != nil {
				t.Fatal(err)
			}
			a.sendEmailDue(context.Background())
			if f.calls() != 1 || emailCount(t, a, "receipt", "sent") != 1 {
				t.Fatal("receipt did not send once")
			}
			message := f.messages[0]
			if message.To != b.Email || !strings.Contains(message.Text, "still needs confirmation") || strings.Contains(message.Text, "INTERNAL_SECRET_NOTE") || strings.Contains(message.Text, b.Notes) {
				t.Fatal("receipt confused confirmation or exposed private notes")
			}
			if !strings.Contains(message.Text, "EDT") || !strings.Contains(message.Text, b.Address) {
				t.Fatal("receipt omitted local appointment details")
			}
			if kind == "estimate" && !strings.Contains(message.Text, "not a scheduled service job") {
				t.Fatal("estimate was described as a service job")
			}
		})
	}
	// The booking and its queue event either both commit or both roll back.
	a, _ := emailTestApp(t)
	enableTestEmail(t, a)
	if _, err := a.store.db.Exec("CREATE TRIGGER reject_test_email BEFORE INSERT ON email_outbox BEGIN SELECT RAISE(ABORT,'email-write-failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.createBooking(context.Background(), testInput("email-atomic-failure")); err == nil {
		t.Fatal("booking committed without its required outbox event")
	}
	var count int
	_ = a.store.db.QueryRow("SELECT COUNT(*) FROM bookings").Scan(&count)
	if count != 0 {
		t.Fatal("failed outbox insert left a partial booking")
	}
}

func TestConfirmationAndOneReminderAreDurableAndDeduplicated(t *testing.T) {
	for _, kind := range []string{"service", "estimate"} {
		t.Run(kind, func(t *testing.T) {
			a, f := emailTestApp(t)
			enableTestEmail(t, a)
			b := createEmailBooking(t, a, "confirmed-reminder-test", kind)
			a.sendEmailDue(context.Background())
			b = confirmEmailBooking(t, a, b)
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := a.patchBooking(b.ID, ptr("confirmed"), ptr("private note")); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if emailCount(t, a, "confirmation", "") != 1 || emailCount(t, a, "reminder", "") != 1 {
				t.Fatal("repeated status/no-op updates duplicated emails")
			}
			a.sendEmailDue(context.Background())
			if f.calls() != 2 {
				t.Fatal("confirmation did not send independently of future reminder")
			}
			reminder := lastEmail(t, a, "reminder")
			if !reminder.ScheduledAt.Equal(b.Start.Add(-24 * time.Hour)) {
				t.Fatal("reminder is not 24 hours before the appointment")
			}
			other, err := newApp(a.cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer other.store.close()
			other.calendar = a.calendar
			other.mailer = f
			other.now = func() time.Time { return reminder.ScheduledAt }
			for range 8 {
				wg.Add(1)
				go func() { defer wg.Done(); other.sendEmailDue(context.Background()) }()
			}
			wg.Wait()
			if f.calls() != 3 || emailCount(t, other, "reminder", "sent") != 1 {
				t.Fatal("persistent reminder was lost or sent twice")
			}
			other.sendEmailDue(context.Background())
			if f.calls() != 3 {
				t.Fatal("already sent reminder repeated")
			}
		})
	}
}

func TestIndependentEmailWorkersClaimOneMessage(t *testing.T) {
	a, f := emailTestApp(t)
	enableTestEmail(t, a)
	createEmailBooking(t, a, "cross-process-mailer", "service")
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	other.now = a.now
	other.calendar = a.calendar
	other.mailer = f
	var wg sync.WaitGroup
	for _, app := range []*App{a, other} {
		wg.Add(1)
		go func(v *App) { defer wg.Done(); v.sendEmailDue(context.Background()) }(app)
	}
	wg.Wait()
	if f.calls() != 1 || emailCount(t, a, "receipt", "sent") != 1 {
		t.Fatal("racing processes both submitted the same email")
	}
}

func TestEmailsFollowCalendarRescheduleAndCancellation(t *testing.T) {
	a, f, b := syncedGoogleBooking(t)
	mail := &fakeMailSender{connected: true}
	a.mailer = mail
	enableTestEmail(t, a)
	if _, err := a.patchBooking(b.ID, ptr("confirmed"), nil); err != nil {
		t.Fatal(err)
	}
	a.sendEmailDue(context.Background())
	oldReminder := lastEmail(t, a, "reminder")
	newStart := b.Start.Add(2 * time.Hour)
	newEnd := b.End.Add(2 * time.Hour)
	changeGoogleTimes(f, b, newStart.Format(time.RFC3339), newEnd.Format(time.RFC3339))
	if err := a.refreshCalendar(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := a.refreshCalendar(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	stale, _ := a.store.emailJob(oldReminder.ID)
	if stale.Status != "skipped" || emailCount(t, a, "reschedule", "") != 1 || emailCount(t, a, "reminder", "queued") != 1 {
		t.Fatal("rescheduling did not replace outdated queued notifications")
	}
	a.sendEmailDue(context.Background())
	if !strings.Contains(mail.messages[len(mail.messages)-1].Text, "11:00 AM EDT") {
		t.Fatal("reschedule email has stale job time")
	}
	f.mu.Lock()
	f.events[b.EventID] = map[string]any{"id": b.EventID, "status": "cancelled"}
	f.mu.Unlock()
	if err := a.refreshCalendar(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if emailCount(t, a, "reminder", "queued") != 0 || emailCount(t, a, "cancellation", "queued") != 1 {
		t.Fatal("Calendar cancellation did not stop reminders and queue a cancellation")
	}
	a.sendEmailDue(context.Background())
	if emailCount(t, a, "cancellation", "sent") != 1 {
		t.Fatal("cancellation update did not send")
	}
}

type emailRefreshCalendar struct {
	*fakeCalendar
	refreshErr error
}

func (f *emailRefreshCalendar) Refresh(context.Context, bool) error { return f.refreshErr }

func TestReminderWaitsForCalendarAndNeverSendsAfterStart(t *testing.T) {
	a, f := emailTestApp(t)
	enableTestEmail(t, a)
	b := confirmEmailBooking(t, a, createEmailBooking(t, a, "reminder-wait-calendar", "service"))
	a.sendEmailDue(context.Background())
	f.calls() // receipt was superseded by the immediate explicit confirmation.
	reminder := lastEmail(t, a, "reminder")
	clock := reminder.ScheduledAt
	a.now = func() time.Time { return clock }
	calendar := &emailRefreshCalendar{fakeCalendar: a.calendar.(*fakeCalendar), refreshErr: errors.New("calendar unavailable")}
	a.calendar = calendar
	a.sendEmailDue(context.Background())
	if emailCount(t, a, "reminder", "queued") != 1 || f.calls() != 1 {
		t.Fatal("offline Calendar caused a stale reminder or lost it")
	}
	calendar.refreshErr = nil
	clock = b.Start
	a.sendEmailDue(context.Background())
	if emailCount(t, a, "reminder", "skipped") != 1 || f.calls() != 1 {
		t.Fatal("reminder was sent after the appointment started")
	}
}

func TestConfirmationTemporarilyWaitsForCalendarSync(t *testing.T) {
	a, f := emailTestApp(t)
	enableTestEmail(t, a)
	b := confirmEmailBooking(t, a, createEmailBooking(t, a, "mail-wait-synced-event", "service"))
	if _, err := a.store.db.Exec("UPDATE bookings SET calendar_status='failed' WHERE id=?", b.ID); err != nil {
		t.Fatal(err)
	}
	a.sendEmailDue(context.Background())
	if f.calls() != 0 || emailCount(t, a, "confirmation", "queued") != 1 {
		t.Fatal("temporary Calendar sync failure lost confirmation")
	}
	if _, err := a.store.db.Exec("UPDATE bookings SET calendar_status='synced' WHERE id=?", b.ID); err != nil {
		t.Fatal(err)
	}
	clock := a.now().Add(time.Minute)
	a.now = func() time.Time { return clock }
	a.sendEmailDue(context.Background())
	if f.calls() != 1 || emailCount(t, a, "confirmation", "sent") != 1 {
		t.Fatal("confirmation did not resume after Calendar recovered")
	}
}

func TestEmailSettingsRetimeOnlyQueuedRemindersAndPreserveSavedData(t *testing.T) {
	a, _ := emailTestApp(t)
	enableTestEmail(t, a)
	b := confirmEmailBooking(t, a, createEmailBooking(t, a, "mail-config-reminder", "service"))
	v := EmailSettings{Enabled: true, RemindersEnabled: true, ReminderHours: 2}
	if err := a.store.saveEmailSettings(v, a.now()); err != nil {
		t.Fatal(err)
	}
	if got := lastEmail(t, a, "reminder"); !got.ScheduledAt.Equal(b.Start.Add(-2 * time.Hour)) {
		t.Fatal("reminder timing was not configurable")
	}
	v.ReminderHours = 3
	if err := a.store.saveEmailSettings(v, a.now()); err == nil {
		t.Fatal("unsupported reminder interval was accepted")
	}
	v.ReminderHours = 2
	v.RemindersEnabled = false
	if err := a.store.saveEmailSettings(v, a.now()); err != nil {
		t.Fatal(err)
	}
	if emailCount(t, a, "reminder", "queued") != 0 {
		t.Fatal("turning reminders off left a send queued")
	}
	v.RemindersEnabled = true
	if err := a.store.saveEmailSettings(v, a.now()); err != nil {
		t.Fatal(err)
	}
	if emailCount(t, a, "reminder", "queued") != 0 {
		t.Fatal("reenabling reminders backfilled skipped appointments")
	}
	if err := a.store.disableEmailNotifications(); err != nil {
		t.Fatal(err)
	}
	if emailCount(t, a, "confirmation", "queued") != 0 {
		t.Fatal("disconnect did not stop queued customer mail")
	}
	saved, err := a.store.booking(b.ID)
	if err != nil || saved.Status != b.Status || !saved.Start.Equal(b.Start) {
		t.Fatal("email settings changed booking data")
	}
}

func TestUnconfirmedAndLateConfirmedJobsDoNotGetReminders(t *testing.T) {
	a, _ := emailTestApp(t)
	enableTestEmail(t, a)
	b := createEmailBooking(t, a, "late-confirmation-email", "service")
	if emailCount(t, a, "reminder", "") != 0 {
		t.Fatal("unconfirmed job received a reminder")
	}
	clock := b.Start.Add(-12 * time.Hour)
	a.now = func() time.Time { return clock }
	confirmEmailBooking(t, a, b)
	if emailCount(t, a, "confirmation", "queued") != 1 || emailCount(t, a, "reminder", "") != 0 {
		t.Fatal("late confirmation queued a duplicate immediate reminder")
	}
}

func TestEmailFailuresRetrySafelyAndUnknownSendsNeedAcknowledgement(t *testing.T) {
	for _, scenario := range []string{"retryable", "permanent", "uncertain", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			a, f := emailTestApp(t)
			enableTestEmail(t, a)
			op, csrf := configureTestOperator(t, a)
			createEmailBooking(t, a, "mail-failure-handling", "service")
			switch scenario {
			case "retryable":
				f.failure = &MailSendError{Retryable: true}
			case "permanent":
				f.failure = &MailSendError{}
			case "uncertain":
				f.failure = &MailSendError{Retryable: true, Uncertain: true}
			default:
				f.failure = errors.New("secret-bearing unexpected transport failure")
			}
			a.sendEmailDue(context.Background())
			job := lastEmail(t, a, "receipt")
			if strings.Contains(job.Error, "secret-bearing") {
				t.Fatal("raw transport failure leaked into admin history")
			}
			want := "uncertain"
			if scenario == "retryable" {
				want = "queued"
			}
			if scenario == "permanent" {
				want = "failed"
			}
			if job.Status != want {
				t.Fatalf("wrong durable error classification: %s want %s", job.Status, want)
			}
			a.sendEmailDue(context.Background())
			if f.calls() != 1 {
				t.Fatal("worker retried immediately or retried an uncertain send")
			}
			if scenario == "retryable" {
				clock := a.now().Add(time.Minute)
				a.now = func() time.Time { return clock }
				f.failure = nil
				a.sendEmailDue(context.Background())
			} else {
				body := map[string]bool{}
				if want == "uncertain" {
					w := request(a, true, "POST", "/api/admin/email/messages/"+job.ID+"/retry", body, op, csrf, a.cfg.AdminOrigin)
					if w.Code != 409 || !strings.Contains(w.Body.String(), "confirm_uncertain_retry") {
						t.Fatal("unknown delivery retry did not need acknowledgement")
					}
					body["confirmUncertain"] = true
				}
				f.failure = nil
				if w := request(a, true, "POST", "/api/admin/email/messages/"+job.ID+"/retry", body, op, csrf, a.cfg.AdminOrigin); w.Code != 202 {
					t.Fatalf("manual recovery failed: %d %s", w.Code, w.Body.String())
				}
				a.sendEmailDue(context.Background())
			}
			if f.calls() != 2 || emailCount(t, a, "receipt", "sent") != 1 {
				t.Fatal("safe/manual recovery did not persist Gmail acceptance")
			}
		})
	}
}

func TestInterruptedEmailIsUncertainAfterReopen(t *testing.T) {
	a, f := emailTestApp(t)
	enableTestEmail(t, a)
	createEmailBooking(t, a, "crash-window-email-test", "service")
	job := lastEmail(t, a, "receipt")
	if _, err := a.store.db.Exec("UPDATE email_outbox SET status='sending',attempt_started=?,attempts=1 WHERE id=?", a.now().Add(-3*time.Minute).Unix(), job.ID); err != nil {
		t.Fatal(err)
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	other.now = a.now
	other.mailer = f
	other.sendEmailDue(context.Background())
	if f.calls() != 0 || emailCount(t, other, "receipt", "uncertain") != 1 {
		t.Fatal("process restart repeated an email with unknown delivery status")
	}
}

func TestEmailRoutesProtectRecipientsHistoryAndRetries(t *testing.T) {
	a, f := emailTestApp(t)
	enableTestEmail(t, a)
	op, csrf := configureTestOperator(t, a)
	member, memberCSRF := joinTestMember(t, a, op, csrf, Owner{Sub: "friend-sub", Email: "friend@example.com"})
	for _, test := range []struct {
		cookie       *http.Cookie
		csrf, origin string
		want         int
	}{{nil, "", a.cfg.AdminOrigin, 401}, {op, "", a.cfg.AdminOrigin, 403}, {op, csrf, "https://attacker.invalid", 403}} {
		if w := request(a, true, "POST", "/api/admin/email/test", map[string]string{}, test.cookie, test.csrf, test.origin); w.Code != test.want {
			t.Fatal("test email bypassed workspace/origin/CSRF boundary")
		}
	}
	if w := request(a, true, "POST", "/api/admin/email/test", map[string]string{"to": "stranger@example.com"}, op, csrf, a.cfg.AdminOrigin); w.Code != 400 {
		t.Fatal("test endpoint accepted an arbitrary recipient")
	}
	w := request(a, true, "POST", "/api/admin/email/test", map[string]string{}, member, memberCSRF, a.cfg.AdminOrigin)
	if w.Code != 202 {
		t.Fatalf("shared member could not test sender connection: %d %s", w.Code, w.Body.String())
	}
	a.sendEmailDue(context.Background())
	if f.calls() != 1 || f.messages[0].To != "sender@example.com" {
		t.Fatal("test email did not go exclusively to connected sender")
	}
	if w = request(a, true, "POST", "/api/admin/email/test", map[string]string{}, op, csrf, a.cfg.AdminOrigin); w.Code != 429 {
		t.Fatal("test email throttle was absent")
	}
	job := lastEmail(t, a, "test")
	if w = request(a, true, "POST", "/api/admin/email/messages/"+job.ID+"/retry", map[string]bool{}, op, csrf, a.cfg.AdminOrigin); w.Code != 409 {
		t.Fatal("already accepted message could be resent")
	}
	w = request(a, true, "GET", "/api/admin/email", nil, member, "", "")
	var response struct {
		History []EmailRecord `json:"history"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.History) != 1 {
		t.Fatal("shared email history was unavailable")
	}
	if strings.Contains(w.Body.String(), f.messages[0].Text) || strings.Contains(w.Body.String(), "google_tokens") {
		t.Fatal("history exposed email body or credentials")
	}
}

func TestStaleEmailManualRetryIsRejectedAndOldHistoryPruned(t *testing.T) {
	a, f := emailTestApp(t)
	enableTestEmail(t, a)
	op, csrf := configureTestOperator(t, a)
	b := createEmailBooking(t, a, "stale-email-retry-test", "service")
	f.failure = &MailSendError{Uncertain: true}
	a.sendEmailDue(context.Background())
	job := lastEmail(t, a, "receipt")
	if _, err := a.patchBooking(b.ID, ptr("cancelled"), nil); err != nil {
		t.Fatal(err)
	}
	if w := request(a, true, "POST", "/api/admin/email/messages/"+job.ID+"/retry", map[string]bool{"confirmUncertain": true}, op, csrf, a.cfg.AdminOrigin); w.Code != 409 || !strings.Contains(w.Body.String(), "email_outdated") {
		t.Fatal("outdated email could be manually resent")
	}
	if _, err := a.store.db.Exec("UPDATE email_outbox SET status='sent',created=? WHERE kind='cancellation'", a.now().AddDate(0, 0, -181).Unix()); err != nil {
		t.Fatal(err)
	}
	a.store.cleanup(a.now())
	if emailCount(t, a, "cancellation", "") != 0 || emailCount(t, a, "receipt", "uncertain") != 1 {
		t.Fatal("history retention removed unresolved mail or kept old completed mail")
	}
	if _, err := a.store.booking(b.ID); err != nil {
		t.Fatal("email retention deleted the appointment")
	}
}
