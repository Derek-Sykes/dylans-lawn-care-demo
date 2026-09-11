package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testMailApp(t *testing.T) (*App, *googleFixture, *http.Cookie, string) {
	t.Helper()
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	if err := a.google.connect(context.Background(), "fixture-code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := testIdentitySession(t, a, Owner{Sub: f.sub, Email: f.email})
	return a, f, cookie, csrf
}

func setTestMailGrant(t *testing.T, a *App) {
	t.Helper()
	owner, err := a.google.owner()
	if err != nil {
		t.Fatal(err)
	}
	grant := mailGrant{Subject: owner.Sub, Email: owner.Email, Tokens: GoogleTokens{Access: "mail-access", Refresh: "mail-refresh", Expiry: a.now().Add(time.Hour), Scope: "openid email " + gmailSendScope}}
	if err = a.store.putSecret("google_mail_tokens", grant); err != nil {
		t.Fatal(err)
	}
}

func savedMailRecord(t *testing.T, a *App, cookie *http.Cookie, csrf string) OAuthState {
	t.Helper()
	q, binding := beginAuthRequest(t, a, "/api/admin/email/connect", nil, cookie, csrf)
	record, err := a.consumeState(q.Get("state"), binding.Value)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func calendarCiphertexts(t *testing.T, a *App) [][]byte {
	t.Helper()
	out := make([][]byte, 2)
	for i, key := range []string{"owner", "google_tokens"} {
		if err := a.store.db.QueryRow("SELECT value FROM secrets WHERE key=?", key).Scan(&out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func assertCalendarCiphertexts(t *testing.T, a *App, before [][]byte) {
	t.Helper()
	after := calendarCiphertexts(t, a)
	for i := range before {
		if !bytes.Equal(before[i], after[i]) {
			t.Fatal("email operation changed encrypted Calendar identity or credentials")
		}
	}
}

func TestEmailMIMEAndHeaderInjection(t *testing.T) {
	want := "Hello Zoë,\nYour lawn appointment request is received.\nIt still needs confirmation."
	msg := EmailMessage{ID: "fixture-message_123", To: "customer@example.com", Subject: "Dylan’s: request received", Text: want}
	raw, err := encodeEmail("owner@example.com", "Dylan’s Lawn Care", msg, instant("2026-09-11T13:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	for key, address := range map[string]string{"From": "owner@example.com", "Reply-To": "owner@example.com", "To": "customer@example.com"} {
		parsed, e := mail.ParseAddress(m.Header.Get(key))
		if e != nil || parsed.Address != address {
			t.Fatalf("incorrect %s header", key)
		}
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != msg.Subject {
		t.Fatal("Unicode subject did not round trip")
	}
	body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
	if err != nil || strings.ReplaceAll(string(body), "\r\n", "\n") != want {
		t.Fatal("Unicode message body did not round trip")
	}
	if m.Header.Get("Message-ID") != "<fixture-message_123@booking.local>" || m.Header.Get("Auto-Submitted") != "auto-generated" || m.Header.Get("Bcc") != "" || m.Header.Get("Cc") != "" {
		t.Fatal("unexpected routing or stable message headers")
	}
	for _, tc := range []struct {
		name, from, business string
		message              EmailMessage
	}{
		{"recipient", "owner@example.com", "Business", EmailMessage{ID: "valid", To: "customer@example.com\r\nBcc: attacker@example.com", Subject: "Receipt", Text: "ok"}},
		{"sender", "owner@example.com\nBcc: attacker@example.com", "Business", msg},
		{"business", "owner@example.com", "Business\r\nBcc: attacker@example.com", msg},
		{"subject", "owner@example.com", "Business", EmailMessage{ID: "valid", To: msg.To, Subject: "Receipt\r\nBcc: attacker@example.com", Text: "ok"}},
		{"message-id", "owner@example.com", "Business", EmailMessage{ID: "bad>\r\nBcc: attacker@example.com", To: msg.To, Subject: "Receipt", Text: "ok"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := encodeEmail(tc.from, tc.business, tc.message, time.Now()); err == nil {
				t.Fatal("header injection was accepted")
			}
		})
	}
}

func TestGmailProviderUsesOnlyConnectedSenderAndSendEndpoint(t *testing.T) {
	a, _, _, _ := testMailApp(t)
	setTestMailGrant(t, a)
	before := calendarCiphertexts(t, a)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/gmail/v1/users/me/messages/send" || r.Header.Get("Authorization") != "Bearer mail-access" {
			t.Error("provider used unsupported Gmail read/identity route or wrong grant")
			w.WriteHeader(400)
			return
		}
		var payload struct {
			Raw string `json:"raw"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid send payload")
		}
		b, err := base64.RawURLEncoding.DecodeString(payload.Raw)
		if err != nil {
			t.Error(err)
		}
		m, err := mail.ReadMessage(bytes.NewReader(b))
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		from, err := mail.ParseAddress(m.Header.Get("From"))
		if err != nil || from.Address != "owner@example.com" {
			t.Error("sender differs from connected Calendar identity")
		}
		writeJSON(w, 200, map[string]string{"id": "gmail-message-reference"})
	}))
	defer server.Close()
	a.google.cfg.GmailURL = server.URL + "/gmail/v1"
	id, err := a.mailer.Send(context.Background(), EmailMessage{ID: "fixture-mail", To: "customer@example.com", Subject: "Request received", Text: "Await confirmation."})
	if err != nil || id != "gmail-message-reference" || calls != 1 {
		t.Fatalf("send failed: %v calls=%d", err, calls)
	}
	assertCalendarCiphertexts(t, a, before)
	var grant mailGrant
	_ = a.store.getSecret("google_mail_tokens", &grant)
	grant.Subject = "different-sub"
	_ = a.store.putSecret("google_mail_tokens", grant)
	if a.mailer.Connected() {
		t.Fatal("mismatched Gmail identity was connected")
	}
	if _, err = a.mailer.Send(context.Background(), EmailMessage{ID: "wrong-owner", To: "customer@example.com", Subject: "x", Text: "x"}); err == nil || calls != 1 {
		t.Fatal("unrelated sender reached Gmail")
	}
}

func TestGmailSendResponseClassesDoNotBlindlyRetry(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		status               int
		body                 string
		retryable, uncertain bool
	}{
		{"missing-reference", 200, `{}`, false, true},
		{"unreadable-success", 200, `{`, false, true},
		{"expired-access", 401, `{}`, true, false},
		{"permission", 403, `{}`, false, false},
		{"rate-quota-403", 403, `{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`, true, false},
		{"user-quota-403", 403, `{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`, true, false},
		{"daily-quota-403", 403, `{"error":{"errors":[{"reason":"dailyLimitExceeded"}]}}`, true, false},
		{"rate-limit", 429, `{}`, true, false},
		{"provider-failure", 503, `{}`, false, true},
		{"bad-request", 400, `{}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _, _ := testMailApp(t)
			setTestMailGrant(t, a)
			before := calendarCiphertexts(t, a)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			a.google.cfg.GmailURL = server.URL
			_, err := a.mailer.Send(context.Background(), EmailMessage{ID: "fixture", To: "customer@example.com", Subject: "Request", Text: "Text"})
			var failure *MailSendError
			if !errors.As(err, &failure) || failure.Retryable != tc.retryable || failure.Uncertain != tc.uncertain || calls != 1 {
				t.Fatalf("unexpected failure class: %#v calls=%d", failure, calls)
			}
			assertCalendarCiphertexts(t, a, before)
		})
	}
	t.Run("lost-response", func(t *testing.T) {
		a, _, _, _ := testMailApp(t)
		setTestMailGrant(t, a)
		calls := 0
		a.google.cfg.GmailURL = "https://gmail.invalid/gmail/v1"
		a.google.client.Transport = authRoundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("fixture response lost")
		})
		_, err := a.mailer.Send(context.Background(), EmailMessage{ID: "fixture", To: "customer@example.com", Subject: "Request", Text: "Text"})
		var failure *MailSendError
		if !errors.As(err, &failure) || !failure.Uncertain || failure.Retryable || calls != 1 {
			t.Fatal("lost response was retried or marked safe")
		}
	})
}

func TestGmailRefreshKeepsCalendarTokenSeparate(t *testing.T) {
	a, f, _, _ := testMailApp(t)
	setTestMailGrant(t, a)
	before := calendarCiphertexts(t, a)
	var grant mailGrant
	_ = a.store.getSecret("google_mail_tokens", &grant)
	grant.Tokens.Expiry = a.now().Add(-time.Minute)
	_ = a.store.putSecret("google_mail_tokens", grant)
	f.mu.Lock()
	f.scope = "openid email " + gmailSendScope
	f.mu.Unlock()
	refreshToken := ""
	a.google.client.Transport = authRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/token" {
			b, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(b))
			values, _ := url.ParseQuery(string(b))
			refreshToken = values.Get("refresh_token")
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	token, err := a.google.mailAccessToken(context.Background())
	if err != nil || token != "fixture-refreshed-access" || refreshToken != "mail-refresh" {
		t.Fatalf("wrong email refresh token: %v", err)
	}
	assertCalendarCiphertexts(t, a, before)
	_ = a.store.getSecret("google_mail_tokens", &grant)
	if grant.Tokens.Refresh != "mail-refresh" || !hasMailScope(grant.Tokens.Scope) {
		t.Fatal("mail refresh lost grant")
	}
}

func TestGmailConsentIsolationScopesIdentityAndOfflineGrant(t *testing.T) {
	for _, scenario := range []string{"success", "missing-scope", "wrong-identity", "missing-refresh"} {
		t.Run(scenario, func(t *testing.T) {
			a, f, cookie, csrf := testMailApp(t)
			before := calendarCiphertexts(t, a)
			f.mu.Lock()
			f.scope = "openid email " + gmailSendScope
			switch scenario {
			case "missing-scope":
				f.scope = "openid email"
			case "wrong-identity":
				f.sub = "other-sub"
				f.email = "other@example.com"
			case "missing-refresh":
				a.google.client.Transport = authRoundTrip(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/token" {
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"fixture-access","expires_in":3600,"scope":"openid email ` + gmailSendScope + `"}`))}, nil
					}
					return http.DefaultTransport.RoundTrip(r)
				})
			}
			f.mu.Unlock()
			record := savedMailRecord(t, a, cookie, csrf)
			err := a.connectMail(context.Background(), "fixture-code", record)
			if scenario == "success" {
				if err != nil || !a.mailer.Connected() {
					t.Fatalf("Gmail consent failed: %v", err)
				}
			} else {
				if err == nil || a.mailer.Connected() {
					t.Fatal("invalid Gmail consent authorized sending")
				}
				var grant mailGrant
				if e := a.store.getSecret("google_mail_tokens", &grant); !errors.Is(e, sql.ErrNoRows) {
					t.Fatal("failed consent retained a mail grant")
				}
			}
			assertCalendarCiphertexts(t, a, before)
		})
	}
}

func TestGmailOAuthIsSeparateBoundSingleUseAndDisconnectable(t *testing.T) {
	a, f, cookie, csrf := testMailApp(t)
	f.mu.Lock()
	f.scope = "openid email " + gmailSendScope
	f.mu.Unlock()
	before := calendarCiphertexts(t, a)
	q, binding := beginAuthRequest(t, a, "/api/admin/email/connect", nil, cookie, csrf)
	if q.Get("scope") != "openid email "+gmailSendScope || q.Get("code_challenge_method") != "S256" || q.Get("include_granted_scopes") != "false" || q.Get("access_type") != "offline" {
		t.Fatal("Gmail consent has wrong scopes or PKCE")
	}
	if strings.Contains(a.google.signInURL("state", "verifier", "redirect"), "gmail") {
		t.Fatal("Gmail permission leaked into ordinary sign-in")
	}
	if !strings.Contains(a.google.authorizationURL("state", "verifier", "redirect"), "gmail") {
		t.Fatal("combined Google connection must request Gmail permission")
	}
	wrong := *binding
	wrong.Value = "different-browser"
	_ = callback(a, q, &wrong)
	if a.mailer.Connected() {
		t.Fatal("wrong browser accepted Gmail consent")
	}
	w := callback(a, q, binding)
	if !a.mailer.Connected() || w.Code != 303 {
		t.Fatalf("bound callback failed: %s", w.Header().Get("Location"))
	}
	var beforeGrant []byte
	_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_mail_tokens'").Scan(&beforeGrant)
	_ = callback(a, q, binding)
	var afterGrant []byte
	_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_mail_tokens'").Scan(&afterGrant)
	if !bytes.Equal(beforeGrant, afterGrant) {
		t.Fatal("callback replay rewrote Gmail grant")
	}
	assertCalendarCiphertexts(t, a, before)
	q, binding = beginAuthRequest(t, a, "/api/admin/email/connect", nil, cookie, csrf)
	revokes := 0
	a.google.client.Transport = authRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/revoke" {
			revokes++
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	if w = request(a, true, "POST", "/api/admin/email/disconnect", nil, cookie, csrf, a.cfg.AdminOrigin); w.Code != 204 {
		t.Fatalf("mail disconnect failed: %d", w.Code)
	}
	_ = callback(a, q, binding)
	if a.mailer.Connected() {
		t.Fatal("pending OAuth restored Gmail after disconnect")
	}
	if revokes != 0 {
		t.Fatal("mail-only disconnect revoked the entire Google project grant")
	}
	assertCalendarCiphertexts(t, a, before)
}

func TestCalendarDisconnectClearsBothGrantsAndStopsEmail(t *testing.T) {
	a, _, cookie, csrf := testMailApp(t)
	setTestMailGrant(t, a)
	if err := a.store.saveEmailSettings(EmailSettings{Enabled: true, RemindersEnabled: true, ReminderHours: 24}, a.now()); err != nil {
		t.Fatal(err)
	}
	owner, _ := a.google.owner()
	w := request(a, true, "POST", "/api/admin/google/disconnect", nil, cookie, csrf, a.cfg.AdminOrigin)
	if w.Code != 204 {
		t.Fatalf("Calendar disconnect failed: %d", w.Code)
	}
	for _, key := range []string{"google_tokens", "google_mail_tokens"} {
		var b []byte
		if err := a.store.db.QueryRow("SELECT value FROM secrets WHERE key=?", key).Scan(&b); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("disconnect retained %s", key)
		}
	}
	settings, err := a.store.emailSettings()
	if err != nil || settings.Enabled || a.mailer.Connected() {
		t.Fatal("Calendar disconnect left email sending enabled")
	}
	after, err := a.google.owner()
	if err != nil || after != owner {
		t.Fatal("disconnect removed Calendar assignment instead of preserving existing events")
	}
}

func TestGmailConnectRequiresCalendarOwnerAndCSRF(t *testing.T) {
	a, _, owner, ownerCSRF := testMailApp(t)
	op, opCSRF := configureTestOperator(t, a)
	for _, tc := range []struct {
		cookie       *http.Cookie
		csrf, origin string
		want         int
	}{
		{nil, "", a.cfg.AdminOrigin, 401}, {owner, "", a.cfg.AdminOrigin, 403}, {owner, ownerCSRF, "https://attacker.invalid", 403}, {op, opCSRF, a.cfg.AdminOrigin, 403},
	} {
		for _, path := range []string{"/api/admin/email/connect", "/api/admin/email/disconnect"} {
			if w := request(a, true, "POST", path, nil, tc.cookie, tc.csrf, tc.origin); w.Code != tc.want {
				t.Fatalf("%s boundary %d want %d", path, w.Code, tc.want)
			}
		}
	}
	if w := request(a, false, "GET", "/api/admin/email", nil, nil, "", ""); w.Code != 404 {
		t.Fatal("public listener exposed email history")
	}
}
