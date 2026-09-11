package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

//go:embed web/index.html web/admin.css web/admin.js config/google-client.json
var embedded embed.FS

// Set from the same source revision as the container's OCI label at build time.
var revision = "local"

func (a *App) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"revision": revision, "deploymentRunId": a.cfg.DeploymentRunID, "deploymentRunAttempt": a.cfg.DeploymentRunAttempt})
}

type App struct {
	cfg                   Config
	store                 *Store
	google                *Google
	calendar              Calendar
	now                   func() time.Time
	secureAdmin           bool
	publicHost, adminHost string
	bookingMu, workerMu   sync.Mutex
	wake                  chan struct{}
	rate                  *rateLimiter
}

func newApp(c Config) (*App, error) {
	if err := validateConfig(c); err != nil {
		return nil, err
	}
	s, err := openStore(c.DataDir, c.BootstrapToken)
	if err != nil {
		return nil, err
	}
	if c.OperatorGoogleEmail != "" {
		if err = s.importOperatorIdentity(OperatorIdentity{Schema: 1, GoogleSub: c.OperatorGoogleSub, Email: c.OperatorGoogleEmail}); err != nil {
			s.close()
			return nil, err
		}
	}
	a := &App{cfg: c, store: s, now: time.Now, wake: make(chan struct{}, 1), rate: &rateLimiter{entries: map[string]rateEntry{}}}
	pub, _ := url.Parse(c.PublicOrigin)
	admin, _ := url.Parse(c.AdminOrigin)
	a.publicHost = pub.Host
	a.adminHost = admin.Host
	a.secureAdmin = admin.Scheme == "https"
	if c.HTTPTimeout == 0 {
		c.HTTPTimeout = 15 * time.Second
	}
	a.google = &Google{cfg: c, store: s, client: &http.Client{Timeout: c.HTTPTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}, now: func() time.Time { return a.now() }}
	a.calendar = a.google
	return a, nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.Status, map[string]string{"error": ae.Message, "code": ae.Code})
		return
	}
	writeJSON(w, 500, map[string]string{"error": "The request could not be completed. Please try again.", "code": "internal_error"})
}
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, &apiError{415, "json_required", "Send this request as JSON."})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(v); err != nil {
		writeError(w, &apiError{400, "invalid_json", "Please check the submitted fields."})
		return false
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		writeError(w, &apiError{400, "invalid_json", "Please submit one JSON object."})
		return false
	}
	return true
}

type rateEntry struct {
	start time.Time
	count int
}
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]rateEntry
}

