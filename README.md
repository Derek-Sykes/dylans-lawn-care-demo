# Dylan's Lawn Care — local website demo

A standalone static website and small Docker workflow. Public website files live in `dist/`. This checkout does not publish or purchase anything; it has no server credentials, domain automation or quote submission backend.

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

Develop on `dev` or a focused feature branch targeting `dev`. Review and commit changes, then push to the dedicated private client repository when configured. On a second computer, clone its **dev** branch, start Docker Desktop and run `start.bat`. Use `update.bat` thereafter.

The private repository is [Derek-Sykes/dylans-lawn-care-demo](https://github.com/Derek-Sykes/dylans-lawn-care-demo), with `dev` as its default branch. Authenticate GitHub on the second computer, then clone once:

```powershell
git clone --branch dev https://github.com/Derek-Sykes/dylans-lawn-care-demo.git
cd dylans-lawn-care-demo
.\start.bat
```

The GitHub **Check website container** workflow also has a **Run workflow** button for an on-demand check. It performs no publication. A manually triggered check passed for the delivery; do not confuse that observed run with a separately verified push-trigger test.

The updater follows the current `dev` or `main` branch. It refuses uncommitted/untracked work, feature branches, and history that cannot safely advance to the remote. It fetches and fast-forwards, builds and verifies a candidate, then replaces this local container. A failed build leaves the prior packaged container running; source may already have advanced. It never force-resets Git or removes unrelated Docker resources. In dev mode, edits are visible immediately because its source is mounted.

The included GitHub check builds the container, verifies the exact files and source revision, tests health and demo indexing headers, and checks private files are inaccessible. It has read-only repository permission. **There is no publish or deploy job, including on main.** Main remains reserved for a separately approved release. Repository protection settings must be configured/verified on the eventual private remote; files alone do not enforce them.

`/version.json` reports the packaged commit, with `-dirty` for a checkout containing uncommitted changes. In dev mode, this identifies the base build; live mounted edits can be newer. Read the parent client's QA record for the actual tested revision and visual checks.

## Recording sequence

1. Run `start.bat`. Open the local preview at normal zoom and hide development panels.
2. Pause on the opening photo and headline, then point out the direct contact action.
3. Scroll slowly through the supported services and authentic project photos.
4. Show the sourced customer review excerpts and the service-area/contact section.
5. Show the narrow mobile layout, menu and contact controls. Explain the next step as a quote conversation; do not trigger a call or send a message during the recording.

Aim for 60–90 seconds. Present this as a private draft for Dylan's review. Confirm copy, photo use, service area and the quote/contact process with the owner before launch.

## Eventual approved publication

This static `dist/` works unchanged on a static host or in the packaged Caddy image behind an HTTPS reverse proxy. The current image uses HTTP on internal port 8080 and a local loopback-only Compose binding. The pinned Caddy base supports the AMD64/ARM64 pattern already used by X Solutions; build the selected host's architecture when its environment is chosen.

After Dylan accepts the scope and approves content/assets, agree the domain and hosting ownership, purchase/connect the selected domain under fresh authorization, and configure HTTPS for that host. Replace the private-demo indexing controls only for the approved public release, set real canonical/metadata URLs where used, verify all contact destinations and the chosen quote process, then test domain HTTPS and mobile behavior. Add deployment automation only after the actual hosting route and release policy are approved. Do not use the X Solutions release job or live container for this client.

The same Git source and `dist/` are the publication inputs, so this transition does not require redesigning the page. No hosting destination or domain is assumed or configured here.

## Workflow provenance

The launcher behavior was adapted from the actual VoiceVault `voicevault.ps1`, `scripts/voicevault.ps1`, `dev/voicevault-dev.ps1` and development guide, plus the verified X Solutions Caddy/image workflow. Useful conventions retained: a single source tree, separate mounted and packaged modes, health-checked start, safe dev/main updates and intentional release gates. VoiceVault's database, AI tools, Portainer integration and credential volumes are unnecessary for this static demo and were not copied.
