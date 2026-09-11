# Manually deploy the dev environment

The development website is **https://dev-demo.xsolutionsmd.com**. The owner portal is **https://dev-demo.xsolutionsmd.com/admin/**. This environment has its own containers, Google connection, settings and booking database. The existing **https://demo.xsolutionsmd.com** website continues to use the independent main release.

## Deploy with the button

1. Push the changes you want to test to **dev**.
2. Open [Actions → Deploy dev to server](https://github.com/Derek-Sykes/dylans-lawn-care-demo/actions/workflows/deploy-dev.yml).
3. Click **Run workflow**, select **dev**, then click **Run workflow** again.
4. Wait for the run to succeed. It checks the app, builds both images, publishes this specific deployment request and verifies both images at the dev address.

With GitHub CLI installed and authenticated, the equivalent command is:

```sh
gh workflow run deploy-dev.yml --ref dev --repo Derek-Sykes/dylans-lawn-care-demo
```

**Only this manual workflow requests a dev deployment.** There is no push, pull request, schedule, or workflow-completion trigger in it. The `dev-server` GitHub environment accepts only the `dev` branch. Ordinary pushes run checks but leave the server revision unchanged. No main merge is involved.

The deployed revision is the dev commit selected when you press the button; subsequent pushes do not change that request. Both jobs use that exact commit. Deployment requests are serialized. Re-running a workflow creates a new attempt, which must be separately applied and verified even if its commit is unchanged.

## What runs where

| Component | Location |
|---|---|
| Image builds and application checks | GitHub Actions runners |
| Web image | `ghcr.io/derek-sykes/dylans-lawn-care-dev-web` |
| Booking image | `ghcr.io/derek-sykes/dylans-lawn-care-dev-booking` |
| Compose project | `dylan-dev` |
| Installed runtime | `/opt/dylan-dev` |
| Private server configuration | `/opt/dylan-dev/runtime.env` (root, mode 600) |
| Persistent database and encryption key | Docker volume `dylan-dev_booking-data` |
| Deployment state and previous-generation recovery | `/var/lib/dylan-dev-deploy` (root only) |
| Reviewed deployment definitions | `/usr/local/share/dylan-dev` |
| Installed updater | `/usr/local/sbin/dylan-dev-update` |
| Updater service and timer | `dylan-dev-update.service`, `dylan-dev-update.timer` |
| Gateway upstreams | `dylan-dev-web:8080`, `dylan-dev-admin:8082` |

No dev application ports are published on the server. The existing Caddy gateway handles HTTPS and forwards this hostname through the existing `xsolutions-proxy` network. The website and booking service also share a private app network. Other apps on the shared proxy network are trusted infrastructure; the shared network is not a security boundary between them.

GitHub builds native ARM64 images and pins both immutable digests in a small manifest. The `dev-server` prerelease contains the latest **manually requested** manifest, while each request also gets an immutable audit release named `dev-request-<run>-<attempt>`. These prereleases do not replace the existing main release marked Latest. The server checks this manifest roughly once a minute; it never polls the dev branch. A missing manifest is a no-op. The timer also resumes at boot without creating a new deployment request.

The server validates the manifest, image architecture, source repository and revision. It tests candidates with disposable data before stopping the dev app, saves a complete stopped-data snapshot before an update, then verifies the public and owner pages over HTTPS. The backend reports the exact workflow run and attempt so a stale process cannot make a repeated deployment look successful. Failed updates restore the preceding application and data; recovery evidence is retained if restoration cannot finish.

## Google sign-in and owner access

Hosted OAuth uses a separate Google **Web application** client in the existing X Solutions Booking project. Its registered redirect is exactly:

```text
https://dev-demo.xsolutionsmd.com/oauth/callback
```

Its client secret and a separately generated setup token live only in the private server configuration, with an ignored private operator copy. They are not in the public repository, image, workflow, release manifest or Actions logs. Personal Google tokens, owner identity and bookings are encrypted/stored in this environment's persistent volume. Source/image updates preserve both the configuration and volume.

Provision the approved operator with `OPERATOR_GOOGLE_EMAIL` and optionally `OPERATOR_GOOGLE_SUB` in the protected runtime configuration, or import `operator-access.json` through the booking CLI. The operator signs in with Google and creates expiring, single-use invitations under **Access & invitations**. Multiple owners sign in with their own invited Google accounts and manage the same workspace. Joining does not assign Calendar ownership: an approved person explicitly connects Calendar when none is assigned, and subsequent invitations preserve that connection. Existing configured installations disable the reusable bootstrap access route. See [admin access](ADMIN_ACCESS.md). Google may require reauthorization if consent is revoked or its token expires; an application update itself does not disconnect the account.

The laptop launchers continue to use the existing desktop client and private Git configuration. Server deployment does not copy a laptop's personal connection or data. Availability and Google Calendar conflicts use the same application behavior in both environments.

## Operator installation and maintenance

Initial server installation is an operator task, separate from pressing the deployment button. The existing Oracle ARM64 server needs Docker Compose, Python 3, curl, systemd and the shared gateway/network. DNS must point the selected hostname to the server.

From a reviewed checkout on the server, run `sudo bash server/dev/install.sh`. Prepare `/opt/dylan-dev/runtime.env` privately with `BOOTSTRAP_TOKEN`, `GOOGLE_CLIENT_ID`, and `GOOGLE_CLIENT_SECRET`; set ownership to root and permissions to 600. Keep these values out of shell history. The installer preserves an existing runtime file and volume.

The canonical gateway configuration is maintained in the separate `Derek-Sykes/xsolutions-website` repository at `server/gateway/config/Caddyfile`. The dev block routes `/admin/version.json` to the backend version endpoint, `/admin`, `/admin/*`, `/admin.css`, `/admin.js`, `/api/admin/*` and `/oauth/*` to the owner listener, and all remaining requests to the public website. Back up the installed gateway config, validate the complete file, and reload Caddy gracefully. Preserve its certificate volumes and other hostname blocks.

Both dev image packages must allow public pulls. The workflow needs the repository's `ORACLE_HOST` variable and a `dev-server` environment restricted to dev; no SSH credential is put into Actions. Once the route and private runtime are ready, enable `sudo systemctl enable --now dylan-dev-update.timer`, then manually dispatch the workflow.

Ordinary app deployments update the images, not root-owned installation scripts or the gateway. If those files change, review and reinstall the affected server files before dispatching a release that depends on them.

## Diagnostics and recovery

On the server:

```sh
sudo systemctl status dylan-dev-update.timer dylan-dev-update.service
sudo journalctl -u dylan-dev-update.service -n 60 --no-pager
sudo cat /var/lib/dylan-dev-deploy/current.json
```

`current.json` is public deployment metadata and contains no credentials. Avoid dumping Docker configuration or environment files into logs. Public identity checks are `/version.json` and `/admin/version.json` on the dev hostname.

Before manual repair, stop only `dylan-dev-update.timer` and wait for its service to finish. Preserve its state directory and the complete volume. A failed recovery or interrupted transaction must be investigated before clearing its recovery guard. Do not delete the volume to make an update pass, reuse the demo project name, or run global Docker prune commands.

The updater retains one complete previous generation, including the SQLite files and their encryption key. This is deployment recovery, not an ongoing backup of newly entered bookings. Before important testing or a manual restore, make a separate protected backup: stop the dev app under the deployment lock, archive the entire volume, save `/opt/dylan-dev` and its deployment state, then restart the same images and timer. Follow the complete-data principles in [local operations](LOCAL_OPERATIONS.md). Keep archives private and copy them to encrypted off-server storage when the data matters. Never combine an older database with a different encryption key.

To roll back application code through the ordinary button, revert the relevant source change on dev and manually deploy the resulting commit. An older schema may require a coordinated data restore; code rollback alone is not automatically a safe database rollback.

## Verification

See [booking validation](BOOKING_VALIDATION.md) for the current test and live deployment record. This environment is for development and testing; its existence does not establish a final client launch or owner acceptance.
