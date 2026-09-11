package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func combinedTestScope() string { return googleScopes }

func readCombinedState(t *testing.T, a *App, q url.Values) OAuthState {
	t.Helper()
	key := a.store.hash(q.Get("state"))
	var sealed []byte
	if err := a.store.db.QueryRow("SELECT value FROM oauth_states WHERE id=?", key).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	raw, err := a.store.open(sealed, "oauth:"+key)
	if err != nil {
		t.Fatal(err)
	}
	var state OAuthState
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func assertCombinedGrant(t *testing.T, a *App, subject string) {
	t.Helper()
	owner, err := a.google.owner()
	if err != nil || owner.Sub != subject || owner.CalendarID == "" {
		t.Fatal("explicit Google connection did not assign the selected Calendar account")
	}
	var calendar GoogleTokens
	var mail mailGrant
	if err = a.store.getSecret("google_tokens", &calendar); err != nil {
		t.Fatal(err)
	}
	if err = a.store.getSecret("google_mail_tokens", &mail); err != nil {
		t.Fatal(err)
	}
	if mail.Subject != owner.Sub || mail.Email != owner.Email || mail.Tokens.Refresh == "" || calendar.Refresh != mail.Tokens.Refresh || !validScopes(calendar.Scope) || !hasMailScope(mail.Tokens.Scope) {
		t.Fatal("one connection did not persist matching Calendar and sending grants")
	}
	if !a.google.Connected() || a.mailer == nil || !a.mailer.Connected() {
		t.Fatal("both Google features were not connected after one callback")
	}
	settings, err := a.store.emailSettings()
	if err != nil || settings.Enabled || emailCount(t, a, "", "") != 0 {
		t.Fatal("connecting Google silently activated or backfilled customer emails")
	}
}

func TestCombinedGoogleMainConnectionRequestsBothPermissionsAndPersistsBoth(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	f.scope = combinedTestScope()
	op, csrf := configureTestOperator(t, a)
	// A member who joins the shared workspace does not claim Calendar ownership.
	ownerCookie, ownerCSRF := joinTestMember(t, a, op, csrf, Owner{Sub: "owner-sub", Email: "owner@example.com"})
	if _, err := a.google.owner(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("invitation claimed Calendar before explicit connection")
	}
	q, binding := beginOAuth(t, a, ownerCookie, ownerCSRF)
	if !validScopes(q.Get("scope")) || !hasMailScope(q.Get("scope")) || q.Get("access_type") != "offline" || q.Get("code_challenge_method") != "S256" {
		t.Fatal("main Google button did not request both features in one protected authorization")
	}
	state := readCombinedState(t, a, q)
	if state.Purpose != "calendar" || !state.ConnectMail || state.ActorSubject != "owner-sub" {
		t.Fatal("combined intent was not stored in the private bound OAuth state")
	}
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=connected") {
		t.Fatalf("combined connection failed: %s", w.Header().Get("Location"))
	}
	assertCombinedGrant(t, a, "owner-sub")
	if f.tokenCalls != 1 || f.creates != 1 {
		t.Fatal("one connection used multiple exchanges or created more than one Calendar")
	}
	if w = callback(a, q, binding); !strings.HasSuffix(w.Header().Get("Location"), "google=failed") || f.tokenCalls != 1 {
		t.Fatal("combined OAuth state could be replayed")
	}
}

func TestCombinedGooglePartialConsentDoesNotSaveEitherConnection(t *testing.T) {
	for _, scope := range []string{calendarScopes, "openid email " + gmailSendScope} {
		t.Run(strings.ReplaceAll(scope, "/", "_"), func(t *testing.T) {
			a, _ := testApp(t)
			f := mockGoogle(t, a)
			f.scope = scope
			cookie, csrf := bootstrap(t, a)
			q, binding := beginOAuth(t, a, cookie, csrf)
			w := callback(a, q, binding)
			if !strings.HasSuffix(w.Header().Get("Location"), "google=missing_scopes") {
				t.Fatalf("partial permission did not explain missing access: %s", w.Header().Get("Location"))
			}
			var count int
			if err := a.store.db.QueryRow("SELECT COUNT(*) FROM secrets WHERE key IN ('owner','google_tokens','google_mail_tokens')").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 || f.creates != 0 {
				t.Fatal("partial consent created or saved a partial Google connection")
			}
		})
	}
}

