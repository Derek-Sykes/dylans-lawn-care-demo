# Oracle demo deployment

## Release process

1. Develop and check on `dev` with `website.ps1 check -NoOpen`. Local start, dev and update commands continue to use port 4177 and never deploy to Oracle.
2. Push `dev`; **Check website container** runs and deployment is skipped. Open a PR from `dev` into `main`.
3. After the required check and release authorization, merge the PR. GitHub builds AMD64/ARM64 images, publishes `ghcr.io/derek-sykes/dylans-lawn-care-demo:sha-<commit>` and attaches `deployment.json` to `release-<commit>`.
4. Oracle's timer checks current main about once a minute. It accepts only this repository's exact image digest and matching revision, verifies ARM64/source labels, and runs a candidate on an isolated random loopback port.
5. The updater replaces only the `dylan-demo` website service and verifies the expected revision, HTML equality and noindex header through HTTPS at `demo.xsolutionsmd.com`. GitHub independently verifies the same address against the expected Oracle IP before reporting success.

The release job also runs for a manual workflow dispatch on `main`. It never merges branches. Superseded main releases are skipped; a main commit whose release is still building leaves the previous site in service. Builds happen on GitHub, so the developer's computer can be off. No GitHub token, SSH credential or runner is installed on Oracle; package and release downloads are public.

## One-time prerequisites

An administrator prepares the shared Caddy stack and its persistent certificate volumes, opens existing public HTTP/HTTPS ports, and creates Docker network `xsolutions-proxy`. The proxy's domain rule must be:

```caddyfile
demo.xsolutionsmd.com {
    reverse_proxy dylan-demo:8080
}
```

DNS for `demo.xsolutionsmd.com` points to Oracle. The proxy owns public ports 80/443 and TLS; the demo container publishes no host ports. Its internal HTTP server has no certificate volume. The company website is a separate service with a different network alias and independent release workflow. The shared wildcard DNS record is intended to cover future demo subdomains. Each additional website still needs an explicit gateway route and a unique container alias; existing explicit DNS records take precedence over the wildcard.

Configure Actions variable `ORACLE_HOST` to the Oracle public IP, set the GHCR package to public, protect `main` with pull requests and **Check website container**, and restrict the `production` environment to `main`. These GitHub settings must be verified independently of files in this repository. Also check the Actions page: if it shows Enable Actions on this repository, enable that gate. This repository previously allowed manual checks while automatic push/PR runs were disabled; the API enabled flag alone did not reveal the gate.

From a reviewed checkout on the prepared server, install with:

```bash
sudo bash server/install.sh
```

The installer copies root-owned configuration and scripts to `/usr/local/share/dylan-demo/compose.yaml` and `/usr/local/sbin/dylan-demo-update`, creates `/opt/dylan-demo` and `/var/lib/dylan-demo-deploy`, and enables `dylan-demo-update.timer`. It does not copy or execute code from a release download. Future changes to the installed updater or production Compose need the same manual administrator review and installation. Image/content changes deploy automatically through main.

## Inspection and recovery

```bash
sudo systemctl status dylan-demo-update.timer
sudo journalctl -u dylan-demo-update.service -n 60 --no-pager
sudo cat /var/lib/dylan-demo-deploy/current.json
sudo docker compose --project-name dylan-demo --env-file /opt/dylan-demo/release.env -f /opt/dylan-demo/compose.yaml ps
```

Check immediately with `sudo systemctl start dylan-demo-update.service`. Pause with `sudo systemctl stop dylan-demo-update.timer`, or `disable --now` to keep it paused after reboot. Resume with `sudo systemctl enable --now dylan-demo-update.timer`.

A candidate failure keeps the existing deployment. A failure or handled interruption during replacement or the HTTPS checks restores the prior Compose, image settings and release-state file, then starts the previous container. On a failed first deployment, the updater removes only the unsuccessful demo service and its new working configuration, then retries on a later timer run. There is no prior website to restore in that case. If recovery itself fails, its working directory is retained and the service fails with its location in the journal. Replacing a single container may briefly interrupt the demo. Existing company containers and shared certificates are unaffected.

Successful upgrades save recovery files as `/var/lib/dylan-demo-deploy/previous-compose.yaml` and `previous.env`. For a deliberate content rollback, revert the unwanted change on `dev`, test, and merge the correction into `main` when authorized. Do not use `down -v`, global Docker pruning, or change the shared proxy to recover one site's content. Preserve source/images and back up installed configuration, deployment state, and the shared proxy certificate volumes through the server's operating procedure.

## Validation record

