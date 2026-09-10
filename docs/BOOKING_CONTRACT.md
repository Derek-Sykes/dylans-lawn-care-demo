# Booking implementation contract

This documents the local booking application's interfaces. The booking extension is on `dev`; the existing live static website and main deployment remain separate.

## Runtime

- Existing static public Caddy website remains the `web` service on internal port 8080. Proxy `/api/public/*` to `booking:8081`.
- A single Go service under `booking/` listens on 8081 (public API only) and 8082 (admin UI + admin API + OAuth callback). Embed `booking/web/` in the Go binary. SQLite and the encryption key live under `/data` on a persistent local volume. No external database or queue.
- Root Dockerfile stays the static site image. `booking/Dockerfile` builds the Go backend. Local Compose builds and starts both. Preserve current production files: provide a separate future booking production template, never change the active deployment workflow into a release of this backend.
- Environment: `PUBLIC_ORIGIN`, `ADMIN_ORIGIN` exact browser origins (including dynamically selected ports); `DATA_DIR=/data`; `BOOTSTRAP_TOKEN` random per-install admin bootstrap secret, generated in ignored `.local/` and preserved on update. `GOOGLE_CLIENT_ID` optional override; default from `booking/config/google-client.json` (public desktop client ID only). `GOOGLE_OAUTH_MODE=desktop` default, web optional with secret supplied only via private environment. Web mode requires configured HTTPS origin/client credentials.
- Local admin startup opens `ADMIN_ORIGIN/#setup=<BOOTSTRAP_TOKEN>` without printing the token. UI reads/clears fragment and POSTs bootstrap; backend authenticates the operator and creates a persistent HttpOnly session. Subsequent Google login recognizes the bound owner identity, no open self-registration. Origin/CSRF checks for writes, install-specific cookie name, allowed Host validation, rate limits. Google tokens encrypted on disk. No token/secret/customer-data logs.
- Native Google OAuth uses PKCE S256, short-lived single-use state tied to the initiating browser, callback `/oauth/callback`, offline refresh, and scopes `openid`, `email`, `calendar.app.created` and `calendar.freebusy`. No Gmail scope. Create a dedicated booking calendar visible in the owner's Google Calendar; check busy times in both primary and booking calendars. Store the verified email, stable subject and dedicated calendar ID in the encrypted owner record, preserving the calendar ID across reconnects. Event writes are retried using stable IDs. No confirmation is claimed until sync succeeds. Handle cancellation and disconnect. No fake OAuth UI.

## HTTP interface (JSON)

- `GET /api/admin/session` → `{authenticated, csrfToken?, setupRequired?, google:{configured,connected,email?,error?,mode,requiresClientConfiguration?}, publicOrigin}`. Anonymous status may reveal only configuration booleans, never connected email. Frontend attaches `X-CSRF-Token` to authenticated writes.
- `POST /api/admin/bootstrap` `{token}` → same session object; token is local operator credential, never committed or displayed by the UI. CSRF-exempt only with strict Origin + token checks. Remove fragment before API call.
- `POST /api/admin/logout` → 204.
- `POST /api/admin/google/configure` `{clientId,clientSecret}` → 204. Authenticated, CSRF-protected, desktop mode only. The ID must match the configured client. Store private configuration encrypted in the persistent volume and never return the secret. The UI imports a separately supplied JSON file once; Google's real desktop endpoint rejected client-ID-only PKCE for this registered client. No private client secret belongs in GitHub.
- `POST /api/admin/google/connect` → `{url}`. `GET /oauth/callback` handles Google and returns to admin with safe outcome indicator. `POST /api/admin/google/disconnect` → 204.
- `GET /api/admin/settings` and `PUT /api/admin/settings`: `{businessName,timeZone,slotMinutes,bufferMinutes,minNoticeHours,horizonDays,weekly:[{weekday,start,end}],exceptions:[{date,closed,start?,end?}],blockedWeekly:[{weekday,allDay,start?,end?}],blockedDates:[{date,allDay,start?,end?}]}`. Weekday 0=Sunday. HH:mm local wall time, YYYY-MM-DD dates; IANA timezone default America/New_York. Defaults weekdays 09:00–17:00, 30-minute appointments, 15-minute buffer, 24-hour notice, 30-day horizon, and no time-off rules. Date exceptions replace usual working hours; weekly and date-specific time off subtract from those hours. Google busy times and existing reservations always take precedence. Older saved settings without the new block arrays behave as empty arrays.
- `GET /api/admin/bookings` → `{bookings:[{id,start,end,serviceId,name,email,phone,address,notes,status,calendarStatus,createdAt,adminNotes}]}`. Optional status filter. `PATCH /api/admin/bookings/{id}` `{status?,adminNotes?}`; statuses needs_followup/contacted/confirmed/cancelled. Calendar synchronization is separate (`pending/synced/failed`). Allow retry through `POST /api/admin/bookings/{id}/retry`.
- `GET /api/public/config` → `{businessName,timeZone,slotMinutes,bufferMinutes,minNoticeHours,horizonDays,services:[{id,name}],bookingEnabled}`; services lawn-care/Lawn care, landscaping/Landscaping, snow-ice/Snow & ice. First version schedules estimate/callback appointments; selected service describes the inquiry, duration remains the owner's slot duration.
- `GET /api/public/slots?date=YYYY-MM-DD` → `{date,timeZone,slots:[{start,end}]}` (RFC3339 UTC instants). Exclude local reservations and Google busy intervals; fail closed on unavailable Calendar rather than publishing unsafe availability.
- `POST /api/public/bookings` `{serviceId,start,name,email,phone,address,notes,idempotencyKey}` → `{id,status,calendarStatus,start,end,message}`. Validate all fields/limits, enforce slot validity and transactionally prevent overlaps; never trust supplied durations. Handle repeated submissions idempotently.
- Both listeners have `/healthz`; public listener must not expose admin routes/assets/data. Errors `{error:"clear safe message",code?:"stable_code"}` with appropriate status. All API responses no-store.

## Frontend

- Preserve site theme/artwork. New public page `dist/book.html`, `dist/booking.css`, `dist/booking.js`, and a restrained booking link in current navigation/contact. Do not replace verified phone contact.
- Admin files `booking/web/index.html`, `admin.css`, `admin.js`. Google connection, weekly availability/date exceptions, booking list with follow-up status and customer details, notes/cancel/retry, logout. Loading, error, disconnected, empty and success states; mobile usable, accessible controls; user data rendered as text.
- No customer accounts, payments, routing or property duration engine in this version.

## Launchers

- `./website.ps1 start` and `./website start` need only Docker (Git for update). Automatic usable ports, print public/admin URLs, open local admin bootstrap automatically unless NoOpen. Existing start/update/stop/status/check commands remain.
- Persist install ID, bootstrap secret and port choices in ignored `.local/`; updates must not overwrite these or delete volumes. Handle two clones, occupied preferred ports, restart, update, and source-mounted dev. `stop` never deletes data. Document backup/restore without exposing credentials.
- Checks build both images and run meaningful backend/integration tests. Do not require Google credentials in CI. Keep main-only existing static release unchanged; no server deployment this task.
