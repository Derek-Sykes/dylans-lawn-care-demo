# Local booking validation

Validation date: September 10, 2026. These checks concern the local booking extension on `dev`, not a server deployment.

## Verified behavior

- Windows Docker Desktop: both containers build, become healthy, and expose separate public and admin addresses. The existing website on port 4177 remained running; the new installation selected 4178 and 4179.
- PowerShell `check`: all 14 static package files match the checkout; packaged revision, headers and private-file exclusions pass. Race-enabled Go tests and isolated HTTP integration checks pass.
- Launcher regression suite: eight test groups plus six targeted checks cover separate clone identities, occupied ports, common PowerShell/Bash state, stop/start persistence, source-mounted editing, clean fast-forward updates, unsafe-update refusal and corrupted-state refusal. Windows PowerShell 5.1 and Git Bash were exercised. macOS hardware was not available.
- Actual Google registration and browser consent succeeded with the registered desktop client after importing private configuration. The application created a dedicated calendar. No Gmail permission was requested.
- The actual Google token endpoint rejected a client-ID-only PKCE exchange with `client_secret is missing`. The private import requirement is a tested limitation, not an assumption. See [Google configuration](GOOGLE_OAUTH.md).
- A closed date returned no appointment times. Existing primary-calendar work hours also correctly removed weekday availability. A temporary Saturday opening offered 11 slots.
- An explicitly labeled local test request was submitted through the public form, synchronized to the real Google Calendar, appeared as an owner follow-up task, and retained its contact details and private notes. The reserved time disappeared from the public slot list.
- The test event was also observed directly in Google Calendar, in the dedicated booking calendar at the expected time and duration. Restarting the backend retained the account connection, browser login, request status and private notes.
- Browser checks for the new time-off controls passed: a recurring Saturday 12:00–13:00 break reduced available times from 10 to 8; a specific-date whole-day block reduced them to zero even with custom working hours. Go tests additionally cover buffers, midnight, both DST transitions, overlapping blocks, legacy settings and explicit clearing.
- A real `website.ps1 update -NoOpen` against the pushed GitHub `dev` revision rebuilt and started packaged containers, removed development source mounts, and preserved the private Google configuration, connected account, browser session, settings, request status and notes.
- Cancellation completed in the real Calendar integration and restored all 11 test slots. Google Calendar's own interface confirmed the test event was gone. Temporary Saturday hours and all test time-off rules were removed; the local cancelled request remains as a test record.
- Signing out cleared the owner view; signing back in through Google restored access for the connected account without importing configuration again.
- [GitHub Actions run 34526659779](https://github.com/Derek-Sykes/dylans-lawn-care-demo/actions/runs/34526659779) passed for implementation commit `a0de2cc0591b14bc22431cc9de12884d96d28273` on Ubuntu. The Oracle publication/deployment job was skipped.
- Desktop screenshots were reviewed for the public form and owner workspace. At a 390-pixel viewport, public fields and the admin request layout fit without horizontal overflow. Native iOS/Safari and macOS rendering were not tested.
- Frontend-focused tests exercise validation, exact idempotent retry payloads, bounded confirmation refresh, configuration JSON parsing/import, duplicate submits, logout races, replacement configuration and live connection refresh.

## Data and operating boundaries

Google tokens, imported configuration, sessions, customer records and the encryption key are local runtime data. They are excluded from Git and both image build contexts. The public repository contains only the OAuth client identifier.

This version schedules estimate/callback conversations. It has no customer accounts, payments, travel-time optimization or automatic service-duration calculation. It sends no customer emails or Calendar invitations. Primary and app-created calendars are checked; other secondary calendars are outside the first version.

After real sign-in and a synchronized request, `docker stats --no-stream` reported approximately 9.5 MiB for the booking process and 16.5 MiB for Caddy (about 26 MiB combined). The small test database was 60 KiB after checkpoint, with a 32 KiB SQLite shared-memory file. These are observations from a light local workload, not a capacity guarantee, and exclude Docker Desktop, image builds and build caches.

The active static Dockerfile, production Compose file, server updater and main-only release workflow remain unchanged. The future booking production template has not been deployed.