func (l *rateLimiter) allow(key string, limit int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) > 2048 {
		for k, v := range l.entries {
			if now.Sub(v.start) > time.Minute {
				delete(l.entries, k)
			}
		}
		if len(l.entries) > 2048 {
			return false
		}
	}
	v := l.entries[key]
	if now.Sub(v.start) >= time.Minute {
		v = rateEntry{start: now}
	}
	v.count++
	l.entries[key] = v
	return v.count <= limit
}
func (a *App) middleware(next http.Handler, admin bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if r.URL.Path == "/healthz" && r.Method == "GET" {
			if err := a.store.db.PingContext(r.Context()); err != nil {
				writeJSON(w, 503, map[string]string{"status": "unavailable"})
			} else {
				writeJSON(w, 200, map[string]string{"status": "ok"})
			}
			return
		}
		origin, host := a.cfg.PublicOrigin, a.publicHost
		if admin {
			origin, host = a.cfg.AdminOrigin, a.adminHost
		}
		if !strings.EqualFold(r.Host, host) {
			writeError(w, &apiError{421, "invalid_host", "This address is not configured for this application."})
			return
		}
		if from := r.Header.Get("Origin"); from != "" && from != origin {
			writeError(w, &apiError{403, "invalid_origin", "This request came from a different website."})
			return
		}
		write := r.Method != "GET" && r.Method != "HEAD"
		if write && r.Header.Get("Origin") != origin {
			writeError(w, &apiError{403, "invalid_origin", "This request needs the application's original page."})
			return
		}
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		group, limit := "read", 180
		if write {
			group, limit = "write", 30
		}
		if r.URL.Path == "/api/admin/bootstrap" || r.URL.Path == "/api/admin/google/connect" || r.URL.Path == "/api/admin/signin" || r.URL.Path == "/api/admin/invitations/accept" {
			group, limit = "auth", 10
		}
		if r.URL.Path == "/api/public/bookings" {
			group, limit = "booking", 15
		}
		if !a.rate.allow(ip+":"+group, limit, a.now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, &apiError{429, "rate_limited", "Too many requests. Please wait a minute and try again."})
			return
		}
		if admin && strings.HasPrefix(r.URL.Path, "/api/admin/") && r.URL.Path != "/api/admin/session" && r.URL.Path != "/api/admin/bootstrap" && r.URL.Path != "/api/admin/signin" && r.URL.Path != "/api/admin/invitations/accept" {
			s, _, err := a.session(r)
			if err != nil {
				writeError(w, &apiError{401, "sign_in_required", "Please sign in to the owner portal."})
				return
			}
			if err == nil && write && !constantEqual(s.CSRF, r.Header.Get("X-CSRF-Token")) {
				writeError(w, &apiError{403, "invalid_csrf", "Refresh the owner portal and try again."})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) publicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /version.json", a.handleVersion)
	mux.HandleFunc("GET /api/public/config", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.store.settings()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"businessName": v.BusinessName, "timeZone": v.TimeZone, "slotMinutes": v.SlotMinutes, "estimateMinutes": v.EstimateMinutes, "bufferMinutes": v.BufferMinutes, "externalBufferMinutes": externalBufferMinutes(v), "minNoticeHours": v.MinNoticeHours, "horizonDays": v.HorizonDays, "services": services, "bookingEnabled": a.calendar.Connected()})
	})
	mux.HandleFunc("GET /api/public/slots", func(w http.ResponseWriter, r *http.Request) {
		slots, v, err := a.availableSlots(r.Context(), r.URL.Query().Get("date"), r.URL.Query().Get("kind"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"date": r.URL.Query().Get("date"), "timeZone": v.TimeZone, "slots": slots})
	})
	mux.HandleFunc("POST /api/public/bookings", func(w http.ResponseWriter, r *http.Request) {
		var in BookingInput
		if !readJSON(w, r, &in) {
			return
		}
		b, created, err := a.createBooking(r.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		status := 200
		if created {
			status = 202
		}
		message := "Your service appointment request was received. Dylan will follow up about the job details."
		if b.CalendarStatus != "synced" {
			message = "Your service appointment request was saved. Its calendar update is not yet complete; Dylan will follow up about the job details."
		}
		if b.Kind == "estimate" {
			message = "Your estimate or callback request was saved. Check its request and calendar status for confirmation."
		}
		writeJSON(w, status, map[string]any{"id": b.ID, "kind": b.Kind, "status": b.Status, "calendarStatus": b.CalendarStatus, "start": b.Start, "end": b.End, "message": message})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, &apiError{404, "not_found", "Page not found."})
	})
	return a.middleware(mux, false)
}
func (a *App) adminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /version.json", a.handleVersion)
	mux.HandleFunc("GET /api/admin/session", a.handleSession)
	mux.HandleFunc("POST /api/admin/bootstrap", a.handleBootstrap)
	mux.HandleFunc("POST /api/admin/signin", a.handleSignIn)
	mux.HandleFunc("GET /api/admin/invitations", a.handleInvitations)
	mux.HandleFunc("POST /api/admin/invitations", a.handleInvitationCreate)
	mux.HandleFunc("DELETE /api/admin/invitations/{id}", a.handleInvitationRevoke)
	mux.HandleFunc("POST /api/admin/invitations/accept", a.handleInvitationAccept)
	mux.HandleFunc("GET /api/admin/members", a.handleMembers)
	mux.HandleFunc("DELETE /api/admin/members/{id}", a.handleMemberRemove)
	mux.HandleFunc("POST /api/admin/logout", a.handleLogout)
	mux.HandleFunc("POST /api/admin/google/connect", a.handleConnect)
	mux.HandleFunc("POST /api/admin/google/configure", a.handleGoogleConfigure)
	mux.HandleFunc("GET /oauth/callback", a.handleCallback)
	mux.HandleFunc("POST /api/admin/google/disconnect", func(w http.ResponseWriter, r *http.Request) {
		s, _, _ := a.session(r)
		if !a.canConnectCalendar(s) {
			writeError(w, &apiError{403, "calendar_owner_required", "Only the calendar owner can change this connection."})
			return
		}
		if err := a.google.disconnect(r.Context()); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /api/admin/settings", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.store.settings()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("PUT /api/admin/settings", func(w http.ResponseWriter, r *http.Request) {
		var v Settings
		if !readJSON(w, r, &v) {
			return
		}
		v, err := a.saveSettings(v)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/admin/bookings", func(w http.ResponseWriter, r *http.Request) {
		status := r.URL.Query().Get("status")
		if status != "" && status != "needs_followup" && status != "contacted" && status != "confirmed" && status != "cancelled" {
			writeError(w, &apiError{400, "invalid_status", "Choose a valid booking filter."})
			return
		}
		calendarSyncError := ""
		if err := a.refreshCalendar(r.Context(), true); err != nil {
			calendarSyncError = calendarRefreshMessage
		}
		query := "SELECT " + bookingColumns + " FROM bookings"
		args := []any{}
		conditions := []string{}
		if status != "" {
			conditions = append(conditions, "status=?")
			args = append(args, status)
		}
		if kind := r.URL.Query().Get("kind"); kind != "" {
			if _, err := bookingKind(kind); err != nil {
				writeError(w, err)
				return
			}
			conditions = append(conditions, "kind=?")
			args = append(args, kind)
		}
		if len(conditions) > 0 {
			query += " WHERE " + strings.Join(conditions, " AND ")
		}
		query += " ORDER BY created_at DESC"
		rows, err := a.store.db.Query(query, args...)
		if err != nil {
			writeError(w, err)
			return
		}
		defer rows.Close()
		bookings := []Booking{}
		for rows.Next() {
			b, err := scanBooking(rows)
			if err != nil {
				writeError(w, err)
				return
			}
			bookings = append(bookings, b)
		}
		if err = rows.Err(); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"bookings": bookings, "calendarSyncError": calendarSyncError})
	})
	mux.HandleFunc("PATCH /api/admin/bookings/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Status     *string `json:"status"`
			AdminNotes *string `json:"adminNotes"`
		}
		if !readJSON(w, r, &in) {
			return
		}
		if in.Status == nil && in.AdminNotes == nil {
			writeError(w, &apiError{400, "empty_update", "Choose a status or enter a note."})
			return
		}
		b, err := a.patchBooking(r.PathValue("id"), in.Status, in.AdminNotes)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, b)
	})
	mux.HandleFunc("POST /api/admin/bookings/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		result, err := a.store.db.Exec("UPDATE bookings SET calendar_status='pending',attempts=0,next_attempt=0 WHERE id=? AND calendar_status!='synced'", r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		_, _ = result.RowsAffected()
		b, err := a.store.booking(r.PathValue("id"))
		if err != nil {
			writeError(w, &apiError{404, "not_found", "Booking not found."})
			return
		}
		a.wakeWorker()
		writeJSON(w, 200, b)
	})
	var files fs.FS
	files, _ = fs.Sub(embedded, "web")
	if a.cfg.WebDir != "" {
		files = os.DirFS(a.cfg.WebDir)
	}
	assets := http.FileServer(http.FS(files))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			writeError(w, &apiError{405, "method_not_allowed", "This page is read-only."})
			return
		}
		adminHome := a.cfg.AdminBasePath + "/"
		switch {
		case a.cfg.AdminBasePath != "" && (r.URL.Path == a.cfg.AdminBasePath || r.URL.Path == adminHome+"index.html"):
			// A canonical trailing slash keeps relative navigation in the portal.
			location := adminHome
			if r.URL.RawQuery != "" {
				location += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, location, http.StatusPermanentRedirect)
		case r.URL.Path == adminHome:
			page := r.Clone(r.Context())
			page.URL.Path = "/"
			page.URL.RawPath = ""
			assets.ServeHTTP(w, page)
		case r.URL.Path == "/admin.css" || r.URL.Path == "/admin.js" || (a.cfg.AdminBasePath == "" && r.URL.Path == "/index.html"):
			assets.ServeHTTP(w, r)
		default:
			writeError(w, &apiError{404, "not_found", "Page not found."})
		}
	})
	return a.middleware(mux, true)
}

