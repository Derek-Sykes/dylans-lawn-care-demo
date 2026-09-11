package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const eventsScope = "https://www.googleapis.com/auth/calendar.app.created"
const busyScope = "https://www.googleapis.com/auth/calendar.freebusy"
const calendarScopes = "openid email " + eventsScope + " " + busyScope
const googleScopes = calendarScopes + " " + gmailSendScope
const clientConfigurationError = "Google requires private desktop client configuration. Ask the installation operator to complete setup."

var errWrongOwner = errors.New("different Google owner")
var errScopes = errors.New("required permissions missing")
var errClientSecretRequired = errors.New("Google requires the installation's private desktop client configuration")
var errGoogle = errors.New("calendar connection unavailable")

type Owner struct {
	Sub        string `json:"sub"`
	Email      string `json:"email"`
	CalendarID string `json:"calendarId"`
}
type GoogleTokens struct {
	Access  string    `json:"access"`
	Refresh string    `json:"refresh"`
	Expiry  time.Time `json:"expiry"`
	Scope   string    `json:"scope"`
}
type GoogleClientConfig struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

func validClientConfiguration(in GoogleClientConfig, expectedID string) bool {
	if in.ClientID == "" || len(in.ClientID) > 255 || in.ClientID != expectedID || len(in.ClientSecret) < 8 || len(in.ClientSecret) > 512 {
		return false
	}
	for _, c := range in.ClientSecret {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

type Connection struct {
	Connected bool
	Error     string
}
type Calendar interface {
	Connected() bool
	Busy(context.Context, time.Time, time.Time) ([]Busy, error)
	Sync(context.Context, Booking) error
}
type Google struct {
	cfg            Config
	store          *Store
	client         *http.Client
	now            func() time.Time
	mu             sync.Mutex
	ops            sync.RWMutex
	lastRefresh    time.Time
	lastRefreshErr error
}

func (g *Google) owner() (Owner, error) {
	var v Owner
	err := g.store.getSecret("owner", &v)
	return v, err
}
func (g *Google) connection() Connection {
	var v GoogleTokens
	var message string
	_ = g.store.getJSON("google_error", &message)
	if message == "" {
		_ = g.store.getJSON("calendar_refresh_error", &message)
	}
	if err := g.store.getSecret("google_tokens", &v); err != nil || v.Refresh == "" {
		return Connection{Error: message}
	}
	return Connection{Connected: true, Error: message}
}
func (g *Google) Connected() bool {
	owner, err := g.owner()
	return g.cfg.ClientID != "" && g.connection().Connected && err == nil && owner.CalendarID != ""
}
func (g *Google) authorizationURL(state, verifier, redirect string) string {
	sum := sha256.Sum256([]byte(verifier))
	v := url.Values{"client_id": {g.cfg.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {googleScopes}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	return g.cfg.AuthURL + "?" + v.Encode()
}
func (g *Google) signInURL(state, verifier, redirect string) string {
	sum := sha256.Sum256([]byte(verifier))
	v := url.Values{"client_id": {g.cfg.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {"openid email"}, "prompt": {"select_account"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	return g.cfg.AuthURL + "?" + v.Encode()
}
func (g *Google) userIdentity(ctx context.Context, token string) (Owner, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", g.cfg.UserInfoURL, nil)
	if err != nil {
		return Owner{}, errGoogle
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := g.client.Do(req)
	if err != nil {
		return Owner{}, errGoogle
	}
	defer res.Body.Close()
	var info struct {
		Sub      string `json:"sub"`
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&info) != nil || !info.Verified || info.Sub == "" || validateOperatorIdentity(OperatorIdentity{Schema: 1, GoogleSub: info.Sub, Email: info.Email}) != nil {
		return Owner{}, errGoogle
	}
	return Owner{Sub: info.Sub, Email: normalizeAccessEmail(info.Email)}, nil
}
func (g *Google) signIn(ctx context.Context, code, verifier, redirect string) (Owner, error) {
	reply, err := g.tokenRequest(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}})
	if err != nil {
		return Owner{}, err
	}
	// Identity-only access is short-lived and never replaces Calendar credentials.
	return g.userIdentity(ctx, reply.AccessToken)
}

func (g *Google) clientSecret() (string, error) {
	if g.cfg.ClientSecret != "" {
		return g.cfg.ClientSecret, nil
	}
	if g.cfg.OAuthMode != "desktop" {
		return "", nil
	}
	var config GoogleClientConfig
	err := g.store.getSecret("google_client_config", &config)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if config.ClientID != g.cfg.ClientID {
		return "", nil
	}
	return config.ClientSecret, nil
}

func (g *Google) requiresClientConfiguration() bool {
	if g.cfg.OAuthMode != "desktop" {
		return false
	}
	secret, err := g.clientSecret()
	return err != nil || secret == ""
}

type tokenReply struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (g *Google) tokenRequest(ctx context.Context, v url.Values) (tokenReply, error) {
	v.Set("client_id", g.cfg.ClientID)
	secret, err := g.clientSecret()
	if err != nil {
		return tokenReply{}, errGoogle
	}
	if secret != "" {
		v.Set("client_secret", secret)
	}
	req, e := http.NewRequestWithContext(ctx, "POST", g.cfg.TokenURL, strings.NewReader(v.Encode()))
	if e != nil {
		return tokenReply{}, errGoogle
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, e := g.client.Do(req)
	if e != nil {
		return tokenReply{}, errGoogle
	}
	defer res.Body.Close()
	var out tokenReply
	if e = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); e != nil {
		return out, errGoogle
	}
	if res.StatusCode != 200 || out.AccessToken == "" || out.ExpiresIn <= 0 {
		description := strings.ToLower(out.ErrorDescription)
		if strings.Contains(description, "client_secret") && (strings.Contains(description, "missing") || strings.Contains(description, "required")) {
			out.ErrorDescription = ""
			return out, errClientSecretRequired
		}
		out.ErrorDescription = ""
		return out, errGoogle
	}
	out.ErrorDescription = ""
	return out, nil
}
func validScopes(scope string) bool {
	set := map[string]bool{}
	for _, s := range strings.Fields(scope) {
		set[s] = true
	}
	full := set["https://www.googleapis.com/auth/calendar"]
	return (full || set[eventsScope]) && (full || set[busyScope] || set["https://www.googleapis.com/auth/calendar.events.freebusy"] || set["https://www.googleapis.com/auth/calendar.readonly"])
}
func (g *Google) connect(ctx context.Context, code, verifier, redirect string) error {
	return g.connectAs(ctx, code, verifier, redirect, "")
}
func (g *Google) connectAs(ctx context.Context, code, verifier, redirect, expectedSub string) error {
	return g.connectAuthorized(ctx, code, verifier, redirect, expectedSub, nil, nil)
}
func (g *Google) connectAuthorized(ctx context.Context, code, verifier, redirect, expectedSub string, authorize func() bool, authorizeTx func(*sql.Tx) bool, connectMail ...bool) error {
	reply, err := g.tokenRequest(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}})
	if err != nil {
		return err
	}
	if !validScopes(reply.Scope) {
		return errScopes
	}
	withMail := len(connectMail) == 1 && connectMail[0]
	if withMail && !hasMailScope(reply.Scope) {
		return errScopes
	}
	info, err := g.userIdentity(ctx, reply.AccessToken)
	if err != nil {
		return err
	}
	if expectedSub != "" && expectedSub != info.Sub {
		return errWrongOwner
	}
	g.ops.Lock()
	defer g.ops.Unlock()
	if authorize != nil && !authorize() {
		return errWrongOwner
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	owner, err := g.owner()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if owner.Sub != "" && owner.Sub != info.Sub {
		return errWrongOwner
	}
	if reply.RefreshToken == "" {
		if withMail {
			var previous mailGrant
			if g.store.getSecret("google_mail_tokens", &previous) == nil && previous.Subject == info.Sub && normalizeAccessEmail(previous.Email) == normalizeAccessEmail(info.Email) && hasMailScope(previous.Tokens.Scope) && validScopes(previous.Tokens.Scope) {
				reply.RefreshToken = previous.Tokens.Refresh
			}
		} else {
			var previous GoogleTokens
			if g.store.getSecret("google_tokens", &previous) == nil {
				reply.RefreshToken = previous.Refresh
			}
		}
	}
	if reply.RefreshToken == "" {
		return errors.New("offline access was not granted")
	}
	tokens := GoogleTokens{reply.AccessToken, reply.RefreshToken, g.now().Add(time.Duration(reply.ExpiresIn) * time.Second), reply.Scope}
	if owner.CalendarID == "" {
		settings, e := g.store.settings()
		if e != nil {
			return e
		}
		var created struct {
			ID string `json:"id"`
		}
		_, e = g.authorizedRequest(ctx, "POST", "/calendars", map[string]string{"summary": settings.BusinessName + " bookings", "timeZone": settings.TimeZone}, &created, reply.AccessToken)
		if e != nil || created.ID == "" || len(created.ID) > 1024 {
			return errGoogle
		}
		owner.CalendarID = created.ID
	} else {
		var existing struct {
			ID string `json:"id"`
		}
		_, e := g.authorizedRequest(ctx, "GET", "/calendars/"+url.PathEscape(owner.CalendarID), nil, &existing, reply.AccessToken)
		if e != nil || existing.ID != owner.CalendarID {
			_ = g.store.putJSON("google_error", "The connected booking calendar is unavailable. Restore its access before reconnecting; existing appointments have been kept.")
			return errGoogle
		}
	}
	owner.Sub = info.Sub
	owner.Email = info.Email
	tx, err := g.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if authorizeTx != nil && !authorizeTx(tx) {
		return errWrongOwner
	}
	if err = g.store.writeTxSecret(tx, "owner", owner); err != nil {
		return err
	}
	if err = g.store.writeTxSecret(tx, "google_tokens", tokens); err != nil {
		return err
	}
	if withMail {
		grant := mailGrant{Subject: owner.Sub, Email: owner.Email, Tokens: tokens}
		if err = g.store.writeTxSecret(tx, "google_mail_tokens", grant); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO meta(key,value) VALUES('mail_error','\"\"') ON CONFLICT(key) DO UPDATE SET value='\"\"'"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT INTO meta(key,value) VALUES('bootstrap_disabled','true') ON CONFLICT(key) DO UPDATE SET value='true'"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return g.store.putJSON("google_error", "")
}
func (g *Google) accessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var t GoogleTokens
	if g.store.getSecret("google_tokens", &t) != nil || t.Refresh == "" {
		return "", errGoogle
	}
	if t.Access != "" && t.Expiry.After(g.now().Add(time.Minute)) {
		return t.Access, nil
	}
	r, err := g.tokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}})
	if err != nil {
		if r.Error == "invalid_grant" {
			_, _ = g.store.db.Exec("DELETE FROM secrets WHERE key='google_tokens'")
			_ = g.store.putJSON("google_error", "Reconnect Google to restore appointment availability.")
		} else if errors.Is(err, errClientSecretRequired) {
			_ = g.store.putJSON("google_error", clientConfigurationError)
		}
		return "", err
	}
	if r.Scope != "" && !validScopes(r.Scope) {
		_, _ = g.store.db.Exec("DELETE FROM secrets WHERE key='google_tokens'")
		_ = g.store.putJSON("google_error", "Reconnect Google and grant both Calendar permissions.")
		return "", errScopes
	}
	t.Access = r.AccessToken
	t.Expiry = g.now().Add(time.Duration(r.ExpiresIn) * time.Second)
	if r.RefreshToken != "" {
		t.Refresh = r.RefreshToken
	}
	if r.Scope != "" {
		t.Scope = r.Scope
	}
	if err = g.store.putSecret("google_tokens", t); err != nil {
		return "", err
	}
	return t.Access, nil
}

func (g *Google) request(ctx context.Context, method, path string, in, out any) (int, error) {
	token, err := g.accessToken(ctx)
	if err != nil {
		return 0, err
	}
	return g.authorizedRequest(ctx, method, path, in, out, token)
}
func (g *Google) authorizedRequest(ctx context.Context, method, path string, in, out any, token string) (int, error) {
	var body io.Reader
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return 0, e
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.cfg.CalendarURL+path, body)
	if err != nil {
		return 0, errGoogle
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.client.Do(req)
	if err != nil {
		return 0, errGoogle
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		if res.StatusCode == 401 || res.StatusCode == 403 {
			_ = g.store.putJSON("google_error", "Google could not authorize Calendar access. Reconnect and grant both Calendar permissions.")
		}
		return res.StatusCode, errGoogle
	}
	if out != nil {
		if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(out); err != nil {
			return res.StatusCode, errGoogle
		}
	}
	return res.StatusCode, nil
}

type googleEvent struct {
	ID               string   `json:"id"`
	Status           string   `json:"status"`
	Transparency     string   `json:"transparency"`
	Recurrence       []string `json:"recurrence"`
	RecurringEventID string   `json:"recurringEventId"`
	Start            struct {
		DateTime string `json:"dateTime"`
		Date     string `json:"date"`
	} `json:"start"`
	End struct {
		DateTime string `json:"dateTime"`
		Date     string `json:"date"`
	} `json:"end"`
	Extended struct {
		Private map[string]string `json:"private"`
	} `json:"extendedProperties"`
}

func (g *Google) matchesEvent(e googleEvent, b Booking) bool {
	start, e1 := time.Parse(time.RFC3339, e.Start.DateTime)
	end, e2 := time.Parse(time.RFC3339, e.End.DateTime)
	return e.Status != "cancelled" && e.Extended.Private["booking_id"] == b.ID && e.Extended.Private["installation_id"] == g.store.installID && e1 == nil && e2 == nil && start.Equal(b.Start) && end.Equal(b.End)
}
func (g *Google) Sync(ctx context.Context, b Booking) error {
	g.ops.RLock()
	defer g.ops.RUnlock()
	owner, err := g.owner()
	if err != nil || owner.CalendarID == "" {
		return errGoogle
	}
	calendarPath := "/calendars/" + url.PathEscape(owner.CalendarID) + "/events"
	path := calendarPath + "/" + url.PathEscape(b.EventID)
	if b.Status == "cancelled" {
		status, err := g.request(ctx, "DELETE", path+"?sendUpdates=none", nil, nil)
		if status == 404 || status == 410 {
			return nil
		}
		return err
	}
	var existing googleEvent
	status, err := g.request(ctx, "GET", path, nil, &existing)
	if err == nil {
		if g.matchesEvent(existing, b) {
			return nil
		}
		// A lost insert response may be followed by an owner changing the event
		// before this retry. Import its times instead of recreating or reverting it.
		if existing.ID != b.EventID {
			return errGoogle
		}
		zone := ""
		if existing.Status != "cancelled" && existing.Start.Date != "" {
			// An all-day event is defined in its Google calendar's timezone, which
			// the owner may have changed independently from the website settings.
			var metadata struct {
				TimeZone string `json:"timeZone"`
			}
			if _, err = g.request(ctx, "GET", "/calendars/"+url.PathEscape(owner.CalendarID)+"?fields=timeZone", nil, &metadata); err != nil || metadata.TimeZone == "" {
				return errGoogle
			}
			zone = metadata.TimeZone
		}
		return g.store.applyCalendarSnapshot(ctx, []googleEvent{existing}, calendarCursor{TimeZone: zone}, false, g.now())
	}
	if status != 404 {
		return errGoogle
	}
	buffer := b.BlockedEnd.Sub(b.End)
	settings, err := g.store.settings()
	if err != nil {
		return err
	}
	margin := max(buffer, time.Duration(externalBufferMinutes(settings))*time.Minute)
	busy, err := g.externalBusy(ctx, b.Start.Add(-margin), b.End.Add(margin))
	if err != nil {
		return err
	}
	local, err := g.store.localBusyExcept(b.Start.Add(-margin), b.End.Add(margin), b.ID)
	if err != nil {
		return err
	}
	if conflicts(Slot{b.Start, b.End}, 0, calendarProtection(settings, int(buffer/time.Minute), busy, local)) {
		return errConflict
	}
	title := "Service appointment"
	description := "Website service appointment request. Review the requested job and confirm service coverage and job details with the customer."
	if b.Kind == "estimate" {
		title = "Estimate / callback"
		description = "Website estimate / callback request. This time is for discussing the project and an estimate, not performing the service job."
	}
	description += fmt.Sprintf("\nService: %s\nName: %s\nPhone: %s\nEmail: %s\nProperty address: %s\nJob notes: %s", serviceName(b.ServiceID), b.Name, b.Phone, b.Email, b.Address, b.Notes)
	event := map[string]any{"id": b.EventID, "summary": title + " · " + serviceName(b.ServiceID) + " · " + b.Name, "description": description, "location": b.Address, "visibility": "private", "start": map[string]string{"dateTime": b.Start.Format(time.RFC3339)}, "end": map[string]string{"dateTime": b.End.Format(time.RFC3339)}, "extendedProperties": map[string]any{"private": map[string]string{"booking_id": b.ID, "installation_id": g.store.installID}}}
	status, err = g.request(ctx, "POST", calendarPath+"?sendUpdates=none", event, &existing)
	if status == 409 {
		_, err = g.request(ctx, "GET", path, nil, &existing)
		if err == nil && g.matchesEvent(existing, b) {
			return nil
		}
		return errGoogle
	}
	if err != nil {
		return err
	}
	if !g.matchesEvent(existing, b) {
		return errGoogle
	}
	return nil
}
func (g *Google) disconnect(ctx context.Context) error {
	g.ops.Lock()
	defer g.ops.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	var tokens GoogleTokens
	_ = g.store.getSecret("google_tokens", &tokens)
	if err := g.store.disableEmailNotifications(); err != nil {
		return err
	}
	if err := g.store.dropMailGrant(); err != nil {
		return err
	}
	// Clear local access even when Google's revocation endpoint is temporarily down.
	if _, err := g.store.db.Exec("DELETE FROM secrets WHERE key='google_tokens'"); err != nil {
		return err
	}
	_ = g.store.putJSON("google_error", "")
	if tokens.Refresh != "" {
		req, err := http.NewRequestWithContext(ctx, "POST", g.cfg.RevokeURL, strings.NewReader(url.Values{"token": {tokens.Refresh}}.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if res, e := g.client.Do(req); e == nil {
				io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
				res.Body.Close()
			}
		}
	}
	return nil
}
