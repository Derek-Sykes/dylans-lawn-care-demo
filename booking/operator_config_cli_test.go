package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func operatorCLI(t *testing.T, c Config, action, input string, want int) string {
	t.Helper()
	var out, diagnostic bytes.Buffer
	got := runOperatorConfigCommand([]string{"operator-config", action}, strings.NewReader(input), &out, &diagnostic, func() (Config, error) { return c, nil })
	if got != want {
		t.Fatalf("operator %s status %d, want %d: %s", action, got, want, diagnostic.String())
	}
	if strings.Contains(diagnostic.String(), "fixture-") || strings.Contains(diagnostic.String(), "private@example.com") {
		t.Fatal("operator diagnostic exposed private input")
	}
	if action != "export" && out.Len() != 0 {
		t.Fatal("operator status/import was not quiet")
	}
	return out.String()
}

func TestOperatorConfigurationCLIImportPersistenceAndNoOverwrite(t *testing.T) {
	a, _ := testApp(t)
	operatorCLI(t, a.cfg, "status", "", 3)
	input := `{"schema":1,"googleSub":"fixture-operator-id","email":"private@example.com"}`
	operatorCLI(t, a.cfg, "import", input, 0)
	operatorCLI(t, a.cfg, "import", input, 0)
	operatorCLI(t, a.cfg, "import", `{"schema":1,"email":"private@example.com"}`, 0)
	operatorCLI(t, a.cfg, "status", "", 0)
	operatorCLI(t, a.cfg, "import", strings.ReplaceAll(input, "fixture-operator-id", "fixture-other-id"), 1)
	var identity OperatorIdentity
	if json.Unmarshal([]byte(operatorCLI(t, a.cfg, "export", "", 0)), &identity) != nil || identity.GoogleSub != "fixture-operator-id" {
		t.Fatal("saved identity changed")
	}
	var encrypted []byte
	if err := a.store.db.QueryRow("SELECT value FROM secrets WHERE key='operator_identity'").Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte(identity.GoogleSub)) {
		t.Fatal("operator identity was not stored privately")
	}
}

func TestOperatorEmailPolicyDoesNotGrantAnIdentityBeforeGoogleSignIn(t *testing.T) {
	a, _ := testApp(t)
	operatorCLI(t, a.cfg, "import", `{"schema":1,"email":"private@example.com"}`, 0)
	operatorCLI(t, a.cfg, "status", "", 0)
	if a.identityRole("", "private@example.com") != "" || a.identityRole("unverified-sub", "private@example.com") != "" {
		t.Fatal("email-only configuration granted a role before Google verification")
	}
	if a.bootstrapAllowed() {
		t.Fatal("private policy left anonymous bootstrap enabled")
	}
}

func TestOperatorConfigurationRejectsCredentialsAndMalformedInput(t *testing.T) {
	a, _ := testApp(t)
	for _, input := range []string{
		``, `null`, `[]`, `{}`, `{"schema":2,"googleSub":"fixture-id","email":"private@example.com"}`,
		`{"schema":1,"googleSub":"fixture-id","email":"invalid"}`,
		`{"schema":1,"googleSub":"fixture-id","email":"private@example.com","refreshToken":"fixture-secret"}`,
		`{"schema":1,"googleSub":"fixture-id","email":"private@example.com"} {}`,
		strings.Repeat("x", 4097),
	} {
		operatorCLI(t, a.cfg, "import", input, 1)
		operatorCLI(t, a.cfg, "status", "", 3)
	}
}

func TestOperatorExportDoesNotExportTokensOrPromoteLegacyOwner(t *testing.T) {
	a, _ := testApp(t)
	if err := a.store.putSecret("owner", Owner{Sub: "fixture-legacy-id", Email: "private@example.com", CalendarID: "fixture-calendar"}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.putSecret("google_tokens", GoogleTokens{Access: "fixture-access-secret", Refresh: "fixture-refresh-secret"}); err != nil {
		t.Fatal(err)
	}
	exported := operatorCLI(t, a.cfg, "export", "", 0)
	if strings.Contains(exported, "secret") || strings.Contains(exported, "calendar") {
		t.Fatal("export contained credentials or Calendar state")
	}
	operatorCLI(t, a.cfg, "status", "", 3)
	var identity OperatorIdentity
	if json.Unmarshal([]byte(exported), &identity) != nil || identity.GoogleSub != "fixture-legacy-id" {
		t.Fatal("legacy identity export failed")
	}
}