func (a *App) saveSettings(v Settings) (Settings, error) {
	a.bookingMu.Lock()
	defer a.bookingMu.Unlock()
	// Older clients omit these arrays. Preserve any existing blocks; an explicit
	// empty array is the supported way to remove them.
	if v.BlockedWeekly == nil || v.BlockedDates == nil || v.EstimateMinutes == 0 || v.ExternalBufferMinutes == nil {
		previous, err := a.store.settings()
		if err != nil {
			return v, err
		}
		if v.BlockedWeekly == nil {
			v.BlockedWeekly = previous.BlockedWeekly
		}
		if v.BlockedDates == nil {
			v.BlockedDates = previous.BlockedDates
		}
		if v.ExternalBufferMinutes == nil {
			v.ExternalBufferMinutes = previous.ExternalBufferMinutes
		}
		if v.EstimateMinutes == 0 {
			v.EstimateMinutes = previous.EstimateMinutes
		}
	}
	v.BusinessName = strings.TrimSpace(v.BusinessName)
	v = canonicalSettings(v)
	if err := validateSettings(v); err != nil {
		return v, &apiError{400, "invalid_settings", err.Error()}
	}
	return v, a.store.putJSON("settings", v)
}

func (a *App) run(ctx context.Context) error {
	public := &http.Server{Addr: ":8081", Handler: a.publicHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	admin := &http.Server{Addr: ":8082", Handler: a.adminHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- public.ListenAndServe() }()
	go func() { errorsCh <- admin.ListenAndServe() }()
	go a.worker(ctx)
	select {
	case err := <-errorsCh:
		public.Close()
		admin.Close()
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = public.Shutdown(shutdown)
		_ = admin.Shutdown(shutdown)
		return nil
	}
}
