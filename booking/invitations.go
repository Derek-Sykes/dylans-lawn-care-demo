package main

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

type Invitation struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expiresAt"`
	Status    string    `json:"status"`
}

func invitationError(code string) error {
	switch code {
	case "invitation_expired":
		return &apiError{410, code, "This invitation has expired. Ask the operator for a new link."}
	case "invitation_revoked":
		return &apiError{410, code, "This invitation was revoked. Ask the operator for a new link."}
	case "invitation_used":
		return &apiError{409, code, "This invitation has already been used. Sign in with your Google account."}
	case "owner_transfer_required":
		return &apiError{409, code, "This installation already has a different owner. The operator must arrange a separate installation or a controlled owner handoff."}
	default:
		return &apiError{400, "invalid_invitation", "This invitation is not valid. Ask the operator for a new link."}
	}
}
func invitationOutcome(err error) string {
	if err == nil {
		return "invited"
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Code
	}
	if errors.Is(err, errWrongOwner) {
		return "wrong_account"
	}
	return "failed"
}
func checkInvitation(status string, expires int64, now time.Time) error {
	if status == "used" {
		return invitationError("invitation_used")
	}
	if status == "revoked" {
		return invitationError("invitation_revoked")
	}
	if expires <= now.Unix() {
		return invitationError("invitation_expired")
	}
	if status != "pending" {
		return invitationError("")
	}
	return nil
}
func (a *App) requireOperator(w http.ResponseWriter, r *http.Request) bool {
	s, _, err := a.session(r)
	if err != nil || s.Role != "operator" {
		writeError(w, &apiError{403, "operator_required", "Only the installation operator can manage owner access."})
		return false
	}
	return true
}
func (a *App) handleInvitations(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	rows, err := a.store.db.Query("SELECT id,email,expires,status FROM invitations ORDER BY created DESC LIMIT 100")
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	items := []Invitation{}
	for rows.Next() {
		var v Invitation
		var expires int64
		if err = rows.Scan(&v.ID, &v.Email, &expires, &v.Status); err != nil {
			writeError(w, err)
			return
		}
		v.ExpiresAt = time.Unix(expires, 0).UTC()
		if v.Status == "pending" && expires <= a.now().Unix() {
			v.Status = "expired"
		}
		items = append(items, v)
	}
	if err = rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"invitations": items})
}
func (a *App) handleInvitationCreate(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Email = normalizeAccessEmail(in.Email)
	if !validAccessEmail(in.Email) {
		writeError(w, &apiError{400, "invalid_email", "Enter the owner's Google account email address."})
		return
	}
	owner, err := a.google.owner()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	if owner.Sub != "" && normalizeAccessEmail(owner.Email) != in.Email {
		writeError(w, invitationError("owner_transfer_required"))
		return
	}
	token := randomToken(32)
	actor, _, _ := a.session(r)
	v := Invitation{ID: randomToken(18), Email: in.Email, ExpiresAt: a.now().Add(24 * time.Hour).UTC(), Status: "pending"}
	tx, err := a.store.db.Begin()
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE invitations SET status='revoked' WHERE status='pending'"); err == nil {
		_, err = tx.Exec("INSERT INTO invitations(id,token_hash,email,expires,created,status,issuer_sub) VALUES(?,?,?,?,?,'pending',?)", v.ID, a.store.hash(token), v.Email, v.ExpiresAt.Unix(), a.now().Unix(), actor.Subject)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"invitation": v, "url": a.cfg.adminHomeURL() + "#invite=" + token})
}
func (a *App) handleInvitationRevoke(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	result, err := a.store.db.Exec("UPDATE invitations SET status='revoked' WHERE id=? AND status='pending'", r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		var status string
		if err = a.store.db.QueryRow("SELECT status FROM invitations WHERE id=?", r.PathValue("id")).Scan(&status); err != nil {
			writeError(w, invitationError(""))
			return
		}
		if status == "used" {
			writeError(w, invitationError("invitation_used"))
			return
		}
	}
	w.WriteHeader(204)
}
func (a *App) handleInvitationAccept(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Token) < 32 || len(in.Token) > 100 {
		writeError(w, invitationError(""))
		return
	}
	var id, status string
	var expires int64
	if err := a.store.db.QueryRow("SELECT id,status,expires FROM invitations WHERE token_hash=?", a.store.hash(in.Token)).Scan(&id, &status, &expires); err != nil {
		writeError(w, invitationError(""))
		return
	}
	if err := checkInvitation(status, expires, a.now()); err != nil {
		writeError(w, err)
		return
	}
	a.beginOAuth(w, r, OAuthState{Purpose: "invite", InvitationID: id})
}
func (a *App) claimInvitation(id string, identity Owner) error {
	// Serialize ownership against Calendar connect/disconnect. The transaction also
	// protects invitation revocation, competing claims, and persistent identity.
	a.google.ops.Lock()
	defer a.google.ops.Unlock()
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var email, status, issuer string
	var expires int64
	if err = tx.QueryRow("SELECT email,status,expires,issuer_sub FROM invitations WHERE id=?", id).Scan(&email, &status, &expires, &issuer); err != nil {
		return invitationError("")
	}
	if err = checkInvitation(status, expires, a.now()); err != nil {
		return err
	}
	var operator OperatorIdentity
	if err = a.store.readTxSecret(tx, "operator_identity", &operator); err != nil || operator.GoogleSub != issuer {
		return invitationError("invitation_revoked")
	}
	if normalizeAccessEmail(identity.Email) != email {
		return errWrongOwner
	}
	var owner Owner
	err = a.store.readTxSecret(tx, "owner", &owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// A second owner is always a deliberate handoff, even before Calendar connects.
	if owner.Sub != "" && owner.Sub != identity.Sub {
		return invitationError("owner_transfer_required")
	}
	if owner.Sub == "" {
		var count int
		if err = tx.QueryRow("SELECT COUNT(*) FROM bookings").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return invitationError("owner_transfer_required")
		}
		owner = Owner{Sub: identity.Sub, Email: identity.Email}
	}
	if err = a.store.writeTxSecret(tx, "owner", owner); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE invitations SET status='used',used_sub=? WHERE id=?", identity.Sub, id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE invitations SET status='revoked' WHERE status='pending' AND id!=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO meta(key,value) VALUES('bootstrap_disabled','true') ON CONFLICT(key) DO UPDATE SET value='true'"); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) establishOperator(identity Owner, sessionID ...string) error {
	// Only the still-live bootstrap flow can reach this. Locking ownership also
	// prevents a competing first Calendar connection from changing this decision.
	a.google.ops.Lock()
	defer a.google.ops.Unlock()
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !a.bootstrapAllowedTx(tx) {
		return errWrongOwner
	}
	if len(sessionID) == 1 {
		s, e := a.readSessionTx(tx, sessionID[0])
		if e != nil || s.Role != "bootstrap" {
			return errWrongOwner
		}
	}
	v := OperatorIdentity{Schema: 1, GoogleSub: identity.Sub, Email: normalizeAccessEmail(identity.Email)}
	if err = validateOperatorIdentity(v); err != nil || v.GoogleSub == "" {
		return errWrongOwner
	}
	if err = a.store.writeTxSecret(tx, "operator_identity", v); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO meta(key,value) VALUES('bootstrap_disabled','true') ON CONFLICT(key) DO UPDATE SET value='true'"); err != nil {
		return err
	}
	return tx.Commit()
}
