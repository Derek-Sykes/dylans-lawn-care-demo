# Local operations and data recovery

`website.ps1` and `website` control only the project recorded in ignored `.local/runtime.env`. The regular stop/update paths never delete a volume. Docker Desktop must retain its data; resetting Docker Desktop or manually removing the booking volume can remove the installation.

## What belongs in a complete backup

- The entire named `<DYLAN_PROJECT>_booking-data` volume, including SQLite sidecar files and the encryption key.
- `.local/runtime.env`, which contains the project identity, selected ports and private bootstrap credential.
- The ignored `.env` only if you supplied optional Google credentials/settings there.

Keep these together in encrypted storage outside the repository. Database customer details are sensitive even when OAuth tokens are encrypted. Never attach the archive or local settings to a public issue, commit, image build or support log.

## Consistent backup

Run `stop` first so SQLite is closed cleanly. Read only the project name from `.local/runtime.env`; do not print the whole file. Choose a new private destination directory. The following PowerShell example uses a directory you create outside the checkout:

```powershell
.\website.ps1 stop
$backupDir = 'C:\Private backups\Dylan booking 2026-09-10'
New-Item -ItemType Directory -Path $backupDir -ErrorAction Stop | Out-Null
$projectLine = Get-Content -LiteralPath '.local/runtime.env' | Where-Object { $_ -like 'DYLAN_PROJECT=*' }
$bookingProject = ($projectLine -split '=', 2)[1]
docker run --rm --user 0 --entrypoint sh --mount "type=volume,source=$($bookingProject)_booking-data,target=/data,readonly" --mount "type=bind,source=$backupDir,target=/backup" "$bookingProject-booking:local" -c 'tar -czf /backup/booking-data.tgz -C /data .'
if ($LASTEXITCODE -ne 0) { throw 'Backup failed.' }
Copy-Item -LiteralPath '.local/runtime.env' -Destination (Join-Path $backupDir 'runtime.env')
if (Test-Path -LiteralPath '.env') { Copy-Item -LiteralPath '.env' -Destination (Join-Path $backupDir '.env') }
.\website.ps1 start -NoOpen
```

On macOS/Linux the same Docker archive command works with an absolute host backup path. Set `bookingProject` by reading only the `DYLAN_PROJECT=` line, use `./website stop` first, and copy the private state file without displaying it. Protect the resulting folder with your operating system's permissions and encrypted backup storage.

## Restore

Restore into a fresh checkout on a stopped installation. Copy the saved `runtime.env` to `.local/runtime.env` before the first start, and restore `.env` if it was part of the backup. Use the saved project name when creating the Docker volume. Do not overwrite a nonempty existing volume: choose a separate restore environment or preserve its own complete backup first.

Build the images with the launcher `build` command, create the named volume, then extract the archive as root. Preserve ownership from the archive because the backend runs as an unprivileged user:

```powershell
.\website.ps1 build -NoOpen
$projectLine = Get-Content -LiteralPath '.local/runtime.env' | Where-Object { $_ -like 'DYLAN_PROJECT=*' }
$bookingProject = ($projectLine -split '=', 2)[1]
docker volume create "$($bookingProject)_booking-data"
docker run --rm --user 0 --entrypoint sh --mount "type=volume,source=$($bookingProject)_booking-data,target=/data" --mount "type=bind,source=$backupDir,target=/backup,readonly" "$bookingProject-booking:local" -c 'test -z "$(ls -A /data)" && tar -xzf /backup/booking-data.tgz -C /data'
if ($LASTEXITCODE -ne 0) { throw 'Restore refused or failed; the volume must be empty.' }
.\website.ps1 start
```

Named volumes may initially inherit an empty `/data` directory owned by the runtime user; the archive restores its stored files and ownership. Verify the owner settings and existing booking records after recovery, then reconnect Google only if needed. Restoring the database without its matching encryption key or bootstrap state is incomplete.

## Checks and isolated fixtures

`check` uses a random `dylan-check-...` project and its own temporary data volume. The explicit `down --volumes` in that check is limited to the newly generated test project. The normal launcher stop path uses `down` without a volume-removal flag.

Run `./scripts/test-launchers.ps1` from PowerShell after a successful local build. Its test source commits exist only inside ignored `.local/launcher-tests/`; its remote is another local fixture directory. It does not commit, fetch, push or reset the actual working repository. Test containers use explicit `dylan-launcher-test-...` names and are removed by exact project labels. Fixture files and the compact result record remain for inspection.

The suite checks fresh installation identities, occupied ports, two simultaneous clones, common PowerShell/Bash state, stopped-stack restart, source-mounted edits, dirty/feature/ahead update refusal, fast-forward update and database persistence. Google OAuth is covered separately; this suite needs no Google account or credentials.

## Future production template

`compose.booking.production.yaml` is intentionally separate from the active static production file. It publishes no host ports. A future shared HTTPS proxy would route the public hostname to the web service's port 8080 and the dedicated admin hostname to the booking service's port 8082. Port 8081 is the internal public API only. Both exact HTTPS origins and a separately approved Google web-client configuration would be required.

Do not run this template through the existing static release job. Register routing, backups, secret storage, health checks and release/rollback behavior only in a separately authorized deployment task.
