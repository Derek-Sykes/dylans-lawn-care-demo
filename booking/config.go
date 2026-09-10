package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	PublicOrigin, AdminOrigin, AdminBasePath, DataDir, BootstrapToken, WebDir string
	DeploymentRunID, DeploymentRunAttempt                                     string
	ClientID, ClientSecret, OAuthMode                                         string
	AuthURL, TokenURL, UserInfoURL, CalendarURL, RevokeURL                    string
	HTTPTimeout                                                               time.Duration
}

func loadConfig() (Config, error) {
	c := Config{PublicOrigin: os.Getenv("PUBLIC_ORIGIN"), AdminOrigin: os.Getenv("ADMIN_ORIGIN"), AdminBasePath: os.Getenv("ADMIN_BASE_PATH"), DataDir: os.Getenv("DATA_DIR"), BootstrapToken: os.Getenv("BOOTSTRAP_TOKEN"), WebDir: os.Getenv("WEB_DIR"), ClientID: os.Getenv("GOOGLE_CLIENT_ID"), ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"), OAuthMode: os.Getenv("GOOGLE_OAUTH_MODE"), HTTPTimeout: 15 * time.Second}
	c.DeploymentRunID = os.Getenv("DEPLOYMENT_RUN_ID")
	c.DeploymentRunAttempt = os.Getenv("DEPLOYMENT_RUN_ATTEMPT")
	if c.DataDir == "" {
		c.DataDir = "/data"
	}
	if c.OAuthMode == "" {
		c.OAuthMode = "desktop"
	}
	if c.ClientID == "" && c.OAuthMode == "desktop" {
		// Desktop client identifiers are public configuration, never server credentials.
		var file struct {
			ClientID  string `json:"client_id"`
			Installed struct {
				ClientID string `json:"client_id"`
			} `json:"installed"`
		}
		if data, err := embedded.ReadFile("config/google-client.json"); err == nil {
			if err := json.Unmarshal(data, &file); err != nil {
				return c, errors.New("invalid Google client configuration")
			}
			c.ClientID = file.ClientID
			if c.ClientID == "" {
				c.ClientID = file.Installed.ClientID
			}
		}
	}
	c.AuthURL = "https://accounts.google.com/o/oauth2/v2/auth"
	c.TokenURL = "https://oauth2.googleapis.com/token"
	c.UserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
	c.CalendarURL = "https://www.googleapis.com/calendar/v3"
	c.RevokeURL = "https://oauth2.googleapis.com/revoke"
	return c, validateConfig(c)
}

func parseOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.HasSuffix(raw, "/") {
		return nil, errors.New("origins must be exact http(s) origins without a trailing slash")
	}
	if u.Scheme == "http" && !loopbackHost(u.Hostname()) {
		return nil, errors.New("unencrypted origins are allowed only on loopback")
	}
	return u, nil
}

func loopbackHost(host string) bool { ip := net.ParseIP(host); return ip != nil && ip.IsLoopback() }

func validateConfig(c Config) error {
	for _, value := range []string{c.DeploymentRunID, c.DeploymentRunAttempt} {
		if len(value) > 20 || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return errors.New("deployment identifiers must be numeric and at most 20 digits")
		}
	}
	if _, err := parseOrigin(c.PublicOrigin); err != nil {
		return err
	}
	admin, err := parseOrigin(c.AdminOrigin)
	if err != nil {
		return err
	}
	if c.AdminBasePath != "" && c.AdminBasePath != "/admin" {
		return errors.New("ADMIN_BASE_PATH must be empty or /admin")
	}
	if c.AdminBasePath != "" && (c.OAuthMode != "web" || admin.Scheme != "https") {
		return errors.New("an admin base path requires HTTPS web OAuth")
	}
	if c.PublicOrigin == c.AdminOrigin && c.AdminBasePath != "/admin" {
		return errors.New("a shared public and admin origin requires ADMIN_BASE_PATH=/admin")
	}
	if len(c.BootstrapToken) < 32 {
		return errors.New("BOOTSTRAP_TOKEN must contain at least 32 random characters")
	}
	if c.OAuthMode != "desktop" && c.OAuthMode != "web" {
		return errors.New("unsupported Google OAuth mode")
	}
	if c.OAuthMode == "desktop" && (!loopbackHost(admin.Hostname()) || admin.Scheme != "http") {
		return errors.New("desktop OAuth requires an http loopback admin origin")
	}
	if c.OAuthMode == "web" && (admin.Scheme != "https" || (c.ClientID != "" && c.ClientSecret == "")) {
		return errors.New("web OAuth requires HTTPS and private client credentials")
	}
	return nil
}

func (c Config) adminHomeURL() string { return c.AdminOrigin + c.AdminBasePath + "/" }
