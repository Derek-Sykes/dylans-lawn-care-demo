package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxGoogleConfigurationBytes = 64 << 10

// This command deliberately uses no listeners, workers or provider requests.
// The launcher streams credentials on stdin; neither results nor errors echo it.
func runGoogleConfigCommand(args []string, input io.Reader, stderr io.Writer, load func() (Config, error)) int {
	fail := func(message string) int { fmt.Fprintln(stderr, message); return 1 }
	if len(args) != 2 || args[0] != "google-config" || (args[1] != "status" && args[1] != "import") {
		return fail("Use booking google-config status or booking google-config import.")
	}
	config, err := load()
	if err != nil {
		return fail("Booking configuration is unavailable. Check this installation's setup.")
	}
	app, err := newApp(config)
	if err != nil {
		return fail("Booking storage is unavailable. Check the persistent volume and encryption key.")
	}
	defer app.store.close()
	if args[1] == "status" {
		if config.OAuthMode == "web" {
			return 0
		}
		secret, err := app.google.clientSecret()
		if err != nil {
			return fail("Private Google configuration could not be read. Check this installation's setup.")
		}
		if secret == "" {
			return 3
		}
		if !validClientConfiguration(GoogleClientConfig{config.ClientID, secret}, config.ClientID) {
			return fail("Private Google configuration needs attention in the owner portal.")
		}
		return 0
	}
	if config.OAuthMode != "desktop" {
		return fail("Private desktop configuration cannot be imported in web mode.")
	}
	in, err := readGoogleConfiguration(input)
	if err != nil || !validClientConfiguration(in, config.ClientID) {
		return fail("Private Google configuration is invalid or does not match this installation.")
	}
	if config.ClientSecret != "" {
		if !validClientConfiguration(GoogleClientConfig{config.ClientID, config.ClientSecret}, config.ClientID) {
			return fail("Existing private environment configuration needs attention.")
		}
		return 0
	}
	if err := app.store.importGoogleConfiguration(in); err != nil {
		return fail("Private Google configuration could not be saved. Existing configuration was preserved.")
	}
	return 0
}

func readGoogleConfiguration(input io.Reader) (GoogleClientConfig, error) {
	invalid := errors.New("invalid private Google configuration")
	data, err := io.ReadAll(io.LimitReader(input, maxGoogleConfigurationBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxGoogleConfigurationBytes {
		return GoogleClientConfig{}, invalid
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var file struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Installed    *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"installed"`
	}
	// Google files contain extra public metadata. json.Unmarshal accepts those
	// fields but rejects malformed JSON and every trailing JSON value.
	if json.Unmarshal(data, &file) != nil {
		return GoogleClientConfig{}, invalid
	}
	if file.Installed != nil {
		return GoogleClientConfig{file.Installed.ClientID, file.Installed.ClientSecret}, nil
	}
	return GoogleClientConfig{file.ClientID, file.ClientSecret}, nil
}

func (s *Store) importGoogleConfiguration(in GoogleClientConfig) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	b, err = s.seal(b, "google_client_config")
	if err != nil {
		return err
	}
	// SQLite decides the winner atomically across concurrent launcher processes.
	// ON CONFLICT never replaces an existing private configuration record.
	result, err := s.db.Exec("INSERT INTO secrets(key,value) VALUES('google_client_config',?) ON CONFLICT(key) DO NOTHING", b)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		var existing GoogleClientConfig
		if err := s.getSecret("google_client_config", &existing); err != nil {
			return err
		}
		if !validClientConfiguration(existing, in.ClientID) {
			return errors.New("existing private configuration requires manual attention")
		}
	}
	// Clear only the setup diagnostic; unrelated Calendar problems remain.
	message, _ := json.Marshal(clientConfigurationError)
	_, err = s.db.Exec("UPDATE meta SET value=? WHERE key='google_error' AND value=?", []byte(`""`), message)
	return err
}
