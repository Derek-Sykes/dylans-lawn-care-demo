package main

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

type WorkspaceMember struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Role          string `json:"role"`
	CalendarOwner bool   `json:"calendarOwner"`
	CanRemove     bool   `json:"canRemove"`
}

func (s *Store) hasWorkspaceEmailTx(tx *sql.Tx, email string) (bool, error) {
	var operator OperatorIdentity
	err := s.readTxSecret(tx, "operator_identity", &operator)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil && normalizeAccessEmail(operator.Email) == email {
		return true, nil
	}
	var owner Owner
	err = s.readTxSecret(tx, "owner", &owner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err == nil && owner.Sub != "" && normalizeAccessEmail(owner.Email) == email {
		return true, nil
	}
	var count int
	err = tx.QueryRow("SELECT COUNT(*) FROM workspace_members WHERE email=? AND status='active'", email).Scan(&count)
	return count > 0, err
}

// A Google subject remains bound even after removal. Rejoining rotates the
// session generation so old browser cookies cannot regain access.
func (s *Store) addWorkspaceMemberTx(tx *sql.Tx, identity Owner, now time.Time) error {
	email := normalizeAccessEmail(identity.Email)
	var id, sub, savedEmail, status string
	err := tx.QueryRow("SELECT id,google_sub,email,status FROM workspace_members WHERE google_sub=? OR email=?", identity.Sub, email).Scan(&id, &sub, &savedEmail, &status)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec("INSERT INTO workspace_members(id,google_sub,email,session_version,created) VALUES(?,?,?,?,?)", randomToken(18), identity.Sub, email, randomToken(24), now.Unix())
		return err
	}
	if err != nil {
		return err
	}
	if sub != identity.Sub || savedEmail != email {
		return errWrongOwner
	}
	if status == "active" {
		return invitationError("member_already_exists")
	}
	_, err = tx.Exec("UPDATE workspace_members SET status='active',session_version=? WHERE id=?", randomToken(24), id)
	return err
}

// Earlier releases used owner for an invitation accepted before Calendar was
// ever connected. Convert only that invitation-only identity to a member. An
// established Calendar (including a disconnected one) and its tokens stay intact.
func (s *Store) migrateInvitationOnlyOwner() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner Owner
	err = s.readTxSecret(tx, "owner", &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner.Sub == "" || owner.CalendarID != "" {
		return nil
	}
	var count int
	if err = tx.QueryRow("SELECT (SELECT COUNT(*) FROM secrets WHERE key='google_tokens') + (SELECT COUNT(*) FROM bookings)").Scan(&count); err != nil || count != 0 {
		return err
	}
	if err = s.addWorkspaceMemberTx(tx, owner, time.Now()); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM secrets WHERE key='owner'"); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) handleMembers(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	// A single transaction keeps the displayed protection flags consistent with
	// membership removals or the first Calendar connection happening concurrently.
	tx, err := a.store.db.Begin()
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()
	var operator OperatorIdentity
	if err = a.store.readTxSecret(tx, "operator_identity", &operator); err != nil {
		writeError(w, err)
		return
	}
	var owner Owner
	if err = a.store.readTxSecret(tx, "owner", &owner); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	items := []WorkspaceMember{{ID: "operator", Email: normalizeAccessEmail(operator.Email), Role: "operator", CalendarOwner: owner.Sub != "" && owner.Sub == operator.GoogleSub}}
	seenSub := map[string]bool{operator.GoogleSub: true}
	seenEmail := map[string]bool{normalizeAccessEmail(operator.Email): true}
	if owner.Sub != "" && !seenSub[owner.Sub] && !seenEmail[normalizeAccessEmail(owner.Email)] {
		items = append(items, WorkspaceMember{ID: "calendar-owner", Email: normalizeAccessEmail(owner.Email), Role: "owner", CalendarOwner: true})
		seenSub[owner.Sub], seenEmail[normalizeAccessEmail(owner.Email)] = true, true
	}
	rows, err := tx.Query("SELECT id,google_sub,email FROM workspace_members WHERE status='active' ORDER BY created,email")
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, sub, email string
		if err = rows.Scan(&id, &sub, &email); err != nil {
			writeError(w, err)
			return
		}
		if !seenSub[sub] && !seenEmail[email] {
			items = append(items, WorkspaceMember{ID: id, Email: email, Role: "owner", CanRemove: true})
			seenSub[sub], seenEmail[email] = true, true
		}
	}
	if err = rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"members": items})
}

func (a *App) handleMemberRemove(w http.ResponseWriter, r *http.Request) {
	if !a.requireOperator(w, r) {
		return
	}
	id := r.PathValue("id")
	if id == "operator" || id == "calendar-owner" {
		writeError(w, &apiError{409, "member_protected", "The operator and connected Calendar account must keep workspace access."})
		return
	}
	a.google.ops.Lock()
	defer a.google.ops.Unlock()
	tx, err := a.store.db.Begin()
	if err != nil {
		writeError(w, err)
		return
	}
	defer tx.Rollback()
	var sub, email string
	err = tx.QueryRow("SELECT google_sub,email FROM workspace_members WHERE id=? AND status='active'", id).Scan(&sub, &email)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, &apiError{404, "member_not_found", "This account no longer has workspace access."})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	var operator OperatorIdentity
	if err = a.store.readTxSecret(tx, "operator_identity", &operator); err != nil {
		writeError(w, err)
		return
	}
	var owner Owner
	if err = a.store.readTxSecret(tx, "owner", &owner); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	if sub == operator.GoogleSub || email == normalizeAccessEmail(operator.Email) || sub == owner.Sub || email == normalizeAccessEmail(owner.Email) {
		writeError(w, &apiError{409, "member_protected", "The operator and connected Calendar account must keep workspace access."})
		return
	}
	if _, err = tx.Exec("UPDATE workspace_members SET status='revoked' WHERE id=?", id); err == nil {
		_, err = tx.Exec("UPDATE invitations SET status='revoked' WHERE email=? AND status='pending'", email)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(204)
}
