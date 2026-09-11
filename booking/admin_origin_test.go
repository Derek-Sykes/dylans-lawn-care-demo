package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sharedOriginConfig(t *testing.T) Config {
	t.Helper()
	c := testConfig(t.TempDir())
	c.PublicOrigin = "https://dev-demo.xsolutionsmd.com"
	c.AdminOrigin = c.PublicOrigin
	c.AdminBasePath = "/admin"
	c.OAuthMode = "web"
	c.ClientSecret = "fixture-web-client-secret"
	return c
}

func TestAdminOriginConfigurationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*Config)
		valid bool
	}{
		{"explicit shared HTTPS admin path", func(c *Config) {}, true},
		{"shared origin without path", func(c *Config) { c.AdminBasePath = "" }, false},
		{"shared loopback desktop", func(c *Config) {
			c.PublicOrigin = "http://127.0.0.1:4177"
			c.AdminOrigin = c.PublicOrigin
			c.OAuthMode = "desktop"
		}, false},
		{"shared HTTP web", func(c *Config) { c.PublicOrigin = "http://127.0.0.1:4177"; c.AdminOrigin = c.PublicOrigin }, false},
		{"trailing slash path", func(c *Config) { c.AdminBasePath = "/admin/" }, false},
		{"different path", func(c *Config) { c.AdminBasePath = "/owner" }, false},
		{"path traversal", func(c *Config) { c.AdminBasePath = "/admin/../" }, false},
		{"external path", func(c *Config) { c.AdminBasePath = "//attacker.invalid" }, false},
		{"query in path", func(c *Config) { c.AdminBasePath = "/admin?next=/" }, false},
		{"missing web secret", func(c *Config) { c.ClientSecret = "" }, false},
		{"unconfigured web candidate", func(c *Config) { c.ClientID = ""; c.ClientSecret = "" }, true},
		{"deployment run markers", func(c *Config) { c.DeploymentRunID = "34533015161"; c.DeploymentRunAttempt = "2" }, true},
		{"unexpected deployment marker", func(c *Config) { c.DeploymentRunID = "not-a-public-run-id" }, false},
		{"separate HTTPS origins", func(c *Config) { c.AdminOrigin = "https://owner.example.com"; c.AdminBasePath = "" }, true},
		{"existing separate desktop origins", func(c *Config) { *c = testConfig(c.DataDir) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := sharedOriginConfig(t)
			tc.edit(&c)
			if err := validateConfig(c); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestLoadWebConfigDoesNotUseDesktopClientFallback(t *testing.T) {
	c := sharedOriginConfig(t)
	for key, value := range map[string]string{
		"PUBLIC_ORIGIN": c.PublicOrigin, "ADMIN_ORIGIN": c.AdminOrigin,
		"ADMIN_BASE_PATH": c.AdminBasePath, "BOOTSTRAP_TOKEN": c.BootstrapToken,
		"GOOGLE_OAUTH_MODE": "web", "GOOGLE_CLIENT_ID": "", "GOOGLE_CLIENT_SECRET": "",
		"DEPLOYMENT_RUN_ID": "34533015161", "DEPLOYMENT_RUN_ATTEMPT": "2",
	} {
		t.Setenv(key, value)
	}
	got, err := loadConfig()
	if err != nil || got.ClientID != "" || got.AdminBasePath != "/admin" {
		t.Fatalf("unconfigured web installation loaded desktop credentials or failed: %v", err)
	}
	if got.DeploymentRunID != "34533015161" || got.DeploymentRunAttempt != "2" {
		t.Fatal("deployment run metadata not loaded")
	}
	t.Setenv("ADMIN_ORIGIN", "http://127.0.0.1:4178")
	t.Setenv("PUBLIC_ORIGIN", "http://127.0.0.1:4177")
	t.Setenv("ADMIN_BASE_PATH", "")
	t.Setenv("GOOGLE_OAUTH_MODE", "desktop")
	got, err = loadConfig()
	if err != nil || got.ClientID == "" || got.ClientSecret != "" {
		t.Fatalf("desktop public client fallback changed: %v", err)
	}
}

func TestSharedOriginAdminPageAndAuthentication(t *testing.T) {
	a, err := newApp(sharedOriginConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.store.close() })
	w := request(a, true, "GET", "/admin?google=connected", nil, nil, "", "")
	if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != "/admin/?google=connected" {
		t.Fatal("admin entry did not canonicalize to the portal while keeping OAuth outcome")
	}
	w = request(a, true, "GET", "/admin/", nil, nil, "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Booking portal") || !strings.Contains(w.Body.String(), `href="./" aria-label="Booking portal home"`) {
		t.Fatal("admin page or relative portal navigation missing")
	}
	for _, path := range []string{"/admin.css", "/admin.js"} {
		if w = request(a, true, "GET", path, nil, nil, "", ""); w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("portal asset unavailable: %s", path)
		}
	}
	for _, path := range []string{"/", "/index.html", "/admin/unexpected", "/config/google-client.json", "/data/booking.sqlite"} {
		if w = request(a, true, "GET", path, nil, nil, "", ""); w.Code != 404 {
			t.Fatalf("unexpected admin route exposed: %s", path)
		}
	}
	for _, path := range []string{"/admin/", "/admin.js", "/api/admin/settings", "/oauth/callback"} {
		if w = request(a, false, "GET", path, nil, nil, "", ""); w.Code != 404 {
			t.Fatalf("public listener exposed admin route: %s", path)
		}
	}
	if w = request(a, true, "GET", "/api/admin/settings", nil, nil, "", ""); w.Code != 401 {
		t.Fatal("shared origin bypassed owner authentication")
	}
	cookie, csrf := bootstrap(t, a)
	if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || !strings.HasPrefix(cookie.Name, "__Host-") {
		t.Fatal("shared HTTPS origin lost secure host-bound owner cookie")
	}
	if w = request(a, true, "PUT", "/api/admin/settings", defaultSettings(), cookie, "", a.cfg.AdminOrigin); w.Code != 403 {
		t.Fatal("shared origin bypassed CSRF validation")
	}
	if w = request(a, true, "PUT", "/api/admin/settings", defaultSettings(), cookie, csrf, "https://attacker.invalid"); w.Code != 403 {
		t.Fatal("shared origin accepted another origin")
	}
	if w = request(a, true, "PUT", "/api/admin/settings", defaultSettings(), cookie, csrf, a.cfg.AdminOrigin); w.Code != 200 {
		t.Fatal("valid shared-origin owner update failed")
	}
}

func TestWebOAuthReturnsToAdminOnSuccessAndFailure(t *testing.T) {
	a, err := newApp(sharedOriginConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.store.close() })
	f := mockGoogle(t, a)
	cookie, csrf := bootstrap(t, a)
	q, binding := beginOAuth(t, a, cookie, csrf)
	if q.Get("redirect_uri") != a.cfg.AdminOrigin+"/oauth/callback" || !binding.Secure {
		t.Fatal("web OAuth callback or secure binding changed")
	}
	w := callback(a, q, binding)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != a.cfg.AdminOrigin+"/admin/?google=connected" || f.lastSecret != a.cfg.ClientSecret {
		t.Fatal("web OAuth failed to return to owner portal using server credentials")
	}
	w = callback(a, q, binding)
	if w.Header().Get("Location") != a.cfg.AdminOrigin+"/admin/?google=failed" || f.tokenCalls != 1 {
		t.Fatal("replayed OAuth callback reached provider or escaped owner portal")
	}
	cookie, csrf = testIdentitySession(t, a, Owner{Sub: "owner-sub", Email: "owner@example.com"})
	q, binding = beginOAuth(t, a, cookie, csrf)
	w = request(a, true, "GET", "/oauth/callback?error=access_denied&state="+q.Get("state"), nil, binding, "", "")
	if w.Header().Get("Location") != a.cfg.AdminOrigin+"/admin/?google=denied" || f.tokenCalls != 1 {
		t.Fatal("declined OAuth callback did not return safely to owner portal")
	}
}

func TestBackendVersionIsPublicMetadataWithHostBoundary(t *testing.T) {
	c := sharedOriginConfig(t)
	c.DeploymentRunID, c.DeploymentRunAttempt = "34533015161", "2"
	a, err := newApp(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.store.close() })
	for _, admin := range []bool{false, true} {
		w := request(a, admin, "GET", "/version.json", nil, nil, "", "")
		var body map[string]string
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body) != 3 || body["revision"] != revision || body["deploymentRunId"] != c.DeploymentRunID || body["deploymentRunAttempt"] != c.DeploymentRunAttempt {
			t.Fatalf("incorrect version metadata (admin=%v)", admin)
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal("version response lost safe metadata headers")
		}
		r := httptest.NewRequest("GET", "https://attacker.invalid/version.json", nil)
		w = httptest.NewRecorder()
		handler := a.publicHandler()
		if admin {
			handler = a.adminHandler()
		}
		handler.ServeHTTP(w, r)
		if w.Code != 421 {
			t.Fatal("version response bypassed configured Host boundary")
		}
	}
}
