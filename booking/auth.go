package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type Session struct {
	CSRF    string `json:"csrf"`
	Expires int64  `json:"expires"`
	Subject string `json:"subject"`
	Email   string `json:"email"`
	Role    string `json:"role"`
}
type OAuthState struct {
	Verifier     string `json:"verifier"`
	SessionID    string `json:"sessionId"`
	RedirectURI  string `json:"redirectUri"`
	Purpose      string `json:"purpose"`
	ActorSubject string `json:"actorSubject"`
	ActorEmail   string `json:"actorEmail"`
	InvitationID string `json:"invitationId"`
}

func (a *App) cookieName() string {
	prefix := "dylan_owner_"
	if a.secureAdmin {
		prefix = "__Host-" + prefix
	}
	return prefix + a.store.installID
}
func (a *App) session(r *http.Request) (Session, string, error) {
	c, err := r.Cookie(a.cookieName())
	if err != nil {
		return Session{}, "", err
	}
	if len(c.Value) > 100 {
		return Session{}, "", errors.New("invalid session")
	}
	id := a.store.hash(c.Value)
	v, err := a.sessionByID(id)
	return v, id, err
}
func (a *App) sessionByID(id string) (Session, error) {
	var encrypted []byte
	var expires int64
	if err := a.store.db.QueryRow("SELECT value,expires FROM sessions WHERE id=?", id).Scan(&encrypted, &expires); err != nil {
		return Session{}, err
	}
	if expires <= a.now().Unix() {
		return Session{}, errors.New("expired session")
	}
	b, err := a.store.open(encrypted, "session:"+id)
	if err != nil {
		return Session{}, err
	}
	var v Session
	err = json.Unmarshal(b, &v)
	if err != nil {
		return Session{}, err
	}
	if v.Role == "bootstrap" && a.bootstrapAllowed() {
		return v, nil
	}
	role := a.identityRole(v.Subject, v.Email)
	if role == "" || (v.Role != "owner" && v.Role != "operator") {
		return Session{}, errors.New("session identity is no longer authorized")
	}
	v.Role = role
	return v, nil
}
func (a *App) newSession(w http.ResponseWriter, actor ...Session) (Session, error) {
	token := randomToken(32)
	id := a.store.hash(token)
	v := Session{Role: "bootstrap"}
	if len(actor) == 1 {
		v = actor[0]
	} else if !a.bootstrapAllowed() {
		return Session{}, errors.New("bootstrap is disabled")
	}
	v.CSRF = randomToken(32)
	v.Expires = a.now().Add(30 * 24 * time.Hour).Unix()
	b, _ := json.Marshal(v)
	b, err := a.store.seal(b, "session:"+id)
	if err != nil {
		return v, err
	}
	if _, err = a.store.db.Exec("INSERT INTO sessions(id,value,expires) VALUES(?,?,?)", id, b, v.Expires); err != nil {
		return v, err
	}
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(), Value: token, Path: "/", HttpOnly: true, Secure: a.secureAdmin, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600, Expires: time.Unix(v.Expires, 0)})
	return v, nil
}
func (a *App) sessionResponse(auth bool, s Session) map[string]any {
	owner, _ := a.google.owner()
	conn := a.google.connection()
	google := map[string]any{"configured": a.cfg.ClientID != "", "connected": conn.Connected, "mode": a.cfg.OAuthMode}
	if auth {
		google["requiresClientConfiguration"] = a.google.requiresClientConfiguration()
		if owner.Email != "" {
			google["email"] = owner.Email
		}
		if conn.Error != "" {
			google["error"] = conn.Error
		}
	}
	out := map[string]any{"authenticated": auth, "setupRequired": a.bootstrapAllowed(), "google": google, "publicOrigin": a.cfg.PublicOrigin}
	if auth {
		out["csrfToken"] = s.CSRF
		out["role"] = s.Role
		out["actorEmail"] = s.Email
		out["canManageAccess"] = s.Role == "operator"
		out["canConnectCalendar"] = a.canConnectCalendar(s)
	}
	return out
}
func (a *App) handleGoogleConfigure(w http.ResponseWriter, r *http.Request) {
	s, _, _ := a.session(r)
	if s.Role != "operator" && s.Role != "bootstrap" {
		writeError(w, &apiError{403, "operator_required", "Only the installation operator can update Google application configuration."})
		return
	}
	if a.cfg.OAuthMode != "desktop" {
		writeError(w, &apiError{409, "configuration_unavailable", "This installation uses server-managed Google configuration."})
		return
	}
	var in GoogleClientConfig
	if !readJSON(w, r, &in) {
		return
	}
	if !validClientConfiguration(in, a.cfg.ClientID) {
		writeError(w, &apiError{400, "invalid_client_configuration", "Choose the private desktop client file for this installation's registered Google app."})
		return
	}
	if err := a.store.putSecret("google_client_config", in); err != nil {
		writeError(w, err)
		return
	}
	var message string
	if a.store.getJSON("google_error", &message) == nil && message == clientConfigurationError {
		_ = a.store.putJSON("google_error", "")
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) handleSession(w http.ResponseWriter, r *http.Request) {
	s, _, err := a.session(r)
	writeJSON(w, 200, a.sessionResponse(err == nil, s))
}
func (a *App) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !a.bootstrapAllowed() || !constantEqual(a.store.hash(in.Token), a.store.hash(a.cfg.BootstrapToken)) {
		writeError(w, &apiError{401, "invalid_bootstrap", "Open this installation's private setup link."})
		return
	}
	s, err := a.newSession(w)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, a.sessionResponse(true, s))
}
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	_, id, err := a.session(r)
	if err == nil {
		_, err = a.store.db.Exec("DELETE FROM sessions WHERE id=?", id)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(), Path: "/", HttpOnly: true, Secure: a.secureAdmin, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.WriteHeader(204)
}
func (a *App) handleConnect(w http.ResponseWriter, r *http.Request) {
	s, sessionID, err := a.session(r)
	if err != nil || !a.canConnectCalendar(s) {
		writeError(w, &apiError{403, "calendar_owner_required", "Only the calendar owner can change this connection."})
		return
	}
	a.beginOAuth(w, r, OAuthState{Purpose: "calendar", SessionID: sessionID, ActorSubject: s.Subject, ActorEmail: s.Email})
}
func (a *App) handleSignIn(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !readJSON(w, r, &in) {
		return
	}
	s, sessionID, err := a.session(r)
	record := OAuthState{Purpose: "signin"}
	if err == nil && s.Role == "bootstrap" {
		record.SessionID = sessionID
	}
	a.beginOAuth(w, r, record)
}
func (a *App) beginOAuth(w http.ResponseWriter, r *http.Request, record OAuthState) {
	if a.cfg.ClientID == "" {
		writeError(w, &apiError{503, "google_unconfigured", "Google connection is not configured for this installation."})
		return
	}
	state, verifier, binding := randomToken(32), randomToken(48), randomToken(32)
	record.Verifier, record.RedirectURI = verifier, a.cfg.AdminOrigin+"/oauth/callback"
	b, _ := json.Marshal(record)
	b, err := a.store.seal(b, "oauth:"+a.store.hash(state))
	if err != nil {
		writeError(w, err)
		return
	}
	_, err = a.store.db.Exec("INSERT INTO oauth_states(id,binding,value,expires) VALUES(?,?,?,?)", a.store.hash(state), a.store.hash(binding), b, a.now().Add(10*time.Minute).Unix())
	if err != nil {
		writeError(w, err)
		return
	}
	// Separate browser binding makes the state single-use AND browser-specific,
	// including returning-owner login before an authenticated session exists.
	http.SetCookie(w, &http.Cookie{Name: a.oauthCookieName(), Value: binding, Path: "/oauth/callback", HttpOnly: true, Secure: a.secureAdmin, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	url := a.google.authorizationURL(state, verifier, record.RedirectURI)
	if record.Purpose != "calendar" {
		url = a.google.signInURL(state, verifier, record.RedirectURI)
	}
	writeJSON(w, 200, map[string]string{"url": url})
}
func (a *App) oauthCookieName() string { return "dylan_oauth_" + a.store.installID }
func (a *App) consumeState(state, binding string) (OAuthState, error) {
	if len(state) > 100 || len(binding) > 100 || state == "" || binding == "" {
		return OAuthState{}, errors.New("invalid OAuth state")
	}
	id := a.store.hash(state)
	var b []byte
	err := a.store.db.QueryRow("DELETE FROM oauth_states WHERE id=? AND binding=? AND expires>? RETURNING value", id, a.store.hash(binding), a.now().Unix()).Scan(&b)
	if err != nil {
		return OAuthState{}, err
	}
	b, err = a.store.open(b, "oauth:"+id)
	if err != nil {
		return OAuthState{}, err
	}
	var record OAuthState
	err = json.Unmarshal(b, &record)
	return record, err
}
func (a *App) handleCallback(w http.ResponseWriter, r *http.Request) {
	outcome := "failed"
	defer func() {
		http.SetCookie(w, &http.Cookie{Name: a.oauthCookieName(), Path: "/oauth/callback", HttpOnly: true, Secure: a.secureAdmin, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		http.Redirect(w, r, a.cfg.adminHomeURL()+"?google="+outcome, http.StatusSeeOther)
	}()
	cookie, err := r.Cookie(a.oauthCookieName())
	if err != nil {
		return
	}
	record, err := a.consumeState(r.URL.Query().Get("state"), cookie.Value)
	if err != nil {
		return
	}
	if r.URL.Query().Get("error") != "" {
		outcome = "denied"
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 4096 {
		return
	}
	var actor Session
	switch record.Purpose {
	case "calendar":
		actor, err = a.sessionByID(record.SessionID)
		if err != nil || !a.canConnectCalendar(actor) || actor.Subject != record.ActorSubject {
			return
		}
		err = a.google.connectAuthorized(r.Context(), code, record.Verifier, record.RedirectURI, record.ActorSubject, func() bool {
			current, e := a.sessionByID(record.SessionID)
			return e == nil && current.Subject == record.ActorSubject && current.Email == record.ActorEmail && a.canConnectCalendar(current)
		}, func(tx *sql.Tx) bool { return a.authorizeCalendarTx(tx, record) })
		if err == nil {
			owner, e := a.google.owner()
			if e != nil {
				return
			}
			// First local setup consumes bootstrap and establishes an identified owner.
			actor = Session{Subject: owner.Sub, Email: owner.Email, Role: a.identityRole(owner.Sub, owner.Email)}
			outcome = "connected"
		}
	case "signin", "invite":
		var identity Owner
		identity, err = a.google.signIn(r.Context(), code, record.Verifier, record.RedirectURI)
		if err == nil {
			if record.Purpose == "invite" {
				err = a.claimInvitation(record.InvitationID, identity)
				outcome = invitationOutcome(err)
			} else {
				policy, policyErr := a.store.operatorIdentity()
				if policyErr == nil && normalizeAccessEmail(policy.Email) == normalizeAccessEmail(identity.Email) {
					err = a.store.bindOperatorIdentity(identity)
				}
				if a.identityRole(identity.Sub, identity.Email) == "" && record.SessionID != "" {
					setup, e := a.sessionByID(record.SessionID)
					if e == nil && setup.Role == "bootstrap" {
						err = a.establishOperator(identity, record.SessionID)
					}
				}
				outcome = "signed_in"
			}
			actor = Session{Subject: identity.Sub, Email: identity.Email, Role: a.identityRole(identity.Sub, identity.Email)}
			if err == nil && actor.Role == "" {
				err = errWrongOwner
			}
		}
	default:
		return // Old OAuth states cannot grant an unidentified session after an update.
	}
	if err != nil {
		if record.Purpose != "invite" || outcome == "invited" {
			outcome = "failed"
		}
		if errors.Is(err, errWrongOwner) {
			outcome = "wrong_account"
		}
		if errors.Is(err, errScopes) {
			outcome = "missing_scopes"
		}
		if errors.Is(err, errClientSecretRequired) {
			outcome = "configuration_required"
			_ = a.store.putJSON("google_error", clientConfigurationError)
		}
		return
	}
	if _, err = a.newSession(w, actor); err != nil {
		outcome = "failed"
		return
	}
	if record.Purpose == "calendar" {
		_, _ = a.store.db.Exec("UPDATE bookings SET next_attempt=0 WHERE calendar_status!='synced'")
		a.wakeWorker()
	}
}
