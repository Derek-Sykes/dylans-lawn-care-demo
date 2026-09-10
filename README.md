# Dylan's Lawn Care — website demo

A standalone static website with a local Docker preview and an authorized demonstration address at **https://demo.xsolutionsmd.com**. Public website files live in `dist/`. Develop on `dev`; an authorized merge into `main` checks, publishes and deploys the demo to Oracle. The company website keeps its own container at xsolutionsmd.com. This repository contains no server credentials, domain automation or quote submission backend.

## Start the demo

Install Git and Docker Desktop with Linux containers. Start Docker Desktop, then double-click **start.bat** in this folder. It builds the image, verifies the files, starts the website and opens:

**http://127.0.0.1:4177/**

The preview listens only on this computer. Keep Docker Desktop running while recording. Closing the launcher window does not stop the website. Double-click **stop.bat** when finished.

| Task | Double-click on Windows | PowerShell from this folder |
|---|---|---|
| Build, check and show the demo | `start.bat` | `.\website.ps1 start` |
| Edit and refresh immediately | `dev.bat` | `.\website.ps1 dev` |
| Build and verify without starting | `build.bat` | `.\website.ps1 build -NoOpen` |
| Pull the current branch, rebuild and start | `update.bat` | `.\website.ps1 update` |
| Stop this demo | `stop.bat` | `.\website.ps1 stop` |
| Run container/file checks | `check.bat` | `.\website.ps1 check -NoOpen` |
| See current status | `status.bat` | `.\website.ps1 status` |
| See recent logs | — | `.\website.ps1 logs` |

The batch files apply a PowerShell execution-policy override only to their own invocation; they do not change Windows policy. Append `-NoOpen` when using PowerShell to avoid opening the browser. If port 4177 is occupied, copy `.env.example` to `.env`, choose a different `DYLAN_PORT`, and restart. Leave unrelated applications running. A second simultaneous clone also needs a distinct `DYLAN_PROJECT`.

## Make changes

Use **dev.bat**, edit HTML/CSS/JavaScript/photos in `dist/`, and refresh the browser. The container mounts those actual files read-only, matching VoiceVault's source-mounted development pattern. No bundler or package installation is needed. Restart dev mode after changing Caddy or Docker configuration.

Use **start.bat** before recording or acceptance review. It packages the files into the image, checks every served file against the checkout, and starts that exact image without a source mount. Later file edits require another start/build. The image contains only `dist/` and the web-server configuration; Git history, client research, launchers and private notes are excluded.

The website is static: there are no accounts, database, uploaded leads, stored messages or tool-login volumes to preserve. Contact links open the business's verified contact destinations. They do not establish message delivery or a booked appointment. Keep personal notes and asset approval records in the parent client folder, outside this repository and image.

## Git and a second computer

Develop on `dev` or a focused feature branch targeting `dev`. Review and commit changes, then push to this repository. On a second computer, clone its **dev** branch, start Docker Desktop and run `start.bat`. Use `update.bat` thereafter.

The public repository is [Derek-Sykes/dylans-lawn-care-demo](https://github.com/Derek-Sykes/dylans-lawn-care-demo), with `dev` as its default branch. Cloning does not require a GitHub sign-in; pushing changes requires write access. Clone once:

```powershell
git clone --branch dev https://github.com/Derek-Sykes/dylans-lawn-care-demo.git
cd dylans-lawn-care-demo
.\start.bat
```

The GitHub **Website checks and deployment** workflow also has a **Run workflow** button. A run on `dev` checks only; a run on `main` also publishes and verifies the current main release. Read [deployment and validation](docs/DEPLOYMENT.md) for requirements and actual verification status.

The updater follows the current `dev` or `main` branch. It refuses uncommitted/untracked work, feature branches, and history that cannot safely advance to the remote. It fetches and fast-forwards, builds and verifies a candidate, then replaces this local container. A failed build leaves the prior packaged container running; source may already have advanced. It never force-resets Git or removes unrelated Docker resources. In dev mode, edits are visible immediately because its source is mounted.

The included GitHub check builds the container, verifies the exact files and source revision, tests health and demo indexing headers, and checks private files are inaccessible. That check has read-only repository permission. Only the separate main release job can publish an image and release manifest. Continue development on `dev`; open a PR with base `main`, wait for **Check website container**, and merge when release is authorized. The initial public demo setup is authorized; it is not standing permission for every future main merge. Repository protection settings must be configured/verified separately; files alone do not enforce them.

`/version.json` reports the packaged commit, with `-dirty` for a checkout containing uncommitted changes. In dev mode, this identifies the base build; live mounted edits can be newer. Read the parent client's QA record for the actual tested revision and visual checks.

## Recording sequence

1. Run `start.bat`. Open the local preview at normal zoom and hide development panels.
2. Pause on the opening photo and headline, then point out the direct contact action.
3. Scroll slowly through the supported services and authentic project photos.
4. Show the sourced customer review excerpts and the service-area/contact section.
5. Show the narrow mobile layout, menu and contact controls. Explain the next step as a quote conversation; do not trigger a call or send a message during the recording.

Aim for 60–90 seconds. Present this as a demonstration for Dylan's review. Confirm copy, photo use, service area and the quote/contact process with the owner before a final client launch.

## Public demonstration and final client launch

The September 10 request authorizes publishing this demonstration at `demo.xsolutionsmd.com` on the existing Oracle server and setting up automatic main releases. It supersedes the earlier local-only deployment boundary for this demo. It does not establish owner acceptance, a purchased client domain or a final business launch. Existing noindex controls and the requested presentation remain in place; noindex discourages indexing but does not restrict who can open the address.

The static image uses HTTP on internal port 8080. `compose.local.yaml` binds that port only to the local computer. `compose.production.yaml` publishes no host ports: the shared Caddy proxy sends `demo.xsolutionsmd.com` requests over the external `xsolutions-proxy` network to `dylan-demo:8080`. Releases support AMD64 and ARM64, and Oracle verifies the ARM64 image. Certificates and the company website belong to separate stacks. See [deployment operations](docs/DEPLOYMENT.md).

After Dylan accepts the scope and approves content/assets, agree final domain and hosting ownership, purchase/connect the selected domain under fresh authorization, and configure HTTPS for that host. Replace the demo indexing controls only for the approved final release, set real canonical/metadata URLs where used, verify all contact destinations and the chosen quote process, then test domain HTTPS and mobile behavior. This demo has its own release job, timer and website container.

The same Git source and `dist/` are the publication inputs, so the final client transition does not require redesigning the page.

## Workflow provenance

The launcher behavior was adapted from the actual VoiceVault `voicevault.ps1`, `scripts/voicevault.ps1`, `dev/voicevault-dev.ps1` and development guide, plus the verified X Solutions Caddy/image workflow. Useful conventions retained: a single source tree, separate mounted and packaged modes, health-checked start, safe dev/main updates and intentional release gates. VoiceVault's database, AI tools, Portainer integration and credential volumes are unnecessary for this static demo and were not copied.
