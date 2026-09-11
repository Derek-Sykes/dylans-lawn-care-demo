package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

const gmailSendScope = "https://www.googleapis.com/auth/gmail.send"

// Mail has its own explicit consent and encrypted grant. Portal sign-in and
// Calendar connection never silently authorize sending from someone's inbox.
type mailGrant struct {
	Subject string       `json:"subject"`
	Email   string       `json:"email"`
	Tokens  GoogleTokens `json:"tokens"`
}
type EmailMessage struct{ ID, To, Subject, Text string }
type MailSender interface {
	Connected() bool
	Send(context.Context, EmailMessage) (string, error)
}
type MailSendError struct {
	Message   string
	Retryable bool
	Uncertain bool
}

func (e *MailSendError) Error() string { return e.Message }

type GoogleMail struct{ google *Google }
type EmailConnection struct {
	Connected  bool   `json:"connected"`
	Email      string `json:"email"`
	CanConnect bool   `json:"canConnect"`
	Error      string `json:"error"`
}

func hasMailScope(scope string) bool {
	for _, s := range strings.Fields(scope) {
		if s == gmailSendScope {
			return true
		}
	}
	return false
}
func (m *GoogleMail) Connected() bool {
	g := m.google
	var grant mailGrant
	owner, err := g.owner()
	return err == nil && g.Connected() && g.store.getSecret("google_mail_tokens", &grant) == nil && grant.Subject == owner.Sub && normalizeAccessEmail(grant.Email) == normalizeAccessEmail(owner.Email) && grant.Tokens.Refresh != "" && hasMailScope(grant.Tokens.Scope)
}
func (a *App) canConnectMail(actor Session) bool {
	owner, err := a.google.owner()
	return err == nil && owner.Sub != "" && owner.CalendarID != "" && actor.Subject == owner.Sub && a.canConnectCalendar(actor)
}
func (a *App) mailConnection(actor Session) EmailConnection {
	owner, _ := a.google.owner()
	var message string
	_ = a.store.getJSON("mail_error", &message)
	return EmailConnection{Connected: a.mailer != nil && a.mailer.Connected(), Email: owner.Email, CanConnect: a.canConnectMail(actor), Error: message}
}
func (g *Google) mailAuthorizationURL(state, verifier, redirect string) string {
	u, _ := url.Parse(g.authorizationURL(state, verifier, redirect))
	q := u.Query()
	q.Set("scope", "openid email "+gmailSendScope)
	q.Set("include_granted_scopes", "false")
	if owner, err := g.owner(); err == nil {
		q.Set("login_hint", owner.Email)
	}
	u.RawQuery = q.Encode()
	return u.String()
}
func (a *App) handleMailConnect(w http.ResponseWriter, r *http.Request) {
	actor, id, err := a.session(r)
	if err != nil || !a.canConnectMail(actor) {
		writeError(w, &apiError{403, "calendar_owner_required", "Only the connected Calendar account can authorize sending email."})
		return
	}
	if !a.google.Connected() {
		writeError(w, &apiError{409, "calendar_connection_required", "Reconnect the booking calendar before connecting email."})
		return
	}
	version, err := a.store.mailAuthorizationVersion()
	if err != nil {
		writeError(w, err)
		return
	}
	a.beginOAuth(w, r, OAuthState{Purpose: "gmail", SessionID: id, ActorSubject: actor.Subject, ActorEmail: actor.Email, MailVersion: version})
}
func (s *Store) mailAuthorizationVersion() (string, error) {
	value := randomToken(24)
	b, _ := json.Marshal(value)
	if _, err := s.db.Exec("INSERT OR IGNORE INTO meta(key,value) VALUES('mail_authorization_version',?)", b); err != nil {
		return "", err
	}
	err := s.getJSON("mail_authorization_version", &value)
	return value, err
}
func (s *Store) dropMailGrant() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	version, _ := json.Marshal(randomToken(24))
	if _, err = tx.Exec("INSERT INTO meta(key,value) VALUES('mail_authorization_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", version); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM secrets WHERE key='google_mail_tokens'"); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) connectMail(ctx context.Context, code string, record OAuthState) error {
	g := a.google
	reply, err := g.tokenRequest(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {record.Verifier}, "redirect_uri": {record.RedirectURI}})
	if err != nil {
		return err
	}
	if !hasMailScope(reply.Scope) {
		return errScopes
	}
	identity, err := g.userIdentity(ctx, reply.AccessToken)
	if err != nil {
		return err
	}
	if identity.Sub != record.ActorSubject || normalizeAccessEmail(identity.Email) != normalizeAccessEmail(record.ActorEmail) {
		return errWrongOwner
	}
	g.ops.Lock()
	defer g.ops.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	tx, err := g.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rawVersion []byte
	var version string
	if tx.QueryRow("SELECT value FROM meta WHERE key='mail_authorization_version'").Scan(&rawVersion) != nil || json.Unmarshal(rawVersion, &version) != nil || version == "" || version != record.MailVersion {
		return errWrongOwner
	}
	var owner Owner
	if g.store.readTxSecret(tx, "owner", &owner) != nil || owner.CalendarID == "" || owner.Sub != identity.Sub || normalizeAccessEmail(owner.Email) != normalizeAccessEmail(identity.Email) || !a.authorizeCalendarTx(tx, record) {
		return errWrongOwner
	}
	var calendarTokens GoogleTokens
	if g.store.readTxSecret(tx, "google_tokens", &calendarTokens) != nil || calendarTokens.Refresh == "" {
		return errGoogle
	}
	if reply.RefreshToken == "" {
		var old mailGrant
		if g.store.readTxSecret(tx, "google_mail_tokens", &old) == nil && old.Subject == identity.Sub && normalizeAccessEmail(old.Email) == normalizeAccessEmail(identity.Email) {
			reply.RefreshToken = old.Tokens.Refresh
		}
	}
	if reply.RefreshToken == "" {
		return errors.New("offline email access was not granted")
	}
	grant := mailGrant{Subject: identity.Sub, Email: identity.Email, Tokens: GoogleTokens{Access: reply.AccessToken, Refresh: reply.RefreshToken, Expiry: g.now().Add(time.Duration(reply.ExpiresIn) * time.Second), Scope: reply.Scope}}
	if err = g.store.writeTxSecret(tx, "google_mail_tokens", grant); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO meta(key,value) VALUES('mail_error','\"\"') ON CONFLICT(key) DO UPDATE SET value='\"\"'"); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) handleMailDisconnect(w http.ResponseWriter, r *http.Request) {
	actor, _, err := a.session(r)
	if err != nil || !a.canConnectMail(actor) {
		writeError(w, &apiError{403, "calendar_owner_required", "Only the connected Calendar account can disconnect email."})
		return
	}
	a.google.ops.Lock()
	defer a.google.ops.Unlock()
	// Google's revoke endpoint revokes the whole project grant, including Calendar.
	// A mail-only disconnect deliberately removes this installation's mail token.
	if err = a.store.disableEmailNotifications(); err == nil {
		err = a.store.dropMailGrant()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	_ = a.store.putJSON("mail_error", "")
	w.WriteHeader(http.StatusNoContent)
}
func (g *Google) mailAccessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var grant mailGrant
	if g.store.getSecret("google_mail_tokens", &grant) != nil || grant.Tokens.Refresh == "" {
		return "", &MailSendError{Message: "Connect Gmail to send appointment emails."}
	}
	t := &grant.Tokens
	if t.Access != "" && t.Expiry.After(g.now().Add(time.Minute)) {
		return t.Access, nil
	}
	r, err := g.tokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}})
	if err != nil {
		if r.Error == "invalid_grant" {
			_, _ = g.store.db.Exec("DELETE FROM secrets WHERE key='google_mail_tokens'")
			_ = g.store.putJSON("mail_error", "Reconnect Gmail to restore appointment emails.")
			return "", &MailSendError{Message: "Reconnect Gmail to restore appointment emails."}
		}
		return "", &MailSendError{Message: "Google could not refresh the email connection. Try again shortly.", Retryable: true}
	}
	if r.Scope != "" && !hasMailScope(r.Scope) {
		_, _ = g.store.db.Exec("DELETE FROM secrets WHERE key='google_mail_tokens'")
		_ = g.store.putJSON("mail_error", "Reconnect Gmail and allow sending email.")
		return "", &MailSendError{Message: "Reconnect Gmail and allow sending email."}
	}
	t.Access, t.Expiry = r.AccessToken, g.now().Add(time.Duration(r.ExpiresIn)*time.Second)
	if r.RefreshToken != "" {
		t.Refresh = r.RefreshToken
	}
	if r.Scope != "" {
		t.Scope = r.Scope
	}
	if err = g.store.putSecret("google_mail_tokens", grant); err != nil {
		return "", &MailSendError{Message: "The email connection could not be saved.", Retryable: true}
	}
	return t.Access, nil
}
func mailAddress(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid email address")
	}
	a, err := mail.ParseAddress(value)
	if err != nil || a.Address != value {
		return "", errors.New("invalid email address")
	}
	return a.String(), nil
}
func encodeEmail(from, business string, msg EmailMessage, now time.Time) (string, error) {
	if _, err := mailAddress(from); err != nil {
		return "", err
	}
	to, err := mailAddress(msg.To)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(msg.Subject, "\r\n") || strings.ContainsAny(business, "\r\n") || msg.ID == "" || strings.IndexFunc(msg.ID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) >= 0 || len(msg.Text) > 64<<10 {
		return "", errors.New("invalid email content")
	}
	body := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(strings.ReplaceAll(msg.Text, "\r\n", "\n"), "\n", "\r\n")))
	var lines strings.Builder
	for len(body) > 76 {
		lines.WriteString(body[:76] + "\r\n")
		body = body[76:]
	}
	lines.WriteString(body + "\r\n")
	fromAddress := (&mail.Address{Name: business, Address: from}).String()
	raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nReply-To: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@booking.local>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\nAuto-Submitted: auto-generated\r\n\r\n%s", fromAddress, to, fromAddress, mime.QEncoding.Encode("utf-8", msg.Subject), now.Format(time.RFC1123Z), msg.ID, lines.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw)), nil
}
func (m *GoogleMail) Send(ctx context.Context, msg EmailMessage) (string, error) {
	g := m.google
	g.ops.RLock()
	defer g.ops.RUnlock()
	if !m.Connected() {
		return "", &MailSendError{Message: "Connect Gmail from the account that owns the booking calendar."}
	}
	owner, _ := g.owner()
	settings, err := g.store.settings()
	if err != nil {
		return "", &MailSendError{Message: "The business settings could not be loaded.", Retryable: true}
	}
	raw, err := encodeEmail(owner.Email, settings.BusinessName, msg, g.now())
	if err != nil {
		return "", &MailSendError{Message: "Check the email recipient and message details."}
	}
	token, err := g.mailAccessToken(ctx)
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]string{"raw": raw})
	req, err := http.NewRequestWithContext(ctx, "POST", g.cfg.GmailURL+"/users/me/messages/send", bytes.NewReader(payload))
	if err != nil {
		return "", &MailSendError{Message: "The email service is not configured correctly."}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if ctx.Err() != nil {
		return "", &MailSendError{Message: "The email send was interrupted before it began.", Retryable: true}
	}
	res, err := g.client.Do(req)
	if err != nil {
		return "", &MailSendError{Message: "Google did not confirm delivery. Check Sent in Gmail before retrying.", Uncertain: true}
	}
	defer res.Body.Close()
	if res.StatusCode == 200 {
		var out struct {
			ID string `json:"id"`
		}
		if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) == nil && out.ID != "" {
			_ = g.store.putJSON("mail_error", "")
			return out.ID, nil
		}
		return "", &MailSendError{Message: "Google accepted the request without a delivery reference. Check Sent in Gmail before retrying.", Uncertain: true}
	}
	var rejected struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&rejected)
	quota := false
	for _, detail := range rejected.Error.Errors {
		if detail.Reason == "rateLimitExceeded" || detail.Reason == "userRateLimitExceeded" || detail.Reason == "dailyLimitExceeded" {
			quota = true
		}
	}
	switch {
	case res.StatusCode == 401:
		g.mu.Lock()
		var grant mailGrant
		if g.store.getSecret("google_mail_tokens", &grant) == nil {
			grant.Tokens.Access = ""
			grant.Tokens.Expiry = time.Time{}
			_ = g.store.putSecret("google_mail_tokens", grant)
		}
		g.mu.Unlock()
		return "", &MailSendError{Message: "Google needs the email connection refreshed.", Retryable: true}
	case res.StatusCode == 403:
		if quota {
			return "", &MailSendError{Message: "Gmail's sending limit was reached. Delivery will be retried later.", Retryable: true}
		}
		_ = g.store.putJSON("mail_error", "Google has not allowed sending email. Check Gmail API setup and reconnect Gmail with send permission.")
		return "", &MailSendError{Message: "Google has not allowed sending email. Check Gmail API setup and reconnect Gmail with send permission."}
	case res.StatusCode == 429:
		return "", &MailSendError{Message: "Gmail's sending limit was reached. Delivery will be retried later.", Retryable: true}
	case res.StatusCode >= 500:
		return "", &MailSendError{Message: "Google could not confirm whether this email was sent. Check Sent in Gmail before retrying.", Uncertain: true}
	default:
		return "", &MailSendError{Message: "Google rejected this message. Check the recipient before retrying."}
	}
}
