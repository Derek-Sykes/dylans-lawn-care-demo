package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func runConfigCLI(t *testing.T, config Config, action, input string, want int) {
	t.Helper()
	var diagnostic bytes.Buffer
	code := runGoogleConfigCommand([]string{"google-config", action}, strings.NewReader(input), &diagnostic, func() (Config, error) { return config, nil })
	if code != want {
		t.Fatalf("configuration command %s returned %d, want %d", action, code, want)
	}
	if code != 1 && diagnostic.Len() != 0 {
		t.Fatal("successful/status command was not quiet")
	}
	if strings.Contains(diagnostic.String(), "fixture-") || strings.Contains(diagnostic.String(), "client_secret") || strings.Contains(diagnostic.String(), config.ClientID) {
		t.Fatal("configuration diagnostic echoed private input")
	}
}
func configFile(in GoogleClientConfig, installed bool) string {
	v := map[string]any{"client_id": in.ClientID, "client_secret": in.ClientSecret, "project_id": "public-fixture-project"}
	if installed {
		b, _ := json.Marshal(map[string]any{"installed": v})
		return string(b)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestGoogleConfigCLIImportsBothFormatsAndSurvivesRestart(t *testing.T) {
	for _, installed := range []bool{false, true} {
		a, _ := testApp(t)
		in := GoogleClientConfig{a.cfg.ClientID, "fixture-private-import-value"}
		runConfigCLI(t, a.cfg, "status", "", 3)
		_ = a.store.putJSON("google_error", clientConfigurationError)
		runConfigCLI(t, a.cfg, "import", configFile(in, installed), 0)
		runConfigCLI(t, a.cfg, "status", "", 0)
		var saved GoogleClientConfig
		if err := a.store.getSecret("google_client_config", &saved); err != nil || saved != in {
			t.Fatal("private CLI import did not persist")
		}
		var encrypted []byte
		if err := a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_client_config'").Scan(&encrypted); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(encrypted, []byte(in.ClientSecret)) || bytes.Contains(encrypted, []byte(in.ClientID)) {
			t.Fatal("CLI stored private configuration in plaintext")
		}
		if a.google.connection().Error != "" {
			t.Fatal("CLI import did not clear the setup diagnostic")
		}
		other, err := newApp(a.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if secret, err := other.google.clientSecret(); err != nil || secret != in.ClientSecret {
			t.Fatal("CLI import did not survive restart")
		}
		other.store.close()
	}
}

func TestGoogleConfigCLIRejectsInvalidInputWithoutEcho(t *testing.T) {
	a, _ := testApp(t)
	valid := GoogleClientConfig{a.cfg.ClientID, "fixture-private-import-value"}
	for _, input := range []string{
		"", "null", "[]", "fixture-private-malformed-json",
		configFile(GoogleClientConfig{"other.apps.googleusercontent.com", valid.ClientSecret}, true),
		configFile(GoogleClientConfig{a.cfg.ClientID, "short"}, false),
		configFile(GoogleClientConfig{a.cfg.ClientID, "fixture secret"}, false),
		configFile(valid, true) + " {}", configFile(valid, true) + " trailing",
		configFile(valid, false) + strings.Repeat(" ", maxGoogleConfigurationBytes),
	} {
		runConfigCLI(t, a.cfg, "import", input, 1)
		runConfigCLI(t, a.cfg, "status", "", 3)
	}
	// The exact byte limit accepts JSON plus legal whitespace; larger input fails.
	input := configFile(valid, true)
	input += strings.Repeat(" ", maxGoogleConfigurationBytes-len(input))
	runConfigCLI(t, a.cfg, "import", input, 0)
	var diagnostic bytes.Buffer
	if code := runGoogleConfigCommand([]string{"google-config", "status"}, strings.NewReader(""), &diagnostic, func() (Config, error) { return Config{}, errors.New("fixture-secret-error-value") }); code != 1 || strings.Contains(diagnostic.String(), "fixture-") {
		t.Fatal("configuration loading failure exposed sensitive detail")
	}
	diagnostic.Reset()
	if code := runGoogleConfigCommand([]string{"google-config", "fixture-secret-argument"}, strings.NewReader(""), &diagnostic, func() (Config, error) { return a.cfg, nil }); code != 1 || strings.Contains(diagnostic.String(), "fixture-") {
		t.Fatal("unknown command echoed arguments")
	}
}

func TestGoogleConfigCLIPreservesExistingCredentialsAndOwnerState(t *testing.T) {
	a, _ := testApp(t)
	first := GoogleClientConfig{a.cfg.ClientID, "fixture-first-private-secret"}
	replacement := GoogleClientConfig{a.cfg.ClientID, "fixture-second-private-secret"}
	if err := a.store.putSecret("google_client_config", first); err != nil {
		t.Fatal(err)
	}
	_ = a.store.putSecret("owner", Owner{"fixture-owner", "fixture@example.com", "fixture-calendar"})
	_ = a.store.putSecret("google_tokens", GoogleTokens{"fixture-access", "fixture-refresh", a.now().Add(time.Hour), googleScopes})
	bootstrap(t, a)
	snapshot := func() map[string][]byte {
		t.Helper()
		out := map[string][]byte{}
		for _, key := range []string{"owner", "google_tokens", "google_client_config"} {
			var b []byte
			if err := a.store.db.QueryRow("SELECT value FROM secrets WHERE key=?", key).Scan(&b); err != nil {
				t.Fatal(err)
			}
			out[key] = b
		}
		var session []byte
		if err := a.store.db.QueryRow("SELECT value FROM sessions LIMIT 1").Scan(&session); err != nil {
			t.Fatal(err)
		}
		out["session"] = session
		return out
	}
	before := snapshot()
	_ = a.store.putJSON("google_error", "A separate Calendar problem")
	runConfigCLI(t, a.cfg, "import", configFile(replacement, true), 0)
	runConfigCLI(t, a.cfg, "status", "", 0)
	for key, after := range snapshot() {
		if !bytes.Equal(before[key], after) {
			t.Fatal("CLI import changed existing credentials, owner or session")
		}
	}
	if a.google.connection().Error != "A separate Calendar problem" {
		t.Fatal("CLI import erased an unrelated diagnostic")
	}
	b, _ := testApp(t)
	env := b.cfg
	env.ClientSecret = "fixture-environment-secret"
	runConfigCLI(t, env, "status", "", 0)
	runConfigCLI(t, env, "import", configFile(GoogleClientConfig{env.ClientID, replacement.ClientSecret}, true), 0)
	var count int
	_ = b.store.db.QueryRow("SELECT count(*) FROM secrets WHERE key='google_client_config'").Scan(&count)
	if count != 0 {
		t.Fatal("CLI import wrote a record despite existing environment credentials")
	}
	web := b.cfg
	web.OAuthMode = "web"
	web.AdminOrigin = "https://owner.example.com"
	web.ClientSecret = "fixture-web-secret"
	runConfigCLI(t, web, "status", "", 0)
	runConfigCLI(t, web, "import", configFile(replacement, true), 1)
}

func TestGoogleConfigCLIConcurrentImportsNeverOverwrite(t *testing.T) {
	a, _ := testApp(t)
	const count = 8
	var wg sync.WaitGroup
	codes := make(chan int, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var diagnostic bytes.Buffer
			input := configFile(GoogleClientConfig{a.cfg.ClientID, fmt.Sprintf("fixture-concurrent-secret-%d", i)}, true)
			codes <- runGoogleConfigCommand([]string{"google-config", "import"}, strings.NewReader(input), &diagnostic, func() (Config, error) { return a.cfg, nil })
		}(i)
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 0 {
			t.Fatal("concurrent import failed")
		}
	}
	var first GoogleClientConfig
	if err := a.store.getSecret("google_client_config", &first); err != nil || !strings.HasPrefix(first.ClientSecret, "fixture-concurrent-secret-") {
		t.Fatal("concurrent imports did not save one valid configuration")
	}
	for i := 0; i < count; i++ {
		runConfigCLI(t, a.cfg, "import", configFile(GoogleClientConfig{a.cfg.ClientID, fmt.Sprintf("fixture-later-secret-%d", i)}, false), 0)
	}
	var final GoogleClientConfig
	_ = a.store.getSecret("google_client_config", &final)
	if first != final {
		t.Fatal("later import overwrote the first configuration")
	}
}
