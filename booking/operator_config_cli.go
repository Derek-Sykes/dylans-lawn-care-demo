package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Operator configuration contains an approved Google identity, never a password
// or bearer token. Host-side provisioning is separate from browser invitations.
func runOperatorConfigCommand(args []string, input io.Reader, stdout, stderr io.Writer, load func() (Config, error)) int {
	fail := func(message string) int { fmt.Fprintln(stderr, message); return 1 }
	if len(args) != 2 || args[0] != "operator-config" || (args[1] != "status" && args[1] != "import" && args[1] != "export") {
		return fail("Use booking operator-config status, import or export.")
	}
	c, err := load()
	if err != nil {
		return fail("Installation configuration is unavailable.")
	}
	a, err := newApp(c)
	if err != nil {
		return fail("Private installation storage is unavailable.")
	}
	defer a.store.close()
	if args[1] == "import" {
		identity, err := readOperatorConfiguration(input)
		if err != nil {
			return fail("Operator configuration is invalid. Supply only the approved Google identity.")
		}
		if err = a.store.importOperatorIdentity(identity); err != nil {
			return fail("Operator configuration could not be imported. Existing access was preserved.")
		}
		return 0
	}
	identity, err := a.store.operatorIdentity()
	if args[1] == "status" {
		if errors.Is(err, sql.ErrNoRows) {
			return 3
		}
		if err != nil {
			return fail("Saved operator access could not be read.")
		}
		return 0
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Explicit export can prepare an identity policy from an older verified
		// owner. It does not promote that owner or copy their Calendar grant.
		owner, e := a.google.owner()
		if e != nil {
			return fail("Sign in with the intended Google account before exporting its identity.")
		}
		identity = OperatorIdentity{Schema: 1, GoogleSub: owner.Sub, Email: owner.Email}
		err = validateOperatorIdentity(identity)
	}
	if err != nil {
		return fail("No verified operator identity is available to export.")
	}
	if err = json.NewEncoder(stdout).Encode(identity); err != nil {
		return fail("Operator identity could not be written.")
	}
	return 0
}

func readOperatorConfiguration(input io.Reader) (OperatorIdentity, error) {
	var identity OperatorIdentity
	invalid := errors.New("invalid operator configuration")
	data, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil || len(data) == 0 || len(data) > 4096 {
		return identity, invalid
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&identity) != nil {
		return OperatorIdentity{}, invalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || validateOperatorIdentity(identity) != nil {
		return OperatorIdentity{}, invalid
	}
	return identity, nil
}
