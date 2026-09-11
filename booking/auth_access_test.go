package main

import (
	"bytes"
	"context"
	"database/sql"
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

func testIdentitySession(t *testing.T, a *App, identity Owner) (*http.Cookie, string) {
	t.Helper()
	role := a.identityRole(identity.Sub, identity.Email)
	if role == "" {
		t.Fatal("fixture identity is not authorized")
	}
	w := httptest.NewRecorder()
	session, err := a.newSession(w, Session{Subject: identity.Sub, Email: identity.Email, Role: role})
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == a.cookieName() {
			return cookie, session.CSRF
		}
	}
	t.Fatal("missing fixture session cookie")
	return nil, ""
}

func configureTestOperator(t *testing.T, a *App) (*http.Cookie, string) {
	t.Helper()
	if err := a.store.importOperatorIdentity(OperatorIdentity{Schema: 1, GoogleSub: "operator-sub", Email: "operator@example.com"}); err != nil {
		t.Fatal(err)
	}
	return testIdentitySession(t, a, Owner{Sub: "operator-sub", Email: "operator@example.com"})
}
func createTestInvitation(t *testing.T, a *App, cookie *http.Cookie, csrf, email string) (Invitation, string) {
	t.Helper()
	w := request(a, true, "POST", "/api/admin/invitations", map[string]string{"email": email}, cookie, csrf, a.cfg.AdminOrigin)
	var body struct {
		Invitation Invitation `json:"invitation"`
		URL        string     `json:"url"`
	}
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("invitation creation: %d %s", w.Code, w.Body.String())
	}
	u, err := url.Parse(body.URL)
	if err != nil || !strings.HasPrefix(u.Fragment, "invite=") {
		t.Fatal("invitation not in URL fragment")
	}
	return body.Invitation, strings.TrimPrefix(u.Fragment, "invite=")
}
func sessionCookie(t *testing.T, a *App, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == a.cookieName() && c.MaxAge > 0 {
			return c
		}
	}
	t.Fatal("callback did not issue identified session")
	return nil
}
func TestIdentityOnlyOperatorSignInPreservesCalendar(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	configureTestOperator(t, a)
	beforeOwner, _ := a.google.owner()
	var beforeTokens GoogleTokens
	_ = a.store.getSecret("google_tokens", &beforeTokens)
	f.sub, f.email = "operator-sub", "operator@example.com"
	q, binding := beginSignIn(t, a, nil, "")
	if q.Get("scope") != "openid email" || q.Get("access_type") == "offline" || q.Get("prompt") == "consent" {
		t.Fatal("sign-in requested Calendar access or repeated consent")
	}
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=signed_in") {
		t.Fatal("operator identity sign-in failed")
	}
	cookie := sessionCookie(t, a, w)
	status := request(a, true, "GET", "/api/admin/session", nil, cookie, "", "")
	var body struct {
		Role               string `json:"role"`
		ActorEmail         string `json:"actorEmail"`
		CanManageAccess    bool   `json:"canManageAccess"`
		CanConnectCalendar bool   `json:"canConnectCalendar"`
		CSRF               string `json:"csrfToken"`
	}
	_ = json.Unmarshal(status.Body.Bytes(), &body)
	if body.Role != "operator" || body.ActorEmail != "operator@example.com" || !body.CanManageAccess || body.CanConnectCalendar {
		t.Fatal("actor/Calendar permissions were conflated")
	}
	for _, path := range []string{"/api/admin/google/connect", "/api/admin/google/disconnect"} {
		if w = request(a, true, "POST", path, map[string]string{}, cookie, body.CSRF, a.cfg.AdminOrigin); w.Code != 403 {
			t.Fatal("operator could replace owner's Calendar")
		}
	}
	afterOwner, _ := a.google.owner()
	var afterTokens GoogleTokens
	_ = a.store.getSecret("google_tokens", &afterTokens)
	if beforeOwner != afterOwner || beforeTokens != afterTokens || f.creates != 1 {
		t.Fatal("identity sign-in changed Calendar data")
	}
	if w = callback(a, q, binding); !strings.HasSuffix(w.Header().Get("Location"), "google=failed") {
		t.Fatal("sign-in state replay succeeded")
	}
	if w = request(a, true, "GET", "/api/admin/settings", nil, cookie, "", ""); w.Code != 200 {
		t.Fatal("operator lost ordinary admin access")
	}
}
func TestOperatorEmailPolicyPinsOnlyVerifiedIdentity(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	if err := a.store.importOperatorIdentity(OperatorIdentity{Schema: 1, Email: "Owner@Example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"wrong_email", "unverified"} {
		f.email, f.verified = "owner@example.com", true
		if change == "wrong_email" {
			f.email = "other@example.com"
		} else {
			f.verified = false
		}
		q, binding := beginSignIn(t, a, nil, "")
		w := callback(a, q, binding)
		if strings.HasSuffix(w.Header().Get("Location"), "google=signed_in") {
			t.Fatal("unverified or unrelated identity gained operator")
		}
		policy, _ := a.store.operatorIdentity()
		if policy.GoogleSub != "" {
			t.Fatal("failed sign-in pinned policy")
		}
	}
	f.email, f.verified = "owner@example.com", true
	q, binding := beginSignIn(t, a, nil, "")
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=signed_in") {
		t.Fatal("configured email could not establish operator")
	}
	policy, _ := a.store.operatorIdentity()
	if policy.GoogleSub != "owner-sub" {
		t.Fatal("verified subject was not pinned")
	}
	if err := a.store.importOperatorIdentity(OperatorIdentity{Schema: 1, Email: "owner@example.com"}); err != nil {
		t.Fatal(err)
	}
	policy, _ = a.store.operatorIdentity()
	if policy.GoogleSub != "owner-sub" {
		t.Fatal("update unpinned established operator")
	}
	f.sub = "different-sub"
	q, binding = beginSignIn(t, a, nil, "")
	w = callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=wrong_account") {
		t.Fatal("same-email different subject replaced operator")
	}
	if _, err := a.google.owner(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("sign-in silently claimed Calendar ownership")
	}
}
func TestOperatorPolicyConcurrentPinAndImportPersistence(t *testing.T) {
	a, _ := testApp(t)
	if err := a.store.importOperatorIdentity(OperatorIdentity{Schema: 1, Email: "operator@example.com"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, sub := range []string{"first-sub", "second-sub"} {
		wg.Add(1)
		go func(sub string) {
			defer wg.Done()
			results <- a.store.bindOperatorIdentity(Owner{Sub: sub, Email: "operator@example.com"})
		}(sub)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("concurrent sign-ins did not pin exactly one subject")
	}
	policy, _ := a.store.operatorIdentity()
	if err := a.store.importOperatorIdentity(OperatorIdentity{Schema: 1, GoogleSub: "third-sub", Email: policy.Email}); err == nil {
		t.Fatal("conflicting imported identity overwrote operator")
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	saved, _ := other.store.operatorIdentity()
	if saved != policy || other.bootstrapAllowed() {
		t.Fatal("operator pin or consumed bootstrap did not persist")
	}
}
func TestInvitationSignupAndRoleIsolation(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	operatorCookie, csrf := configureTestOperator(t, a)
	invite, token := createTestInvitation(t, a, operatorCookie, csrf, "OWNER@example.com")
	list := request(a, true, "GET", "/api/admin/invitations", nil, operatorCookie, "", "")
	if strings.Contains(list.Body.String(), token) || !strings.Contains(list.Body.String(), invite.ID) {
		t.Fatal("invitation list exposed bearer token or lost invitation")
	}
	q, binding := beginAuthRequest(t, a, "/api/admin/invitations/accept", map[string]string{"token": token}, nil, "")
	if q.Get("scope") != "openid email" {
		t.Fatal("invitation acceptance requested Calendar consent")
	}
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=invited") {
		t.Fatalf("claim failed: %s", w.Header().Get("Location"))
	}
	ownerCookie := sessionCookie(t, a, w)
	if _, err := a.google.owner(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("invitation assigned a Calendar owner before explicit connection")
	}
	if a.identityRole(f.sub, f.email) != "owner" {
		t.Fatal("invitation did not grant workspace access")
	}
	if a.google.connection().Connected {
		t.Fatal("invitation saved an identity token as Calendar credentials")
	}
	w = request(a, true, "GET", "/api/admin/session", nil, ownerCookie, "", "")
	var body struct {
		Role               string `json:"role"`
		CanManageAccess    bool   `json:"canManageAccess"`
		CanConnectCalendar bool   `json:"canConnectCalendar"`
		CSRF               string `json:"csrfToken"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Role != "owner" || body.CanManageAccess || !body.CanConnectCalendar {
		t.Fatal("owner permissions incorrect")
	}
	if w = request(a, true, "POST", "/api/admin/invitations", map[string]string{"email": "other@example.com"}, ownerCookie, body.CSRF, a.cfg.AdminOrigin); w.Code != 403 {
		t.Fatal("owner gained operator invitation permission")
	}
	if w = request(a, true, "GET", "/api/admin/invitations", nil, ownerCookie, "", ""); w.Code != 403 {
		t.Fatal("owner read private invitation access list")
	}
	if w = request(a, true, "POST", "/api/admin/invitations/accept", map[string]string{"token": token}, nil, "", a.cfg.AdminOrigin); w.Code != 409 {
		t.Fatal("single-use invitation could be replayed")
	}
	q, binding = beginSignIn(t, a, nil, "")
	w = callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=signed_in") {
		t.Fatal("invited owner could not sign in normally")
	}
}
func TestInvitationPendingOAuthRevocationExpiryAndWrongAccount(t *testing.T) {
	for _, scenario := range []string{"revoked", "expired", "wrong_account", "operator_changed"} {
		t.Run(scenario, func(t *testing.T) {
			a, _ := testApp(t)
			f := mockGoogle(t, a)
			cookie, csrf := configureTestOperator(t, a)
			invite, token := createTestInvitation(t, a, cookie, csrf, "owner@example.com")
			q, binding := beginAuthRequest(t, a, "/api/admin/invitations/accept", map[string]string{"token": token}, nil, "")
			outcome := "invitation_" + scenario
			switch scenario {
			case "revoked":
				if w := request(a, true, "DELETE", "/api/admin/invitations/"+invite.ID, nil, cookie, csrf, a.cfg.AdminOrigin); w.Code != 204 {
					t.Fatal("revoke failed")
				}
			case "expired":
				_, _ = a.store.db.Exec("UPDATE invitations SET expires=0 WHERE id=?", invite.ID)
			case "wrong_account":
				f.email = "stranger@example.com"
				outcome = "wrong_account"
			case "operator_changed":
				_ = a.store.putSecret("operator_identity", OperatorIdentity{Schema: 1, GoogleSub: "new-operator", Email: "new@example.com"})
				outcome = "invitation_revoked"
			}
			w := callback(a, q, binding)
			if !strings.HasSuffix(w.Header().Get("Location"), "google="+outcome) {
				t.Fatalf("unsafe pending OAuth outcome %s", w.Header().Get("Location"))
			}
			if _, err := a.google.owner(); !errors.Is(err, sql.ErrNoRows) {
				t.Fatal("failed invitation claimed ownership")
			}
		})
	}
}
func TestInvitationConcurrentClaimAndReplacement(t *testing.T) {
	a, _ := testApp(t)
	cookie, csrf := configureTestOperator(t, a)
	first, oldToken := createTestInvitation(t, a, cookie, csrf, "owner@example.com")
	second, _ := createTestInvitation(t, a, cookie, csrf, "another@example.com")
	invite, _ := createTestInvitation(t, a, cookie, csrf, "owner@example.com")
	if w := request(a, true, "POST", "/api/admin/invitations/accept", map[string]string{"token": oldToken}, nil, "", a.cfg.AdminOrigin); w.Code != 410 {
		t.Fatal("replacement left old token usable")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- a.claimInvitation(invite.ID, Owner{Sub: "owner-sub", Email: "owner@example.com"})
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("invitation consumed more than once")
	}
	for id, want := range map[string]string{first.ID: "revoked", second.ID: "pending"} {
		var status string
		_ = a.store.db.QueryRow("SELECT status FROM invitations WHERE id=?", id).Scan(&status)
		if status != want {
			t.Fatal("per-email replacement changed an unrelated invitation")
		}
	}
}
func TestInvitationCSRFOriginForgeryAndCalendarPreservation(t *testing.T) {
	a, _ := testApp(t)
	cookie, csrf := configureTestOperator(t, a)
	for _, test := range []struct {
		cookie       *http.Cookie
		csrf, origin string
		want         int
	}{{nil, "", a.cfg.AdminOrigin, 401}, {cookie, "", a.cfg.AdminOrigin, 403}, {cookie, csrf, "https://attacker.invalid", 403}} {
		if w := request(a, true, "POST", "/api/admin/invitations", map[string]string{"email": "owner@example.com"}, test.cookie, test.csrf, test.origin); w.Code != test.want {
			t.Fatal("access boundary accepted invitation creation")
		}
	}
	if w := request(a, true, "POST", "/api/admin/invitations/accept", map[string]string{"token": randomToken(32)}, nil, "", a.cfg.AdminOrigin); w.Code != 400 {
		t.Fatal("forged invitation accepted")
	}
	invite, _ := createTestInvitation(t, a, cookie, csrf, "owner@example.com")
	_ = a.store.putSecret("owner", Owner{Sub: "another-owner", Email: "another@example.com", CalendarID: "keep-calendar"})
	_ = a.store.putSecret("google_tokens", GoogleTokens{Refresh: "keep-refresh"})
	if err := a.claimInvitation(invite.ID, Owner{Sub: "owner-sub", Email: "owner@example.com"}); err != nil {
		t.Fatalf("new workspace member could not join: %v", err)
	}
	if w := request(a, true, "POST", "/api/admin/invitations", map[string]string{"email": "owner@example.com"}, cookie, csrf, a.cfg.AdminOrigin); w.Code != 409 {
		t.Fatal("active workspace member received a duplicate invitation")
	}
	owner, _ := a.google.owner()
	var tokens GoogleTokens
	_ = a.store.getSecret("google_tokens", &tokens)
	if owner.CalendarID != "keep-calendar" || tokens.Refresh != "keep-refresh" {
		t.Fatal("new member changed existing Calendar credentials")
	}
}
func TestLegacySessionsBootstrapAndOwnerMigration(t *testing.T) {
	a, _ := testApp(t)
	legacyCookie, _ := bootstrap(t, a)
	id := a.store.hash(legacyCookie.Value)
	raw, _ := json.Marshal(map[string]any{"csrf": "old-csrf", "expires": a.now().Add(time.Hour).Unix()})
	encrypted, _ := a.store.seal(raw, "session:"+id)
	_, _ = a.store.db.Exec("UPDATE sessions SET value=? WHERE id=?", encrypted, id)
	if w := request(a, true, "GET", "/api/admin/settings", nil, legacyCookie, "", ""); w.Code != 401 {
		t.Fatal("unidentified legacy session retained access")
	}
	_ = a.store.putSecret("owner", Owner{Sub: "owner-sub", Email: "owner@example.com", CalendarID: "existing-calendar"})
	tokens := GoogleTokens{Refresh: "persistent-refresh", Access: "persistent-access", Expiry: a.now().Add(time.Hour), Scope: googleScopes}
	_ = a.store.putSecret("google_tokens", tokens)
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	if _, err = other.store.operatorIdentity(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("legacy owner was silently promoted to operator")
	}
	if w := request(other, true, "POST", "/api/admin/bootstrap", map[string]string{"token": other.cfg.BootstrapToken}, nil, "", other.cfg.AdminOrigin); w.Code != 401 {
		t.Fatal("old setup token bypassed owner login")
	}
	var saved GoogleTokens
	_ = other.store.getSecret("google_tokens", &saved)
	if saved != tokens {
		t.Fatal("migration changed personal tokens")
	}
	_, _ = other.store.db.Exec("DELETE FROM secrets WHERE key IN ('owner','google_tokens')")
	if other.bootstrapAllowed() {
		t.Fatal("consumed bootstrap became reusable when owner was removed")
	}
}
func TestBootstrapIdentitySetupAndCalendarActorBinding(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	cookie, csrf := bootstrap(t, a)
	q, binding := beginSignIn(t, a, cookie, csrf)
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=signed_in") {
		t.Fatal("bootstrap could not establish operator")
	}
	policy, _ := a.store.operatorIdentity()
	if policy.GoogleSub != "owner-sub" {
		t.Fatal("first setup did not establish identified operator")
	}
	if w = request(a, true, "GET", "/api/admin/settings", nil, cookie, "", ""); w.Code != 401 {
		t.Fatal("bootstrap session survived identity establishment")
	}
	operatorCookie, operatorCSRF := testIdentitySession(t, a, Owner{Sub: f.sub, Email: f.email})
	q, binding = beginOAuth(t, a, operatorCookie, operatorCSRF)
	f.sub = "different-sub"
	if w = callback(a, q, binding); !strings.HasSuffix(w.Header().Get("Location"), "google=wrong_account") {
		t.Fatal("Calendar consent changed authenticated actor")
	}
	if _, err := a.google.owner(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("wrong Calendar actor changed owner")
	}
	f.sub = policy.GoogleSub
	q, binding = beginOAuth(t, a, operatorCookie, operatorCSRF)
	w = callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=connected") {
		t.Fatal("operator could not connect own first Calendar")
	}
}

type authRoundTrip func(*http.Request) (*http.Response, error)

func (f authRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPendingBootstrapCalendarCannotOutliveOperatorEstablishment(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	cookie, csrf := bootstrap(t, a)
	q, binding := beginOAuth(t, a, cookie, csrf)
	entered, release := make(chan struct{}), make(chan struct{})
	a.google.client.Transport = authRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/userinfo" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- callback(a, q, binding) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Google identity exchange did not start")
	}
	err := a.establishOperator(Owner{Sub: "operator-sub", Email: "operator@example.com"})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	w := <-done
	if !strings.HasSuffix(w.Header().Get("Location"), "google=wrong_account") || f.creates != 0 {
		t.Fatal("consumed bootstrap OAuth created a Calendar after operator establishment")
	}
	if _, err = a.google.owner(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("stale setup flow bound owner")
	}
}

func TestCalendarReconnectRevalidatesLogoutAtCommit(t *testing.T) {
	a, _ := testApp(t)
	mockGoogle(t, a)
	if err := a.google.connect(context.Background(), "code", randomToken(48), a.cfg.AdminOrigin+"/oauth/callback"); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := testIdentitySession(t, a, Owner{Sub: "owner-sub", Email: "owner@example.com"})
	var before []byte
	_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_tokens'").Scan(&before)
	q, binding := beginOAuth(t, a, cookie, csrf)
	entered, release := make(chan struct{}), make(chan struct{})
	a.google.client.Transport = authRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" && r.URL.Path == "/calendar/v3/calendars/fixture-booking-calendar" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- callback(a, q, binding) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Calendar validation did not begin")
	}
	logout := request(a, true, "POST", "/api/admin/logout", nil, cookie, csrf, a.cfg.AdminOrigin)
	close(release)
	if logout.Code != 204 {
		t.Fatal("logout failed")
	}
	w := <-done
	if !strings.HasSuffix(w.Header().Get("Location"), "google=wrong_account") {
		t.Fatal("logged-out in-flight Calendar authorization committed")
	}
	var after []byte
	_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_tokens'").Scan(&after)
	if !bytes.Equal(before, after) {
		t.Fatal("revoked reconnection rewrote Calendar tokens")
	}
}
func TestBootstrapCalendarRevalidatesExternalPolicyImportAtCommit(t *testing.T) {
	a, _ := testApp(t)
	mockGoogle(t, a)
	cookie, csrf := bootstrap(t, a)
	q, binding := beginOAuth(t, a, cookie, csrf)
	entered, release := make(chan struct{}), make(chan struct{})
	a.google.client.Transport = authRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && r.URL.Path == "/calendar/v3/calendars" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- callback(a, q, binding) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Calendar creation did not begin")
	}
	err := a.store.importOperatorIdentity(OperatorIdentity{Schema: 1, Email: "operator@example.com"})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	w := <-done
	if !strings.HasSuffix(w.Header().Get("Location"), "google=wrong_account") {
		t.Fatal("bootstrap committed after concurrent operator import")
	}
	if _, err = a.google.owner(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("consumed bootstrap bound first calendar owner")
	}
}