func TestCombinedGoogleUpgradeKeepsLegacyCalendarAndAppointments(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	f.scope = calendarScopes
	if err := a.google.connect(context.Background(), "legacy-code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	beforeOwner, err := a.google.owner()
	if err != nil {
		t.Fatal(err)
	}
	var mail mailGrant
	if err = a.store.getSecret("google_mail_tokens", &mail); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("legacy Calendar-only setup unexpectedly granted Gmail")
	}
	b, _, err := a.createBooking(context.Background(), testInput("legacy-combined-upgrade"))
	if err != nil {
		t.Fatal(err)
	}
	a.syncDue(context.Background())
	beforeBooking, err := a.store.booking(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := testIdentitySession(t, a, Owner{Sub: beforeOwner.Sub, Email: beforeOwner.Email})
	f.scope = combinedTestScope()
	q, binding := beginOAuth(t, a, cookie, csrf)
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=connected") {
		t.Fatalf("legacy upgrade failed: %s", w.Header().Get("Location"))
	}
	assertCombinedGrant(t, a, beforeOwner.Sub)
	afterOwner, _ := a.google.owner()
	afterBooking, err := a.store.booking(b.ID)
	if err != nil || afterOwner != beforeOwner || afterBooking != beforeBooking || f.creates != 1 {
		t.Fatal("enabling both features replaced existing Calendar or appointment data")
	}
}

func TestCombinedGoogleWrongAccountLeavesExistingGrantsUntouched(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	f.scope = combinedTestScope()
	cookie, csrf := bootstrap(t, a)
	q, binding := beginOAuth(t, a, cookie, csrf)
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=connected") {
		t.Fatal("initial combined fixture did not connect")
	}
	cookie = sessionCookie(t, a, w)
	actor, err := a.sessionByID(a.store.hash(cookie.Value))
	if err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{}
	for _, key := range []string{"owner", "google_tokens", "google_mail_tokens"} {
		var data []byte
		if err = a.store.db.QueryRow("SELECT value FROM secrets WHERE key=?", key).Scan(&data); err != nil {
			t.Fatal(err)
		}
		before[key] = data
	}
	f.sub, f.email = "stranger-sub", "stranger@example.com"
	q, binding = beginOAuth(t, a, cookie, actor.CSRF)
	w = callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=wrong_account") {
		t.Fatal("different Google identity replaced the authenticated account")
	}
	for key, old := range before {
		var data []byte
		if err = a.store.db.QueryRow("SELECT value FROM secrets WHERE key=?", key).Scan(&data); err != nil || !bytes.Equal(data, old) {
			t.Fatal("wrong-account callback rewrote existing encrypted grants")
		}
	}
	if f.creates != 1 {
		t.Fatal("wrong-account reconnect created another Calendar")
	}
}

func TestCombinedGoogleGrantWritesAreAtomic(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	f.scope = combinedTestScope()
	if _, err := a.store.db.Exec("CREATE TRIGGER reject_combined_mail BEFORE INSERT ON secrets WHEN NEW.key='google_mail_tokens' BEGIN SELECT RAISE(ABORT,'fixture-mail-write-failure'); END"); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := bootstrap(t, a)
	q, binding := beginOAuth(t, a, cookie, csrf)
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=failed") {
		t.Fatal("failed grant write claimed successful combined setup")
	}
	var count int
	if err := a.store.db.QueryRow("SELECT COUNT(*) FROM secrets WHERE key IN ('owner','google_tokens','google_mail_tokens')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed Gmail grant write left Calendar partially connected")
	}
}