Implementation and local checks are recorded here before release. Live release results are added only after Oracle and Actions have verified them. This public demonstration retains its existing noindex controls; it does not establish final owner approval or delivery of a paid client website.

September 10, 2026 implementation checks, based on source revision `d4d327b47517b028c245291e6a5da78cbd83ee0d` with the deployment changes applied:

- `website.ps1 check -NoOpen` passed: all 11 public files matched, with health, packaged revision, noindex and private-path exclusions verified.
- The production Compose definition ran on an isolated local network behind a separate Caddy proxy. It exposed zero host ports, became healthy, and served all 11 files byte-for-byte through the proxy. Revision, source label and noindex were preserved. The temporary test containers/network were removed.
- The unmodified updater ran in a disposable Linux container with simulated GitHub, network and Docker responses. All 11 scenarios passed: initial success, subsequent success, release still building, invalid image repository, wrong architecture, wrong source, candidate failure, superseding main, Compose replacement failure, live-check failure and first-deployment live-check failure. Failure paths preserved prior settings/state; the first-deployment failure removed its unsuccessful configuration. These verify updater control flow, not a production fault injection.
- Shell syntax and Compose configuration checks passed. Existing website presentation and Windows launchers were unchanged. Live Oracle deployment, public TLS and actual GitHub release-trigger verification remain pending at this implementation checkpoint.

September 10 live release and recovery follow-up:

- [PR 1](https://github.com/Derek-Sykes/dylans-lawn-care-demo/pull/1) merged after actual push and pull-request checks passed. Its main revision `1cd780ebf00ad1bd457d69aa05fbdc5d9b6ce0b4` triggered [production run 34449032348](https://github.com/Derek-Sykes/dylans-lawn-care-demo/actions/runs/34449032348), which successfully published the image and verified the exact live revision, HTML and noindex header. This was a merge-triggered run, not a manual dispatch.
- The Oracle timer deployed the ARM64 image behind the shared gateway, and `https://demo.xsolutionsmd.com/version.json` independently returned that exact revision. The company website continued serving its prior release while the demo deployed. Wildcard DNS now resolves demo subdomains to the same server; the configured demo host has a valid automatically issued HTTPS certificate.
- Recovery revision `19f5795` passed 21 isolated simulated updater scenarios, extending the original checks with configuration/state write failures, handled termination during replacement and after state-file rename, first-release interruption, failed copy/Compose/state recovery, retained recovery files, and ignored signals during cleanup. Shell syntax and actual Caddy validation using temporary memory mounts also passed. These are control-flow tests, not live production fault injection.

September 10 mobile arrow correction:

- Replaced all 16 font-based arrows with decorative inline SVGs using the same shape and color rules on every device. This removes the iPhone emoji substitution shown in the user's screenshot, including the sticky contact action.
- Kept the existing circle dimensions and hover effects; circle wrappers center SVGs with grid alignment and cannot shrink. The sticky contact action uses a horizontal icon/text layout.
- Browser checks at 320, 390 and 1280 CSS pixels found no horizontal overflow. The opening call/explore arrows have zero measured horizontal/vertical center offset; all seven circle wrappers matched at 390 pixels. Reel next navigation still works and its icons are centered within subpixel rounding. No browser errors or warnings were reported. This was desktop-browser responsive testing, not a physical iPhone test.
- Local container checks passed for all 11 public files, source revision, health, noindex and private-file exclusions. No server configuration or deployment code changed in this correction.

September 10 repeating scroll and hover motion:

- Every scroll entrance, including the opening photo reveal, now rearms after its element fully leaves the visible area and replays on the next pass in either direction. Stable layout measurements avoid feedback from the animation's own transforms; updates run at most once per animation frame. Sticky navigation and mobile contact bars are accounted for.
- Service photo zoom and grid expansion remain independently animated. Removed the outer-card hover lift that inherited entrance delays, matched grid track definitions for interpolation, and added missing hover color transitions. Paused/reduced-motion states suppress movement targets. Focused content stays visible, and FAQ expansion recalculates reveal positions.
- Browser interaction checks covered repeated desktop service/review entrances, hero rearming, rapid pointer movement among service cards, and return to equal card widths with all image transforms reset. At 390 CSS pixels, the first service card rearmed offscreen and replayed on return while the following cards tracked their own visibility; there was no horizontal overflow. Pausing cleared every pending entrance and kept content visible while scrolling. Responsive checks used the desktop browser, not a physical phone.
- JavaScript syntax, diff checks, and `website.ps1 check -NoOpen` passed. All 11 public files, packaged source revision, health, noindex and private-path exclusions were verified. Deployment configuration was unchanged.
