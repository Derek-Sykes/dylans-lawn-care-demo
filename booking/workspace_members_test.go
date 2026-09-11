package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func joinTestMember(t *testing.T, a *App, operator *http.Cookie, csrf string, identity Owner) (*http.Cookie, string) {
	t.Helper()
	v, _ := createTestInvitation(t, a, operator, csrf, identity.Email)
	if err := a.claimInvitation(v.ID, identity); err != nil {
		t.Fatal(err)
	}
	return testIdentitySession(t, a, identity)
}

func listTestMembers(t *testing.T, a *App, cookie *http.Cookie) []WorkspaceMember {
	t.Helper()
	w := request(a, true, "GET", "/api/admin/members", nil, cookie, "", "")
	var out struct {
		Members []WorkspaceMember `json:"members"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("member list: %d %s", w.Code, w.Body.String())
	}
	return out.Members
}

func testMemberID(t *testing.T, a *App, sub string) string {
	t.Helper()
	var id string
	if err := a.store.db.QueryRow("SELECT id FROM workspace_members WHERE google_sub=?", sub).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestThreeAccountsShareOneWorkspaceAndPreserveCalendar(t *testing.T) {
	a, _ := testApp(t)
	opCookie, opCSRF := configureTestOperator(t, a)
	calendarOwner := Owner{Sub: "operator-sub", Email: "operator@example.com", CalendarID: "existing-calendar"}
	if err := a.store.putSecret("owner", calendarOwner); err != nil {
		t.Fatal(err)
	}
	if err := a.store.putSecret("google_tokens", GoogleTokens{Refresh: "keep-refresh"}); err != nil {
		t.Fatal(err)
	}
	var beforeOwner, beforeTokens []byte
	_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='owner'").Scan(&beforeOwner)
	_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_tokens'").Scan(&beforeTokens)
	friendCookie, friendCSRF := joinTestMember(t, a, opCookie, opCSRF, Owner{Sub: "friend-sub", Email: "Friend@example.com"})
	ownerCookie, ownerCSRF := joinTestMember(t, a, opCookie, opCSRF, Owner{Sub: "dylan-sub", Email: "dylan@example.com"})
	b, _, err := a.createBooking(context.Background(), testInput("shared-service-request"))
	if err != nil {
		t.Fatal(err)
	}
	estimate := testInput("shared-estimate-request")
	estimate.Kind, estimate.Start = "estimate", "2026-09-15T13:00:00Z"
	e, _, err := a.createBooking(context.Background(), estimate)
	if err != nil {
		t.Fatal(err)
	}
	w := request(a, true, "PATCH", "/api/admin/bookings/"+b.ID, map[string]string{"status": "contacted", "adminNotes": "Shared follow-up"}, friendCookie, friendCSRF, a.cfg.AdminOrigin)
	if w.Code != 200 {
		t.Fatalf("member cannot update work: %d %s", w.Code, w.Body.String())
	}
	settings, _ := a.store.settings()
	settings.EstimateMinutes = 30
	w = request(a, true, "PUT", "/api/admin/settings", settings, ownerCookie, ownerCSRF, a.cfg.AdminOrigin)
	if w.Code != 200 {
		t.Fatalf("member cannot update shared schedule: %d", w.Code)
	}
	for _, cookie := range []*http.Cookie{opCookie, friendCookie, ownerCookie} {
		w = request(a, true, "GET", "/api/admin/bookings", nil, cookie, "", "")
		for _, value := range []string{b.ID, e.ID, "Shared follow-up", "contacted"} {
			if w.Code != 200 || !strings.Contains(w.Body.String(), value) {
				t.Fatal("accounts did not see the same work")
			}
		}
		w = request(a, true, "GET", "/api/admin/settings", nil, cookie, "", "")
		var saved Settings
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &saved) != nil || saved.EstimateMinutes != 30 {
			t.Fatal("accounts did not see the same settings")
		}
	}
	for _, actor := range []struct {
		cookie *http.Cookie
		csrf   string
	}{{friendCookie, friendCSRF}, {ownerCookie, ownerCSRF}} {
		for _, path := range []string{"/api/admin/google/connect", "/api/admin/google/disconnect", "/api/admin/invitations"} {
			w = request(a, true, "POST", path, map[string]string{"email": "other@example.com"}, actor.cookie, actor.csrf, a.cfg.AdminOrigin)
			if w.Code != 403 {
				t.Fatalf("member gained protected access: %s %d", path, w.Code)
			}
		}
		for _, path := range []string{"/api/admin/members", "/api/admin/invitations"} {
			if w = request(a, true, "GET", path, nil, actor.cookie, "", ""); w.Code != 403 {
				t.Fatal("member could manage access")
			}
		}
	}
	items := listTestMembers(t, a, opCookie)
	if len(items) != 3 || items[0].Role != "operator" || !items[0].CalendarOwner || items[0].CanRemove {
		t.Fatalf("protected operator was not deduplicated: %+v", items)
	}
	var afterOwner, afterTokens []byte
	_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='owner'").Scan(&afterOwner)
	_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_tokens'").Scan(&afterTokens)
	if !bytes.Equal(beforeOwner, afterOwner) || !bytes.Equal(beforeTokens, afterTokens) {
		t.Fatal("sharing rewrote Calendar identity or credentials")
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	for _, cookie := range []*http.Cookie{opCookie, friendCookie, ownerCookie} {
		if w = request(other, true, "GET", "/api/admin/settings", nil, cookie, "", ""); w.Code != 200 {
			t.Fatal("member session did not survive reopen")
		}
	}
}

func TestMemberRemovalInvalidatesSessionsWithoutResurrection(t *testing.T) {
	a, _ := testApp(t)
	op, csrf := configureTestOperator(t, a)
	identity := Owner{Sub: "friend-sub", Email: "friend@example.com"}
	cookie, memberCSRF := joinTestMember(t, a, op, csrf, identity)
	secondCookie, _ := testIdentitySession(t, a, identity)
	id := testMemberID(t, a, identity.Sub)
	for _, tc := range []struct {
		cookie       *http.Cookie
		csrf, origin string
		want         int
	}{{nil, "", a.cfg.AdminOrigin, 401}, {cookie, memberCSRF, a.cfg.AdminOrigin, 403}, {op, "", a.cfg.AdminOrigin, 403}, {op, csrf, "https://attacker.invalid", 403}} {
		if w := request(a, true, "DELETE", "/api/admin/members/"+id, nil, tc.cookie, tc.csrf, tc.origin); w.Code != tc.want {
			t.Fatalf("removal boundary: %d want%d", w.Code, tc.want)
		}
	}
	if w := request(a, true, "DELETE", "/api/admin/members/"+id, nil, op, csrf, a.cfg.AdminOrigin); w.Code != 204 {
		t.Fatal("operator could not remove member")
	}
	if a.identityRole(identity.Sub, identity.Email) != "" {
		t.Fatal("removed identity retained access")
	}
	newCookie, _ := joinTestMember(t, a, op, csrf, identity)
	for _, old := range []*http.Cookie{cookie, secondCookie} {
		if w := request(a, true, "GET", "/api/admin/settings", nil, old, "", ""); w.Code != 401 {
			t.Fatal("reinvitation resurrected old browser session")
		}
	}
	if w := request(a, true, "GET", "/api/admin/settings", nil, newCookie, "", ""); w.Code != 200 {
		t.Fatal("new invitation did not grant a new session")
	}
	if w := request(a, true, "POST", "/api/admin/invitations", map[string]string{"email": "FRIEND@example.com"}, op, csrf, a.cfg.AdminOrigin); w.Code != 409 || !strings.Contains(w.Body.String(), "member_already_exists") {
		t.Fatal("duplicate active member was not explained")
	}
	if w := request(a, true, "DELETE", "/api/admin/members/"+id, nil, op, csrf, a.cfg.AdminOrigin); w.Code != 204 {
		t.Fatal("second removal failed")
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	if other.identityRole(identity.Sub, identity.Email) != "" {
		t.Fatal("restart resurrected revoked membership")
	}
	invite, _ := createTestInvitation(t, other, op, csrf, identity.Email)
	if err = other.claimInvitation(invite.ID, Owner{Sub: "different-google-sub", Email: identity.Email}); !errors.Is(err, errWrongOwner) {
		t.Fatal("same email changed pinned Google identity")
	}
}

func TestIndependentInvitationClaimsAndSameEmailReplacement(t *testing.T) {
	a, _ := testApp(t)
	op, csrf := configureTestOperator(t, a)
	first, _ := createTestInvitation(t, a, op, csrf, "first@example.com")
	second, _ := createTestInvitation(t, a, op, csrf, "second@example.com")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, item := range []struct {
		invitation Invitation
		sub        string
	}{{first, "first-sub"}, {second, "second-sub"}} {
		wg.Add(1)
		go func(v Invitation, sub string) {
			defer wg.Done()
			results <- a.claimInvitation(v.ID, Owner{Sub: sub, Email: v.Email})
		}(item.invitation, item.sub)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.google.owner(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("concurrent invitations assigned a Calendar")
	}
	if len(listTestMembers(t, a, op)) != 3 {
		t.Fatal("independent invitations did not both join")
	}
	for _, identity := range []Owner{{Sub: "first-sub", Email: "first@example.com"}, {Sub: "second-sub", Email: "second@example.com"}} {
		cookie, _ := testIdentitySession(t, a, identity)
		s, err := a.sessionByID(a.store.hash(cookie.Value))
		if err != nil || !a.canConnectCalendar(s) {
			t.Fatal("unassigned Calendar could not be explicitly connected by a member")
		}
	}
}

func TestInvitationOnlyLegacyOwnerMigratesWithoutClaimingCalendar(t *testing.T) {
	a, _ := testApp(t)
	op, csrf := configureTestOperator(t, a)
	legacy := Owner{Sub: "legacy-sub", Email: "legacy@example.com"}
	if err := a.store.putSecret("owner", legacy); err != nil {
		t.Fatal(err)
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	if _, err = other.google.owner(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("invite-only legacy record retained Calendar assignment")
	}
	if other.identityRole(legacy.Sub, legacy.Email) != "owner" || other.bootstrapAllowed() {
		t.Fatal("migration lost legacy access or enabled bootstrap")
	}
	id := testMemberID(t, other, legacy.Sub)
	if w := request(other, true, "DELETE", "/api/admin/members/"+id, nil, op, csrf, other.cfg.AdminOrigin); w.Code != 204 {
		t.Fatal("unconnected migrated member cannot be removed")
	}
	third, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer third.store.close()
	if third.identityRole(legacy.Sub, legacy.Email) != "" {
		t.Fatal("migration resurrected removed legacy member")
	}
}

func TestLegacyMemberCookieCannotResurrectAfterRejoinAndCalendarConnection(t *testing.T) {
	a, _ := testApp(t)
	op, csrf := configureTestOperator(t, a)
	legacy := Owner{Sub: "owner-sub", Email: "owner@example.com"}
	if err := a.store.putSecret("owner", legacy); err != nil {
		t.Fatal(err)
	}
	oldCookie, _ := testIdentitySession(t, a, legacy)
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	other.now = a.now
	mockGoogle(t, other)
	if w := request(other, true, "DELETE", "/api/admin/members/"+testMemberID(t, other, legacy.Sub), nil, op, csrf, other.cfg.AdminOrigin); w.Code != 204 {
		t.Fatal("migrated identity could not be removed")
	}
	cookie, memberCSRF := joinTestMember(t, other, op, csrf, legacy)
	secondCookie, _ := testIdentitySession(t, other, legacy)
	q, binding := beginOAuth(t, other, cookie, memberCSRF)
	w := callback(other, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=connected") {
		t.Fatal("rejoined member could not explicitly connect")
	}
	if w = request(other, true, "GET", "/api/admin/settings", nil, oldCookie, "", ""); w.Code != 401 {
		t.Fatal("Calendar assignment resurrected removed legacy cookie")
	}
	if w = request(other, true, "GET", "/api/admin/settings", nil, secondCookie, "", ""); w.Code != 200 {
		t.Fatal("Calendar assignment unnecessarily invalidated a current member session")
	}
}

func TestSharedWorkspaceUpgradePreservesExistingCalendarOwnerSession(t *testing.T) {
	a, _ := testApp(t)
	op, _ := configureTestOperator(t, a)
	owner := Owner{Sub: "legacy-calendar-sub", Email: "legacy-calendar@example.com", CalendarID: "existing-calendar"}
	if err := a.store.putSecret("owner", owner); err != nil {
		t.Fatal(err)
	}
	if err := a.store.putSecret("google_tokens", GoogleTokens{Refresh: "existing-refresh"}); err != nil {
		t.Fatal(err)
	}
	cookie, _ := testIdentitySession(t, a, owner)
	var before []byte
	_ = a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_tokens'").Scan(&before)
	// Recreate the previous release's schema, which has no members table.
	if _, err := a.store.db.Exec("DROP TABLE workspace_members"); err != nil {
		t.Fatal(err)
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	for _, c := range []*http.Cookie{op, cookie} {
		if w := request(other, true, "GET", "/api/admin/settings", nil, c, "", ""); w.Code != 200 {
			t.Fatal("upgrade invalidated an existing operator or Calendar-owner session")
		}
	}
	items := listTestMembers(t, other, op)
	if len(items) != 2 || items[1].Email != owner.Email || !items[1].CalendarOwner || items[1].CanRemove {
		t.Fatal("legacy Calendar identity was not preserved as a protected member")
	}
	var after []byte
	_ = other.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_tokens'").Scan(&after)
	if !bytes.Equal(before, after) {
		t.Fatal("upgrade rewrote existing Calendar credentials")
	}
}

func TestExplicitCalendarConnectionChoosesMemberAndProtectsThem(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	op, csrf := configureTestOperator(t, a)
	first := Owner{Sub: "first-sub", Email: "first@example.com"}
	firstCookie, firstCSRF := joinTestMember(t, a, op, csrf, first)
	chosen := Owner{Sub: "owner-sub", Email: "owner@example.com"}
	chosenCookie, chosenCSRF := joinTestMember(t, a, op, csrf, chosen)
	// A member who joined second explicitly chooses their Calendar; invitation
	// order must not reserve Calendar ownership for the first invitee.
	q, binding := beginOAuth(t, a, chosenCookie, chosenCSRF)
	w := callback(a, q, binding)
	if !strings.HasSuffix(w.Header().Get("Location"), "google=connected") {
		t.Fatalf("explicit member connection failed: %s", w.Header().Get("Location"))
	}
	owner, err := a.google.owner()
	if err != nil || owner.Sub != chosen.Sub || owner.CalendarID == "" || f.creates != 1 {
		t.Fatal("Calendar was not assigned by explicit consent")
	}
	chosenCookie = sessionCookie(t, a, w)
	if w = request(a, true, "GET", "/api/admin/settings", nil, chosenCookie, "", ""); w.Code != 200 {
		t.Fatal("new Calendar owner lost shared workspace")
	}
	if w = request(a, true, "POST", "/api/admin/google/connect", nil, firstCookie, firstCSRF, a.cfg.AdminOrigin); w.Code != 403 {
		t.Fatal("another member could replace assigned Calendar")
	}
	id := testMemberID(t, a, chosen.Sub)
	for _, target := range []string{id, "calendar-owner", "operator"} {
		if w = request(a, true, "DELETE", "/api/admin/members/"+target, nil, op, csrf, a.cfg.AdminOrigin); w.Code != 409 || !strings.Contains(w.Body.String(), "member_protected") {
			t.Fatal("protected Calendar identity or operator could be removed")
		}
	}
	items := listTestMembers(t, a, op)
	if len(items) != 3 {
		t.Fatal("Calendar owner membership duplicated")
	}
	for _, item := range items {
		if item.Email == chosen.Email && (!item.CalendarOwner || item.CanRemove) {
			t.Fatal("Calendar owner not shown protected")
		}
	}
	if err = a.google.disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	owner, err = other.google.owner()
	if err != nil || owner.Sub != chosen.Sub || owner.CalendarID == "" {
		t.Fatal("disconnect/restart unassigned established Calendar")
	}
}

func TestRemovedMemberPendingCalendarOAuthCannotConnect(t *testing.T) {
	a, _ := testApp(t)
	mockGoogle(t, a)
	op, csrf := configureTestOperator(t, a)
	identity := Owner{Sub: "owner-sub", Email: "owner@example.com"}
	cookie, memberCSRF := joinTestMember(t, a, op, csrf, identity)
	q, binding := beginOAuth(t, a, cookie, memberCSRF)
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
		t.Fatal("identity exchange did not begin")
	}
	w := request(a, true, "DELETE", "/api/admin/members/"+testMemberID(t, a, identity.Sub), nil, op, csrf, a.cfg.AdminOrigin)
	close(release)
	if w.Code != 204 {
		t.Fatal("member removal failed")
	}
	w = <-done
	if !strings.HasSuffix(w.Header().Get("Location"), "google=wrong_account") {
		t.Fatalf("removed member completed Calendar consent: %s", w.Header().Get("Location"))
	}
	if _, err := a.google.owner(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("revoked pending consent assigned Calendar")
	}
}

func TestFirstCalendarConnectionRevalidatesMemberAtFinalTransaction(t *testing.T) {
	a, _ := testApp(t)
	mockGoogle(t, a)
	op, csrf := configureTestOperator(t, a)
	identity := Owner{Sub: "owner-sub", Email: "owner@example.com"}
	cookie, memberCSRF := joinTestMember(t, a, op, csrf, identity)
	q, binding := beginOAuth(t, a, cookie, memberCSRF)
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
		t.Fatal("Calendar setup did not begin")
	}
	// A second process can update the persistent policy while this process is
	// waiting for Google. The commit must recheck the saved membership generation.
	_, err := a.store.db.Exec("UPDATE workspace_members SET status='revoked' WHERE google_sub=?", identity.Sub)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	w := <-done
	if !strings.HasSuffix(w.Header().Get("Location"), "google=wrong_account") {
		t.Fatal("revoked member committed first Calendar assignment")
	}
	if _, err = a.google.owner(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("failed final authorization assigned Calendar")
	}
	var tokens GoogleTokens
	if err = a.store.getSecret("google_tokens", &tokens); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("failed final authorization saved personal tokens")
	}
}

func TestCompetingExplicitCalendarConnectionsAssignOneIdentity(t *testing.T) {
	a, _ := testApp(t)
	f := mockGoogle(t, a)
	op, csrf := configureTestOperator(t, a)
	identities := []Owner{{Sub: "first-sub", Email: "first@example.com"}, {Sub: "second-sub", Email: "second@example.com"}}
	type attempt struct {
		values  url.Values
		binding *http.Cookie
	}
	attempts := []attempt{}
	for _, identity := range identities {
		cookie, memberCSRF := joinTestMember(t, a, op, csrf, identity)
		q, binding := beginOAuth(t, a, cookie, memberCSRF)
		attempts = append(attempts, attempt{url.Values{"state": {q.Get("state")}, "code": {identity.Sub}}, binding})
	}
	a.google.client.Transport = authRoundTrip(func(r *http.Request) (*http.Response, error) {
		var body any
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			body = map[string]any{"access_token": r.Form.Get("code"), "refresh_token": "fixture-refresh", "expires_in": 3600, "scope": googleScopes}
		} else if r.URL.Path == "/userinfo" {
			for _, identity := range identities {
				if r.Header.Get("Authorization") == "Bearer "+identity.Sub {
					body = map[string]any{"sub": identity.Sub, "email": identity.Email, "email_verified": true}
				}
			}
		}
		if body != nil {
			b, _ := json.Marshal(body)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b))}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, item := range attempts {
		wg.Add(1)
		go func(v attempt) {
			defer wg.Done()
			results <- request(a, true, "GET", "/oauth/callback?"+v.values.Encode(), nil, v.binding, "", "")
		}(item)
	}
	wg.Wait()
	close(results)
	success := 0
	for w := range results {
		if strings.HasSuffix(w.Header().Get("Location"), "google=connected") {
			success++
		}
	}
	if success != 1 || f.creates != 1 {
		t.Fatalf("competing Calendar assignments: success=%d creates=%d", success, f.creates)
	}
	if len(listTestMembers(t, a, op)) != 3 {
		t.Fatal("competing consent changed shared membership")
	}
}
