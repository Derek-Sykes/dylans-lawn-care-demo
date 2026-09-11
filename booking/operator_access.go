package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
)

// OperatorIdentity is an allowlisted identity, not a Google credential.
type OperatorIdentity struct {
	Schema    int    `json:"schema"`
	GoogleSub string `json:"googleSub"`
	Email     string `json:"email"`
}

func normalizeAccessEmail(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func validAccessEmail(value string) bool {
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value && len(value) <= 254 && !strings.ContainsAny(value, "\r\n")
}
func validateOperatorIdentity(v OperatorIdentity) error {
	if v.Schema != 1 || len(v.GoogleSub) > 255 || strings.TrimSpace(v.GoogleSub) != v.GoogleSub || strings.ContainsAny(v.GoogleSub, "\r\n\t ") || !validAccessEmail(normalizeAccessEmail(v.Email)) {
		return errors.New("operator configuration requires schema 1, a Google account email, and an optional verified Google subject")
	}
	return nil
}
func (s *Store) operatorIdentity() (OperatorIdentity, error) {
	var v OperatorIdentity
	err := s.getSecret("operator_identity", &v)
	if err == nil {
		err = validateOperatorIdentity(v)
	}
	return v, err
}
func (s *Store) readTxSecret(tx *sql.Tx, key string, out any) error {
	var b []byte
	if err := tx.QueryRow("SELECT value FROM secrets WHERE key=?", key).Scan(&b); err != nil {
		return err
	}
	b, err := s.open(b, key)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func (s *Store) writeTxSecret(tx *sql.Tx, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b, err = s.seal(b, key)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO secrets(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, b)
	return err
}
func (s *Store) importOperatorIdentity(v OperatorIdentity) error {
	if err := validateOperatorIdentity(v); err != nil {
		return err
	}
	v.Email = normalizeAccessEmail(v.Email)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current OperatorIdentity
	err = s.readTxSecret(tx, "operator_identity", &current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (normalizeAccessEmail(current.Email) != v.Email || (current.GoogleSub != "" && v.GoogleSub != "" && current.GoogleSub != v.GoogleSub)) {
		return errors.New("a different operator is already configured; the saved operator was preserved")
	}
	if err == nil && current.GoogleSub != "" {
		v.GoogleSub = current.GoogleSub
	}
	if err = s.writeTxSecret(tx, "operator_identity", v); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO meta(key,value) VALUES('bootstrap_disabled','true') ON CONFLICT(key) DO UPDATE SET value='true'"); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) bootstrapAllowed() bool {
	var disabled bool
	if err := a.store.getJSON("bootstrap_disabled", &disabled); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if disabled {
		return false
	}
	if _, err := a.store.operatorIdentity(); !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	owner, err := a.google.owner()
	return (err == nil && owner.Sub == "") || errors.Is(err, sql.ErrNoRows)
}

func (a *App) identityRole(sub, email string) string {
	operator, err := a.store.operatorIdentity()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err == nil && sub != "" && sub == operator.GoogleSub && normalizeAccessEmail(email) == normalizeAccessEmail(operator.Email) {
		return "operator"
	}
	owner, err := a.google.owner()
	if err == nil && sub != "" && sub == owner.Sub && normalizeAccessEmail(email) == normalizeAccessEmail(owner.Email) {
		return "owner"
	}
	return ""
}

// An email-only installation policy is pinned to Google's stable subject on the
// first verified sign-in. No session can become operator from email alone.
func (s *Store) bindOperatorIdentity(identity Owner) error {
	if identity.Sub == "" {
		return errWrongOwner
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current OperatorIdentity
	if err = s.readTxSecret(tx, "operator_identity", &current); err != nil {
		return err
	}
	if normalizeAccessEmail(current.Email) != normalizeAccessEmail(identity.Email) || (current.GoogleSub != "" && current.GoogleSub != identity.Sub) {
		return errWrongOwner
	}
	current.GoogleSub = identity.Sub
	if err = s.writeTxSecret(tx, "operator_identity", current); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) readSessionTx(tx *sql.Tx, id string) (Session, error) {
	var b []byte
	var expires int64
	if err := tx.QueryRow("SELECT value,expires FROM sessions WHERE id=?", id).Scan(&b, &expires); err != nil {
		return Session{}, err
	}
	if expires <= a.now().Unix() {
		return Session{}, errors.New("expired session")
	}
	b, err := a.store.open(b, "session:"+id)
	if err != nil {
		return Session{}, err
	}
	var s Session
	err = json.Unmarshal(b, &s)
	return s, err
}
func (a *App) bootstrapAllowedTx(tx *sql.Tx) bool {
	var raw []byte
	var disabled bool
	err := tx.QueryRow("SELECT value FROM meta WHERE key='bootstrap_disabled'").Scan(&raw)
	if err == nil {
		if json.Unmarshal(raw, &disabled) != nil || disabled {
			return false
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	var operator OperatorIdentity
	if err = a.store.readTxSecret(tx, "operator_identity", &operator); !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	var owner Owner
	err = a.store.readTxSecret(tx, "owner", &owner)
	return errors.Is(err, sql.ErrNoRows) || (err == nil && owner.Sub == "")
}
func (a *App) authorizeCalendarTx(tx *sql.Tx, record OAuthState) bool {
	s, err := a.readSessionTx(tx, record.SessionID)
	if err != nil || s.Subject != record.ActorSubject || s.Email != record.ActorEmail {
		return false
	}
	if s.Role == "bootstrap" {
		return a.bootstrapAllowedTx(tx)
	}
	if s.Role != "operator" && s.Role != "owner" {
		return false
	}
	var operator OperatorIdentity
	err = a.store.readTxSecret(tx, "operator_identity", &operator)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	isOperator := err == nil && s.Subject != "" && s.Subject == operator.GoogleSub && normalizeAccessEmail(s.Email) == normalizeAccessEmail(operator.Email)
	var owner Owner
	err = a.store.readTxSecret(tx, "owner", &owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner.Sub == "") {
		return isOperator
	}
	return err == nil && s.Subject != "" && s.Subject == owner.Sub && normalizeAccessEmail(s.Email) == normalizeAccessEmail(owner.Email)
}
func (a *App) canConnectCalendar(s Session) bool {
	if s.Role == "bootstrap" {
		return a.bootstrapAllowed()
	}
	if s.Role != "operator" && s.Role != "owner" {
		return false
	}
	owner, err := a.google.owner()
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner.Sub == "") {
		return s.Role == "operator"
	}
	return err == nil && s.Subject == owner.Sub && normalizeAccessEmail(s.Email) == normalizeAccessEmail(owner.Email)
}
