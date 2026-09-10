# Dylan's Lawn Care — website and local booking workspace

The public website lives in `dist/`. Local development also runs a Go booking service, a private admin workspace and a persistent SQLite database. **This booking work is development only.** The currently deployed [public demonstration](https://demo.xsolutionsmd.com) and its main-only static release workflow remain separate; these changes do not deploy booking.

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

Windows users can also double-click **start.bat**. The command builds and verifies both images, starts the app, automatically retrieves missing Google application configuration using Git, prints the public and admin addresses, and opens the admin workspace with the private setup credential in a URL fragment. The admin page consumes and removes that fragment. The credential is never printed by the launcher.

Preferred addresses are **http://127.0.0.1:4177/** for customers and **http://127.0.0.1:4178/** for the admin workspace. If either port is occupied, the launcher chooses an available port and remembers it. Each fresh clone receives its own install ID, Docker project and database volume, so two copies can run together. Both addresses bind only to this computer.

On a fresh installation, `start` retrieves `google-client.json` from the `main` branch of the private `Derek-Sykes/xsolutions-booking-private` repository. It uses the laptop's existing Git authentication, validates the configuration and stores it encrypted in this install's persistent data volume. The owner then clicks **Connect Google Calendar** and signs in normally. No manual file import is needed when the private repository is provisioned and the Git account has access. The private file must be added once by the application operator; see [Google configuration and operator setup](docs/GOOGLE_OAUTH.md).

Existing configured installations skip the private fetch entirely, including during updates. GitHub credentials stay with Git on the host; personal Google tokens and customer data never enter either GitHub repository. The public desktop client ID in this source is not a secret. An account's ability to clone this public website does not grant access to the private configuration repository. If private configuration is missing or access is denied, startup reports the problem before opening the admin portal rather than claiming Google setup succeeded.

The owner sets regular working days and hours, date exceptions, and optional time off for recurring breaks or specific dates. Available appointments are working hours minus time off, existing reservations and Google Calendar conflicts. Saved requests include contact and property details, private follow-up notes, and separate customer-follow-up and Calendar-sync statuses. See [validation results](docs/BOOKING_VALIDATION.md).

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
| Reopen private admin setup | `.\website.ps1 open` | `./website open` |

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

The root Dockerfile, active `compose.production.yaml`, `server/` scripts and main release behavior remain the static website deployment. The existing GitHub workflow can still use PowerShell `check` on Linux, then publish only the root static image after a separately authorized main merge. **No booking backend, data or credentials are published by that workflow.** See [existing deployment records](docs/DEPLOYMENT.md).

[compose.booking.production.yaml](compose.booking.production.yaml) is a future template only. It is never selected by the launchers or release workflow. A real booking deployment needs a separately authorized reviewed release, approved public/admin HTTPS origins, appropriate Google configuration, backup/restore verification and shared-proxy routing. No final client launch or owner acceptance is established by this local implementation.
