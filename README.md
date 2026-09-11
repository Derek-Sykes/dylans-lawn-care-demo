# Dylan's Lawn Care — website and local booking workspace

The public website lives in `dist/`. Local development also runs a Go booking service, a private admin workspace and a persistent SQLite database. **This booking work is development only.** The [existing demonstration](https://demo.xsolutionsmd.com) uses the separate main-only static release workflow. The complete dev application has its own [manual server deployment](docs/DEV_DEPLOYMENT.md) at **https://dev-demo.xsolutionsmd.com**, with the owner portal at **/admin/**. Ordinary dev pushes run checks and do not deploy either environment.

## Start on this computer

Install and start Docker Desktop with Linux containers. Use Git authenticated as an account with access to the private configuration repository. No Go, Python, Node or separate database installation is required.

Windows PowerShell:

~~~powershell
.\website.ps1 start
~~~

If Windows blocks local scripts, use this single-command, process-only override; it does not change the system execution policy:

~~~powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\website.ps1 start
~~~

macOS, Linux or Git Bash:

~~~bash
./website start
~~~

Windows users can also double-click **start.bat**. The command builds and verifies both images, starts the app, automatically retrieves missing Google application configuration using Git, prints the public and admin addresses, and opens the admin workspace for Google sign-in. Only a genuinely unconfigured installation uses the private initial-setup fragment; the launcher never prints it.

Preferred addresses are **http://127.0.0.1:4177/** for customers and **http://127.0.0.1:4178/** for the admin workspace. If either port is occupied, the launcher chooses an available port and remembers it. Each fresh clone receives its own install ID, Docker project and database volume, so two copies can run together. Both addresses bind only to this computer.

On a fresh installation, `start` retrieves `google-client.json` and `operator-access.json` from the `dev` branch of the private `Derek-Sykes/xsolutions-booking-private` repository. It uses the laptop's existing Git authentication, validates the configuration and stores it encrypted in this install's persistent data volume. The approved operator then clicks **Sign in with Google**. They can invite the business owner or connect their own Calendar for a separate testing installation. No manual file import is needed when the private repository is provisioned and the Git account has access. The private file must be added once by the application operator; see [Google configuration and operator setup](docs/GOOGLE_OAUTH.md).

Existing configured installations skip the private fetch entirely, including during updates. GitHub credentials stay with Git on the host; personal Google tokens and customer data never enter either GitHub repository. The public desktop client ID in this source is not a secret. An account's ability to clone this public website does not grant access to the private configuration repository. If private configuration is missing or access is denied, startup reports the problem before opening the admin portal rather than claiming Google setup succeeded.

Customers use **Book an appointment** in the navigation or homepage to choose a service, date and time for work at their property. Name, phone, email and property address are required; job notes are optional. New installations start with a **60-minute default appointment length**, adjustable under **Availability** in the owner portal. Changing the default affects new bookings and preserves existing appointment lengths and saved installation settings. **Request an estimate** opens a separate callback flow: it defaults to a 15-minute Calendar slot, also adjustable by the owner. The owner portal keeps **Appointments** and **Estimates & callbacks** in separate views; both retain contact details and follow-up status.

Changing a booking’s start or end in its dedicated Google Calendar updates the portal and public availability. The owner view refreshes about once a minute while visible, or immediately with **Refresh**. Edits preserve the individual appointment duration, buffer and follow-up status; a Calendar deletion cancels the matching request.

The owner sets regular working days and hours, date exceptions, and optional time off for recurring breaks or specific dates. Appointment starts follow actual free stretches of the day. Other Google Calendar events have a separately adjustable **30-minute buffer** by default; customer appointments retain the independently adjustable **15-minute gap**. With working hours of 9–7 and a personal event ending at 5, the first hour-long job can start at 5:30. Each free stretch then follows the selected appointment length plus the business gap. Saved requests retain contact details, private follow-up notes and separate customer-follow-up and Calendar-sync statuses. See [validation results](docs/BOOKING_VALIDATION.md).

The portal supports **multiple owners in one shared workspace**. Each signs in with their own approved Google account and sees the same jobs, estimates and settings. The **operator** can invite people, see who has access, and remove invited owners. Each invitation expires after 24 hours and works once; invitations to different people do not cancel each other. Calendar connection is a separate action: joining never assigns the calendar account. The first approved person to explicitly connect Google Calendar establishes the shared calendar; later sign-ins preserve that connection. The private configuration includes the operator's approved Google identity, not personal Calendar tokens. See [shared access and first-time setup](docs/ADMIN_ACCESS.md).

The **Emails** tab can send request receipts, confirmations, time changes, cancellations and a configurable reminder from that Calendar account's Gmail. The workspace's **Connect Google** action requests Calendar and send-only Gmail permissions together in one consent flow, then an owner enables email automation. Existing Calendar-only installations can add the missing permission through **Enable Gmail** under Emails or **Reconnect Google**. Ordinary portal sign-in remains identity-only. Existing bookings are not emailed just because the feature is installed or enabled. Requests remain unconfirmed until an owner confirms them. The shared history shows accepted, failed and uncertain sends; ambiguous attempts need review before retrying. This uses the existing booking container and persistent database. See [appointment email setup and behavior](docs/EMAIL_NOTIFICATIONS.md).

| Task | PowerShell | Bash |
|---|---|---|
| Build, verify and start | `.\website.ps1 start` | `./website start` |
| Edit public/admin files and refresh | `.\website.ps1 dev` | `./website dev` |
| Build and verify the static package | `.\website.ps1 build -NoOpen` | `./website build --no-open` |
| Run backend and application checks | `.\website.ps1 check -NoOpen` | `./website check --no-open` |
| Safely update, rebuild and start | `.\website.ps1 update` | `./website update` |
| Stop this install, keep its data | `.\website.ps1 stop` | `./website stop` |
| Status and both addresses | `.\website.ps1 status` | `./website status` |
| Recent app logs | `.\website.ps1 logs` | `./website logs` |
| Open the admin portal | `.\website.ps1 open` | `./website open` |

Append `-NoOpen` in PowerShell or `--no-open` in Bash to suppress browser opening. The existing Windows batch shortcuts remain supported. Keep Docker running while using the app. Closing a launcher window does not stop the app.

## Editing and updates

In dev mode, public `dist/` and admin `booking/web/` are mounted read-only from the checkout, so HTML/CSS/JavaScript edits appear on refresh. Rerun dev after Go or container configuration changes. Start and update use packaged images, removing those source mounts; later edits require another build/start.

Git is needed to clone, retrieve private Google configuration on first setup, and update. The updater accepts a clean current `dev` or `main` branch and performs a fast-forward only. It refuses uncommitted/untracked source changes, feature branches, and ahead/divergent history. It never resets your work, overwrites private setup, or deletes a database volume. A failed build leaves the previous containers running, although the source may already have advanced. Development normally belongs on `dev`.

The saved `.local/runtime.env` contains this install's ID, project name, selected ports and bootstrap token. It is ignored by Git. `.env` can optionally override the preferred ports, project name and Google settings; see [the example](.env.example). Avoid changing an existing install's project name because it selects a different database volume. Copying the source without `.local/` creates a separate install; restoring an install requires its saved state and data together.

`stop` removes only this install's containers and network. Its named `<project>_booking-data` volume survives stop, rebuild, source update and Docker restart. A stopped stack resumes on its saved ports when those ports remain available. If another program claims one, the next start selects another; OAuth uses the resulting loopback admin origin.

PowerShell and Bash share an atomic launcher guard. If a launcher is terminated without cleanup, confirm it is no longer running before removing `.local/launcher.guard` and retrying.

## Privacy and backups

The database, Google tokens, encryption key and local operator credential are private. They are never publication inputs. Keep complete backups outside this repository, in encrypted storage. Follow [backup and restore](docs/LOCAL_OPERATIONS.md) and [privacy details](docs/PRIVACY.md). Losing the encryption key makes stored Google credentials unusable; copying only the SQLite file is not a complete backup.

The static image is deliberately built from only the existing `Dockerfile`, `Caddyfile` and `dist/`. The root `.dockerignore` keeps its existing allowlist. Local Compose adds `Caddyfile.local` as a read-only configuration mount and builds the separate `booking/Dockerfile`. The public Caddy service proxies only `/api/public/*`; admin routes and OAuth stay on the separate admin listener.

## Checks and release boundary

`check` builds both images, verifies every static file against the checkout, checks the packaged revision, health, demo noindex headers and private-file exclusions, runs Go tests, and starts a temporary isolated booking stack for authentication, CSRF, settings and route-isolation checks. It uses no Google credentials and removes only its own temporary containers and data afterward. Your local booking volume is untouched.

Developer launcher regression tests are in [scripts/test-launchers.ps1](scripts/test-launchers.ps1). They create isolated source fixtures and explicitly named test projects to verify collision handling, two clones, shared-shell settings, restart, mounted edits, safe update refusals and data persistence. They never update the real source branch or contact a remote Git server.

[scripts/test-google-setup.ps1](scripts/test-google-setup.ps1) exercises private setup with synthetic credentials in each available shell: PowerShell 7, Windows PowerShell 5.1 and Bash. It checks missing, malformed, oversized and mismatched configuration, unexpected Git URL rewrites, successful import, temporary-file cleanup and offline reuse. It does not read real Google credentials.

The root Dockerfile, `compose.production.yaml`, existing `server/update-release.sh` and main release behavior remain the static website deployment. The existing GitHub workflow uses PowerShell `check` on Linux, then publishes only the root static image after a separately authorized main merge. **No booking backend, data or credentials are published by that main workflow.** See [existing deployment records](docs/DEPLOYMENT.md).

[compose.dev-server.yaml](compose.dev-server.yaml) and [server/dev](server/dev) belong exclusively to the manual dev environment. Use GitHub Actions → **Deploy dev to server** → **Run workflow**, select **dev**, then confirm **Run workflow**. This builds the chosen dev revision on GitHub and updates only that environment, preserving its private settings and data. See [deployment and recovery instructions](docs/DEV_DEPLOYMENT.md).

[compose.booking.production.yaml](compose.booking.production.yaml) remains a future template for a separately authorized booking launch. It is never selected by the local launchers or either release workflow. No final client launch or owner acceptance is established by the dev deployment.