func TestCombinedGooglePortalSignInRemainsIdentityOnly(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	f.scope = combinedTestScope()
	if err := a.store.importOperatorIdentity(OperatorIdentity{Schema: 1, GoogleSub: "owner-sub", Email: "owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	q, binding := beginSignIn(t, a, nil, "")
	if q.Get("scope") != "openid email" || q.Get("access_type") == "offline" {
		t.Fatal("portal sign-in requested business-account permissions")
	}
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=signed_in") {
		t.Fatal("ordinary portal sign-in failed")
	}
	var count int
	if err := a.store.db.QueryRow("SELECT COUNT(*) FROM secrets WHERE key IN ('owner','google_tokens','google_mail_tokens')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 || f.creates != 0 {
		t.Fatal("identity-only sign-in silently connected Calendar or email")
	}
}

func TestCombinedGoogleDoesNotReuseCalendarOnlyRefreshForGmail(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	f.scope = calendarScopes
	if err := a.google.connect(context.Background(), "legacy-code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	before := calendarCiphertexts(t, a)
	cookie, csrf := testIdentitySession(t, a, Owner{Sub: f.sub, Email: f.email})
	f.scope = combinedTestScope()
	a.google.client.Transport = authRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/token" {
			body, _ := json.Marshal(map[string]any{"access_token": "combined-without-offline-grant", "expires_in": 3600, "scope": combinedTestScope()})
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	q, binding := beginOAuth(t, a, cookie, csrf)
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=failed") {
		t.Fatal("combined setup claimed durable Gmail access without an eligible refresh grant")
	}
	assertCalendarCiphertexts(t, a, before)
	var grant mailGrant
	if err := a.store.getSecret("google_mail_tokens", &grant); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("Calendar-only refresh token was stored as Gmail authorization")
	}
	if f.creates != 1 {
		t.Fatal("missing offline grant replaced the legacy Calendar")
	}
}

func TestCombinedGoogleRefreshFallbackRequiresMatchingBothScopeGrant(t *testing.T) {
	for _, test := range []struct {
		name, scope string
		success     bool
	}{{"no_prior_grant", "", false}, {"calendar_only", calendarScopes, false}, {"mail_only", "openid email " + gmailSendScope, false}, {"combined", combinedTestScope(), true}} {
		t.Run(test.name, func(t *testing.T) {
			a, _ := testApp(t)
			f := mockGoogle(t, a)
			f.scope = calendarScopes
			if err := a.google.connect(context.Background(), "legacy-code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
				t.Fatal(err)
			}
			if test.scope != "" {
				grant := mailGrant{Subject: f.sub, Email: f.email, Tokens: GoogleTokens{Refresh: "previous-scoped-refresh", Scope: test.scope}}
				if err := a.store.putSecret("google_mail_tokens", grant); err != nil {
					t.Fatal(err)
				}
			}
			before := calendarCiphertexts(t, a)
			var priorMail []byte
			_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_mail_tokens'").Scan(&priorMail)
			cookie, csrf := testIdentitySession(t, a, Owner{Sub: f.sub, Email: f.email})
			a.google.client.Transport = authRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/token" {
					body, _ := json.Marshal(map[string]any{"access_token": "combined-without-new-refresh", "expires_in": 3600, "scope": combinedTestScope()})
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
				}
				return http.DefaultTransport.RoundTrip(r)
			})
			q, binding := beginOAuth(t, a, cookie, csrf)
			w := callback(a, q, binding)
			if test.success {
				if !strings.HasSuffix(w.Header().Get("Location"), "google=connected") {
					t.Fatal("existing combined grant was not retained for offline access")
				}
				assertCombinedGrant(t, a, f.sub)
				var saved GoogleTokens
				if err := a.store.getSecret("google_tokens", &saved); err != nil || saved.Refresh != "previous-scoped-refresh" {
					t.Fatal("matching existing combined refresh grant was not reused")
				}
			} else {
				if !strings.HasSuffix(w.Header().Get("Location"), "google=failed") {
					t.Fatal("incomplete or absent refresh grant was accepted for both features")
				}
				assertCalendarCiphertexts(t, a, before)
				var saved []byte
				_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_mail_tokens'").Scan(&saved)
				if !bytes.Equal(priorMail, saved) {
					t.Fatal("failed combined upgrade changed a prior email grant")
				}
			}
			if f.creates != 1 {
				t.Fatal("refresh fallback changed Calendar identity")
			}
		})
	}
}
