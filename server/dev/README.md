# Manual development deployment runtime

This is independent of the static demonstration's main-branch updater. A timer checks for the manifest published by **Deploy dev to server**, but a source push, pull request, timer tick or server restart cannot publish a new manifest. Only an explicit workflow dispatch publishes it. The dev environment is `https://dev-demo.xsolutionsmd.com`, with the owner workspace at `/admin/`.

## Installed contract

| Item | Value |
|---|---|
| Runtime directory | `/opt/dylan-dev` |
| Private deployment state | `/var/lib/dylan-dev-deploy` |
| Root-owned templates | `/usr/local/share/dylan-dev` |
| Root-owned updater | `/usr/local/sbin/dylan-dev-update` |
| Timer/service | `dylan-dev-update.timer`, `dylan-dev-update.service` |
| Compose project | `dylan-dev` |
| Persistent database/key volume | `dylan-dev_booking-data` |
| Proxy aliases | `dylan-dev-web:8080`, `dylan-dev-admin:8082` |
| Shared proxy network | `xsolutions-proxy` |
| Manifest | Fixed `dev-server` prerelease, `deployment.json` asset |

Only the web and admin listeners use the existing shared proxy network. The public booking API uses the per-project private network through `booking:8081`. No application host ports are published. The database volume is unique to this environment, and local/laptop installs and the existing demonstration are untouched.

`install.sh` installs these reviewed host files. It preserves an existing timer's running state; on a new installation it leaves the timer disabled until configuration and routing are ready. Updates to these host scripts require a separate installation; downloaded image releases never execute installation code.

The operator provisions `/opt/dylan-dev/runtime.env`, owned by root and mode `0600`, with `BOOTSTRAP_TOKEN`, `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET`. These are private runtime values and never belong in the public repository or image. Compose fixes both origins to the development HTTPS address, `GOOGLE_OAUTH_MODE=web`, and `ADMIN_BASE_PATH=/admin`. Google must have an approved web client redirect at `https://dev-demo.xsolutionsmd.com/oauth/callback`; the laptop desktop client is a separate registration.

Once the private configuration and shared gateway are prepared, activate the independently scoped timer with `sudo systemctl enable --now dylan-dev-update.timer`. It polls once per minute and leaves an absent manifest alone. `sudo systemctl start dylan-dev-update.service` performs the same immediate check; it does not publish or choose a different source revision.

## Verification and recovery

The updater validates the exact manifest fields, immutable image namespaces/digests, Linux ARM64 architecture and image source/revision. Run ID and attempt prevent a stale manifest from replacing a newer request. It deploys the dispatched commit even if later development commits exist. Repeating a manual dispatch for the same commit is a distinct release, verified through the admin version endpoint's run metadata.

Both candidate containers use an isolated internal network with no host ports, no real Google configuration and disposable data. Candidate probes cover both served revisions, the public page, owner page, public API route and noindex. Existing dev containers are then stopped briefly while the complete database/key volume is archived. The replacement must pass both HTTPS revision checks and actual public/admin routes before release state is committed.

On a handled failure or interruption, the updater restores the stopped-volume snapshot, previous image definitions and prior release state, then verifies the old HTTPS revision. One complete prior generation remains in `/var/lib/dylan-dev-deploy/previous`, including `data.tgz` and previous release definitions. This is a same-server recovery copy, not protection against disk loss; maintain encrypted off-server backups separately.

A durable `transaction.json` records an in-progress replacement before stopping the app. After a process kill or reboot interrupts replacement, later polls **refuse to overwrite the retained recovery generation** until an operator inspects the journal, `check.*` recovery directory, current containers and `previous` directory. This intentionally requires recovery inspection; automatic recovery from power loss has not been claimed or tested. If a handled recovery itself fails, its private recovery directory and journal are also retained. Do not delete either or dispatch more releases to bypass that state.

Use `sudo journalctl -u dylan-dev-update.service -n 80 --no-pager` for safe deployment status; command failures deliberately omit raw environment/output. Stop only the dev timer while repairing. Restore a matched prior image definition, manifest and full data archive together, preserving ownership and the encryption key. Never remove another project's containers, shared proxy network or certificate volumes.

## Developer checks

`python3 scripts/test-dev-deploy.py` runs isolated tests with synthetic data and no network/Docker access. It covers manifest/identity rejection, absent and repeated releases, first/subsequent deploys, candidate/replacement/HTTPS failure, snapshot failure, handled interruption after state publication, failed recovery, and process-loss journal refusal. It also runs with `python3 -O` and an unprivileged Linux user.

`scripts/test-dev-deploy-containers.py` additionally exercises the real candidate containers and complete volume snapshot/restore using separately built fixture images and newly named temporary resources. See its command-line documentation. It never connects Google or uses the existing installation's data.
