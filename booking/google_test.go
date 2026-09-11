package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type googleFixture struct {
	mu                                                           sync.Mutex
	sub, email, scope, tokenError                                string
	verified, calendarMissing, busyError, loseInsert, loseDelete bool
	tokenCalls, refreshCalls, creates, inserts, deletes          int
	lastVerifier, lastSecret                                     string
	events                                                       map[string]map[string]any
	server                                                       *httptest.Server
}

func mockGoogle(t *testing.T, a *App) *googleFixture {
	t.Helper()
	f := &googleFixture{sub: "owner-sub", email: "owner@example.com", scope: googleScopes, verified: true, events: map[string]map[string]any{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/token":
			_ = r.ParseForm()
			f.tokenCalls++
			f.lastVerifier = r.Form.Get("code_verifier")
			f.lastSecret = r.Form.Get("client_secret")
			if f.tokenError != "" {
				writeJSON(w, 400, map[string]string{"error": "invalid_request", "error_description": f.tokenError})
				return
			}
			if r.Form.Get("grant_type") == "refresh_token" {
				f.refreshCalls++
				writeJSON(w, 200, map[string]any{"access_token": "fixture-refreshed-access", "expires_in": 3600, "scope": f.scope})
				return
			}
			writeJSON(w, 200, map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "expires_in": 3600, "scope": f.scope})
		case r.URL.Path == "/userinfo":
			writeJSON(w, 200, map[string]any{"sub": f.sub, "email": f.email, "email_verified": f.verified})
		case r.URL.Path == "/calendar/v3/calendars" && r.Method == "POST":
			f.creates++
			writeJSON(w, 200, map[string]string{"id": "fixture-booking-calendar"})
		case r.URL.Path == "/calendar/v3/calendars/fixture-booking-calendar" && r.Method == "GET":
			if f.calendarMissing {
				w.WriteHeader(404)
			} else {
				writeJSON(w, 200, map[string]string{"id": "fixture-booking-calendar"})
			}
		case r.URL.Path == "/calendar/v3/freeBusy":
			var body struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body.Items) != 2 || body.Items[0].ID != "primary" || body.Items[1].ID != "fixture-booking-calendar" {
				t.Error("freebusy did not cover primary and booking calendars")
			}
			if f.busyError {
				writeJSON(w, 200, map[string]any{"calendars": map[string]any{"primary": map[string]any{"errors": []map[string]string{{"reason": "notFound"}}}}})
				return
			}
			writeJSON(w, 200, map[string]any{"calendars": map[string]any{"primary": map[string]any{"busy": []any{}}, "fixture-booking-calendar": map[string]any{"busy": []any{}}}})
		case r.URL.Path == "/calendar/v3/calendars/fixture-booking-calendar/events" && r.Method == "GET":
			events := []map[string]any{}
			for _, event := range f.events {
				events = append(events, event)
			}
			writeJSON(w, 200, map[string]any{"items": events, "nextSyncToken": "fixture-sync-token", "timeZone": "America/New_York"})
		case strings.HasPrefix(r.URL.Path, "/calendar/v3/calendars/fixture-booking-calendar/events"):
			if r.Method == "POST" {
				var event map[string]any
				_ = json.NewDecoder(r.Body).Decode(&event)
				id, _ := event["id"].(string)
				if _, ok := f.events[id]; ok {
					w.WriteHeader(409)
					return
				}
				f.events[id] = event
				f.inserts++
				if f.loseInsert {
					f.loseInsert = false
					w.WriteHeader(503)
					return
				}
				writeJSON(w, 200, event)
				return
			}
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			event, ok := f.events[id]
			if !ok {
				w.WriteHeader(404)
				return
			}
			if r.Method == "DELETE" {
				delete(f.events, id)
				f.deletes++
				if f.loseDelete {
					f.loseDelete = false
					w.WriteHeader(503)
					return
				}
				w.WriteHeader(204)
				return
			}
			writeJSON(w, 200, event)
		case r.URL.Path == "/revoke":
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected provider route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	a.google.cfg.AuthURL = f.server.URL + "/authorize"
	a.google.cfg.TokenURL = f.server.URL + "/token"
	a.google.cfg.UserInfoURL = f.server.URL + "/userinfo"
	a.google.cfg.CalendarURL = f.server.URL + "/calendar/v3"
	a.google.cfg.RevokeURL = f.server.URL + "/revoke"
	a.calendar = a.google
	return f
}
func beginOAuth(t *testing.T, a *App, cookie *http.Cookie, csrf string) (url.Values, *http.Cookie) {
	return beginAuthRequest(t, a, "/api/admin/google/connect", nil, cookie, csrf)
}
func beginSignIn(t *testing.T, a *App, cookie *http.Cookie, csrf string) (url.Values, *http.Cookie) {
	return beginAuthRequest(t, a, "/api/admin/signin", map[string]string{}, cookie, csrf)
}
func beginAuthRequest(t *testing.T, a *App, path string, body any, cookie *http.Cookie, csrf string) (url.Values, *http.Cookie) {
	t.Helper()
	w := request(a, true, "POST", path, body, cookie, csrf, a.cfg.AdminOrigin)
	if w.Code != 200 {
		t.Fatalf("connect status %d", w.Code)
	}
	var response map[string]string
	if json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal("invalid connect response")
	}
	u, err := url.Parse(response["url"])
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == a.oauthCookieName() {
			return u.Query(), c
		}
	}
	t.Fatal("missing OAuth browser binding")
	return nil, nil
}
func callback(a *App, q url.Values, binding *http.Cookie) *httptest.ResponseRecorder {
	values := url.Values{"state": {q.Get("state")}, "code": {"fixture-code"}}
	return request(a, true, "GET", "/oauth/callback?"+values.Encode(), nil, binding, "", "")
}
func TestOAuthPKCEBrowserBindingReplayAndScopes(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	cookie, csrf := bootstrap(t, a)
	q, binding := beginOAuth(t, a, cookie, csrf)
	if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != a.cfg.AdminOrigin+"/oauth/callback" || q.Get("scope") != googleScopes {
		t.Fatal("incorrect OAuth request")
	}
	var encrypted []byte
	if err := a.store.db.QueryRow("SELECT value FROM oauth_states WHERE id=?", a.store.hash(q.Get("state"))).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	raw, err := a.store.open(encrypted, "oauth:"+a.store.hash(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	var saved OAuthState
	_ = json.Unmarshal(raw, &saved)
	challenge := sha256.Sum256([]byte(saved.Verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) || len(saved.Verifier) < 43 {
		t.Fatal("PKCE challenge does not match private verifier")
	}
	wrong := *binding
	wrong.Value = randomToken(32)
	if w := callback(a, q, &wrong); !strings.Contains(w.Header().Get("Location"), "google=failed") || f.tokenCalls != 0 {
		t.Fatal("wrong browser consumed authorization")
	}
	w := callback(a, q, binding)
	if !strings.Contains(w.Header().Get("Location"), "google=connected") || f.tokenCalls != 1 || f.lastVerifier != saved.Verifier {
		t.Fatal("valid OAuth callback failed")
	}
	if f.lastSecret != "" {
		t.Fatal("desktop secret unexpectedly sent")
	}
	if w = callback(a, q, binding); !strings.Contains(w.Header().Get("Location"), "google=failed") || f.tokenCalls != 1 {
		t.Fatal("OAuth state replay reached token endpoint")
	}
	owner, err := a.google.owner()
	if err != nil || owner.Sub != "owner-sub" || owner.CalendarID != "fixture-booking-calendar" {
		t.Fatal("owner and dedicated calendar were not persisted")
	}
	if f.creates != 1 {
		t.Fatal("dedicated calendar not created exactly once")
	}
	// Public status never exposes the owner even after successful OAuth.
	w = request(a, true, "GET", "/api/admin/session", nil, nil, "", "")
	if strings.Contains(w.Body.String(), "owner@example.com") {
		t.Fatal("anonymous session exposed owner address")
	}
	for _, scope := range []string{"openid email", busyScope, eventsScope, "https://www.googleapis.com/auth/calendar.events.owned " + busyScope} {
		if validScopes(scope) {
			t.Fatal("incomplete or wrong Calendar permissions accepted")
		}
	}
}
func TestOAuthReturningOwnerExpiryAndMissingSecret(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	cookie, csrf := bootstrap(t, a)
	q, binding := beginOAuth(t, a, cookie, csrf)
	_, _ = a.store.db.Exec("UPDATE oauth_states SET expires=0")
	if w := callback(a, q, binding); !strings.Contains(w.Header().Get("Location"), "google=failed") || f.tokenCalls != 0 {
		t.Fatal("expired OAuth state accepted")
	}
	f.tokenError = "client_secret is missing."
	q, binding = beginOAuth(t, a, cookie, csrf)
	w := callback(a, q, binding)
	if !strings.Contains(w.Header().Get("Location"), "google=configuration_required") {
		t.Fatal("missing private configuration was not diagnosed")
	}
	f.tokenError = ""
	a.google.cfg.ClientSecret = "fixture-private-client-secret"
	q, binding = beginOAuth(t, a, cookie, csrf)
	if w = callback(a, q, binding); !strings.Contains(w.Header().Get("Location"), "google=connected") || f.lastSecret == "" {
		t.Fatal("optional private desktop configuration was not used")
	}
	q, binding = beginSignIn(t, a, nil, "")
	f.sub = "different-owner"
	if w = callback(a, q, binding); !strings.Contains(w.Header().Get("Location"), "google=wrong_account") {
		t.Fatal("open Google self-registration allowed")
	}
	f.sub = "owner-sub"
	q, binding = beginSignIn(t, a, nil, "")
	if w = callback(a, q, binding); !strings.Contains(w.Header().Get("Location"), "google=signed_in") || f.creates != 1 {
		t.Fatal("returning owner did not reuse calendar")
	}
}
func TestOAuthPermissionDenialRefreshAndConnectionDiagnostics(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	f.scope = busyScope
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); !errors.Is(err, errScopes) {
		t.Fatal("declined write permission accepted")
	}
	f.scope = googleScopes
	f.verified = false
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err == nil {
		t.Fatal("unverified Google identity accepted")
	}
	f.verified = true
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	var tokens GoogleTokens
	if err := a.store.getSecret("google_tokens", &tokens); err != nil {
		t.Fatal(err)
	}
	tokens.Expiry = a.now().Add(-time.Hour)
	if err := a.store.putSecret("google_tokens", tokens); err != nil {
		t.Fatal(err)
	}
	if _, err := a.google.Busy(context.Background(), a.now(), a.now().Add(time.Hour)); err != nil || f.refreshCalls != 1 {
		t.Fatal("expired access token was not refreshed")
	}
	var saved GoogleTokens
	_ = a.store.getSecret("google_tokens", &saved)
	if saved.Refresh != "fixture-refresh" || saved.Access != "fixture-refreshed-access" {
		t.Fatal("refresh token was lost when Google omitted a replacement")
	}
	f.busyError = true
	if _, err := a.google.Busy(context.Background(), a.now(), a.now().Add(time.Hour)); err == nil || a.google.connection().Error == "" {
		t.Fatal("unavailable calendar did not produce owner diagnostic")
	}
	f.busyError = false
	if _, err := a.google.Busy(context.Background(), a.now(), a.now().Add(time.Hour)); err != nil || a.google.connection().Error != "" {
		t.Fatal("recovered Calendar did not clear diagnostic")
	}
	f.calendarMissing = true
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err == nil || f.creates != 1 {
		t.Fatal("missing calendar was silently replaced")
	}
}
func TestGoogleStableEventRetriesAndDisconnect(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	b, _, err := a.createBooking(context.Background(), testInput("google-stable-event-001"))
	if err != nil {
		t.Fatal(err)
	}
	f.loseInsert = true
	if err = a.google.Sync(context.Background(), b); err == nil {
		t.Fatal("lost insert response was not surfaced")
	}
	if err = a.google.Sync(context.Background(), b); err != nil || f.inserts != 1 {
		t.Fatal("retry duplicated remote appointment")
	}
	f.mu.Lock()
	event := f.events[b.EventID]
	f.mu.Unlock()
	if event["summary"] != "Service appointment · Lawn care · Sample Customer" || event["location"] != b.Address || event["visibility"] != "private" {
		t.Fatal("calendar event did not identify the private service job and property")
	}
	description, _ := event["description"].(string)
	for _, detail := range []string{"service appointment request", "Service: Lawn care", b.Name, b.Phone, b.Email, b.Address, b.Notes} {
		if !strings.Contains(description, detail) {
			t.Fatalf("calendar event omitted job detail %q", detail)
		}
	}
	for _, label := range []string{"start", "end"} {
		value, _ := event[label].(map[string]any)
		dateTime, _ := value["dateTime"].(string)
		got, parseErr := time.Parse(time.RFC3339, dateTime)
		want := b.Start
		if label == "end" {
			want = b.End
		}
		if parseErr != nil || !got.Equal(want) {
			t.Fatalf("calendar event changed the requested job %s time", label)
		}
	}
	b.Status = "cancelled"
	f.loseDelete = true
	if err = a.google.Sync(context.Background(), b); err == nil {
		t.Fatal("lost deletion response was not surfaced")
	}
	if err = a.google.Sync(context.Background(), b); err != nil || f.deletes != 1 {
		t.Fatal("repeated cancellation failed")
	}
	if err = a.google.disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner, e := a.google.owner()
	if e != nil || owner.Sub == "" || owner.CalendarID == "" || a.google.Connected() {
		t.Fatal("disconnect lost owner binding or retained access")
	}
}
