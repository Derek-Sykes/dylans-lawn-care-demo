package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestPrivateClientConfigurationAuthorizationAndPersistence(t *testing.T) {
	a, _ := testApp(t)
	cookie, csrf := bootstrap(t, a)
	in := GoogleClientConfig{a.cfg.ClientID, "fixture-client-secret-only"}
	for _, tc := range []struct {
		auth, csrf bool
		origin     string
		status     int
	}{
		{false, false, a.cfg.AdminOrigin, 401},
		{true, false, a.cfg.AdminOrigin, 403},
		{true, true, "https://other.invalid", 403},
		{true, true, "", 403},
	} {
		c := cookie
		if !tc.auth {
			c = nil
		}
		token := ""
		if tc.csrf {
			token = csrf
		}
		w := request(a, true, "POST", "/api/admin/google/configure", in, c, token, tc.origin)
		if w.Code != tc.status {
			t.Fatalf("configuration authorization status %d, wanted %d", w.Code, tc.status)
		}
	}
	for _, invalid := range []GoogleClientConfig{
		{"other.apps.googleusercontent.com", in.ClientSecret},
		{a.cfg.ClientID, ""}, {a.cfg.ClientID, "has spaces"},
		{a.cfg.ClientID, strings.Repeat("a", 513)}, {a.cfg.ClientID, "embedded\nnewline"},
	} {
		w := request(a, true, "POST", "/api/admin/google/configure", invalid, cookie, csrf, a.cfg.AdminOrigin)
		if w.Code != 400 || strings.Contains(w.Body.String(), in.ClientSecret) {
			t.Fatal("invalid configuration was accepted or echoed")
		}
	}
	if !a.google.requiresClientConfiguration() {
		t.Fatal("missing client configuration was not reported")
	}
	_ = a.store.putJSON("google_error", clientConfigurationError)
	w := request(a, true, "POST", "/api/admin/google/configure", in, cookie, csrf, a.cfg.AdminOrigin)
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal("valid client configuration failed or returned a body")
	}
	if a.google.requiresClientConfiguration() || a.google.connection().Error != "" {
		t.Fatal("saved client configuration did not clear setup requirement")
	}
	var raw []byte
	if err := a.store.db.QueryRow("SELECT value FROM secrets WHERE key='google_client_config'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(in.ClientSecret)) || bytes.Contains(raw, []byte(in.ClientID)) {
		t.Fatal("client configuration stored in plaintext")
	}
	for _, auth := range []bool{false, true} {
		c := cookie
		if !auth {
			c = nil
		}
		w = request(a, true, "GET", "/api/admin/session", nil, c, "", "")
		if strings.Contains(w.Body.String(), in.ClientSecret) {
			t.Fatal("session exposed private client configuration")
		}
		var body struct {
			Google map[string]any `json:"google"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		value, present := body.Google["requiresClientConfiguration"]
		if auth && (!present || value != false) {
			t.Fatal("authenticated configuration state missing")
		}
		if !auth && present {
			t.Fatal("anonymous configuration details exposed")
		}
	}
	other, err := newApp(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.store.close()
	if secret, err := other.google.clientSecret(); err != nil || secret != in.ClientSecret {
		t.Fatal("encrypted configuration did not survive restart")
	}
	f := mockGoogle(t, other)
	if _, err := other.google.tokenRequest(context.Background(), url.Values{"grant_type": {"authorization_code"}}); err != nil || f.lastSecret != in.ClientSecret {
		t.Fatal("token request did not load saved private configuration")
	}
	other.google.cfg.ClientSecret = "fixture-environment-precedence"
	if _, err := other.google.tokenRequest(context.Background(), url.Values{"grant_type": {"authorization_code"}}); err != nil || f.lastSecret != other.google.cfg.ClientSecret {
		t.Fatal("explicit environment configuration did not take precedence")
	}
	_ = a.store.putJSON("google_error", "A separate calendar issue")
	w = request(a, true, "POST", "/api/admin/google/configure", in, cookie, csrf, a.cfg.AdminOrigin)
	if w.Code != 204 || a.google.connection().Error != "A separate calendar issue" {
		t.Fatal("configuration import cleared an unrelated Calendar diagnostic")
	}
}

func TestPrivateClientConfigurationRejectsWebModeAndDifferentStoredClient(t *testing.T) {
	a, _ := testApp(t)
	if err := a.store.putSecret("google_client_config", GoogleClientConfig{"other.apps.googleusercontent.com", "fixture-secret"}); err != nil {
		t.Fatal(err)
	}
	if secret, err := a.google.clientSecret(); err != nil || secret != "" || !a.google.requiresClientConfiguration() {
		t.Fatal("another app's private configuration was used")
	}
	cookie, csrf := bootstrap(t, a)
	a.cfg.OAuthMode = "web"
	w := request(a, true, "POST", "/api/admin/google/configure", GoogleClientConfig{a.cfg.ClientID, "fixture-secret"}, cookie, csrf, a.cfg.AdminOrigin)
	if w.Code != 409 {
		t.Fatal("private desktop import allowed in web mode")
	}
}
