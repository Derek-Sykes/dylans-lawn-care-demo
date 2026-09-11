param([string]$BashPath = '')
$ErrorActionPreference = 'Stop'
$siteRoot = Split-Path $PSScriptRoot -Parent
$runId = [guid]::NewGuid().ToString('N').Substring(0,10)
$qaRoot = Join-Path $siteRoot ".local/launcher-tests/$runId"
$seed = Join-Path $qaRoot 'source fixture'
$cloneA = Join-Path $qaRoot 'clone a'
$cloneB = Join-Path $qaRoot 'clone b'
$privateFixture = Join-Path $qaRoot 'private fixture'
$privateOffline = Join-Path $qaRoot 'private fixture offline'
$dummySecret = "fixture-only-not-a-google-secret-$runId"
$projectA = "dylan-launcher-test-$runId-a"
$projectB = "dylan-launcher-test-$runId-b"
$blocker = "dylan-launcher-test-$runId-port"
$ps = if (Get-Command pwsh -ErrorAction SilentlyContinue) { (Get-Command pwsh).Source } else { (Get-Command powershell).Source }
if (-not $BashPath) {
  $git = (Get-Command git).Source
  $candidate = Join-Path (Split-Path (Split-Path $git -Parent) -Parent) 'bin/bash.exe'
  if (Test-Path -LiteralPath $candidate) { $BashPath = $candidate }
  elseif ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT -and (Get-Command bash -ErrorAction SilentlyContinue)) { $BashPath = (Get-Command bash).Source }
}
$results = [Collections.Generic.List[string]]::new()
function Native([string]$Program,[string[]]$Arguments) {
  & $Program @Arguments
  if ($LASTEXITCODE -ne 0) { throw "$Program failed ($LASTEXITCODE)." }
}
function Record([string]$Name) { $results.Add($Name); Write-Host "PASS: $Name" }
function State([string]$Clone) {
  $values = @{}
  Get-Content -LiteralPath (Join-Path $Clone '.local/runtime.env') | ForEach-Object {
    if ($_ -match '^([A-Z_]+)=(.*)$') { $values[$Matches[1]] = $Matches[2] }
  }
  return $values
}
function Launch([string]$Clone,[string]$Action,[switch]$Bash,[switch]$Refuse,[string]$ExpectedMessage = '') {
  Push-Location $Clone
  try {
    $previous = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
    if ($Bash) { $output = (& $BashPath ./website $Action --no-open 2>&1 | Out-String) }
    else { $output = (& $ps -NoProfile -ExecutionPolicy Bypass -File ./website.ps1 $Action -NoOpen 2>&1 | Out-String) }
    $code = $LASTEXITCODE; $ErrorActionPreference = $previous
    $testState = State $Clone
    if ($output.Contains($testState.BOOTSTRAP_TOKEN)) { throw 'Launcher printed a private bootstrap token.' }
    if ($output.Contains($dummySecret)) { throw 'Launcher printed dummy private configuration.' }
    if (Get-ChildItem -LiteralPath (Join-Path $Clone '.local') -Directory -Filter 'gc-*') { throw 'Private temporary repository survived launcher completion.' }
    if ($ExpectedMessage -and -not $output.Contains($ExpectedMessage)) { throw "Expected safe setup result was not reported: $ExpectedMessage. Output: $output" }
    if ($Refuse) {
      if ($code -eq 0) { throw "$Action should have refused unsafe source." }
    } elseif ($code -ne 0) { throw "$Action failed: $output" }
    Write-Host "$Action completed for $(Split-Path $Clone -Leaf)."
  } finally { Pop-Location }
}
function SettingsFixture([hashtable]$Settings) {
  # Launcher tests exercise persistent storage without faking a Google login.
  # Authentication/CSRF and operator permissions have separate application tests.
  if ($Settings.DYLAN_PROJECT -notmatch '^dylan-launcher-test-[a-f0-9]{10}-[ab]$') { throw 'Refused settings access outside an isolated launcher fixture.' }
  return @{Volume="$($Settings.DYLAN_PROJECT)_booking-data"}
}
function ReadOwnerSettings([hashtable]$Auth) {
  $output = & docker run --rm --network none --mount "type=volume,source=$($Auth.Volume),target=/data,readonly" python:3.14-alpine python -c 'import sqlite3; c=sqlite3.connect("file:/data/booking.sqlite?mode=ro",uri=True); v=c.execute("SELECT value FROM meta WHERE key=?",("settings",)).fetchone()[0]; print(v.decode() if isinstance(v,bytes) else v)'
  if ($LASTEXITCODE -ne 0) { throw 'Could not read isolated fixture settings.' }
  return $output | ConvertFrom-Json
}
function SaveOwnerSettings([hashtable]$Auth,$Settings) {
  $Settings | ConvertTo-Json -Depth 10 -Compress | & docker run --rm -i --network none --mount "type=volume,source=$($Auth.Volume),target=/data" python:3.14-alpine python -c 'import json,sqlite3,sys; v=json.load(sys.stdin); c=sqlite3.connect("/data/booking.sqlite"); c.execute("UPDATE meta SET value=? WHERE key=?",(json.dumps(v).encode(),"settings")); c.commit()'
  if ($LASTEXITCODE -ne 0) { throw 'Could not save isolated fixture settings.' }
}
function CleanProject([string]$Project) {
  $previous = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  try {
    $containers = & docker ps -aq --filter "label=com.docker.compose.project=$Project"
    if ($containers) { & docker rm --force @containers 2>&1 | Out-Null }
    & docker network rm "$($Project)_default" 2>&1 | Out-Null
    & docker volume rm "$($Project)_booking-data" 2>&1 | Out-Null
  } finally { $ErrorActionPreference = $previous }
}
try {
  # Only fixture copies receive this local URL. Production launchers keep a fixed GitHub destination.
  New-Item -ItemType Directory -Path $privateFixture -Force | Out-Null
  Native git @('-C',$privateFixture,'init','--initial-branch=dev')
  Native git @('-C',$privateFixture,'config','user.name','Isolated launcher test')
  Native git @('-C',$privateFixture,'config','user.email','launcher-test@example.invalid')
  [IO.File]::WriteAllText((Join-Path $privateFixture 'README.md'),'Dummy private setup fixture; never a real credential.')
  Native git @('-C',$privateFixture,'add','README.md')
  Native git @('-C',$privateFixture,'commit','-m','Empty private configuration fixture')
  $privateFixtureURL = ([uri]($privateFixture + [IO.Path]::DirectorySeparatorChar)).AbsoluteUri.TrimEnd('/')
  New-Item -ItemType Directory -Path $seed -Force | Out-Null
  foreach ($name in @('dist','booking','scripts','website','website.ps1','Dockerfile','Caddyfile','Caddyfile.local','compose.local.yaml','compose.dev.yaml','.dockerignore','.gitignore','.gitattributes')) {
    Copy-Item -LiteralPath (Join-Path $siteRoot $name) -Destination $seed -Recurse
  }
  foreach ($launcher in @('website','website.ps1')) {
    $path = Join-Path $seed $launcher
    $text = [IO.File]::ReadAllText($path).Replace('https://github.com/Derek-Sykes/xsolutions-booking-private.git',$privateFixtureURL)
    [IO.File]::WriteAllText($path,$text,(New-Object Text.UTF8Encoding($false)))
  }
  Native git @('-C',$seed,'init','--initial-branch=dev')
  Native git @('-C',$seed,'config','user.name','Isolated launcher test')
  Native git @('-C',$seed,'config','user.email','launcher-test@example.invalid')
  Native git @('-C',$seed,'add','.')
  Native git @('-C',$seed,'update-index','--chmod=+x','website')
  Native git @('-C',$seed,'commit','-m','Local launcher fixture')
  Native git @('clone','--no-hardlinks',$seed,$cloneA)
  Native git @('clone','--no-hardlinks',$seed,$cloneB)
  Launch $cloneA status
  Launch $cloneB status -Bash:([bool]$BashPath)
  $initialA = State $cloneA; $initialB = State $cloneB
  if ($initialA.DYLAN_PROJECT -eq $initialB.DYLAN_PROJECT -or $initialA.BOOTSTRAP_TOKEN -eq $initialB.BOOTSTRAP_TOKEN) { throw 'Fresh clones did not receive separate identities.' }
  Record 'Fresh clones get different project identities and setup credentials'
  $guard = Join-Path $cloneA '.local/launcher.guard'
  New-Item -ItemType Directory -Path $guard | Out-Null
  try {
    Launch $cloneA status -Refuse
    if ($BashPath) { Launch $cloneA status -Bash -Refuse }
  } finally { Remove-Item -LiteralPath $guard }
  Record 'Shared guard rejects simultaneous launchers across both shells'
  $sourceState = State $siteRoot
  $image = "$($sourceState.DYLAN_PROJECT):local"
  Native docker @('run','--detach','--name',$blocker,'--publish','127.0.0.1::8080',$image)
  $binding = (& docker port $blocker '8080/tcp').Trim()
  if ($binding -notmatch '^127\.0\.0\.1:(\d+)$') { throw 'Could not read isolated blocker port.' }
  $occupied = [int]$Matches[1]
  $preferredAdmin = if ($occupied -eq 65535) {1024} else {$occupied+1}
  [IO.File]::WriteAllText((Join-Path $cloneA '.env'), (@("DYLAN_PROJECT=$projectA","DYLAN_PORT=$occupied","DYLAN_ADMIN_PORT=$preferredAdmin",'') -join [char]10))
  [IO.File]::WriteAllText((Join-Path $cloneB '.env'), (@("DYLAN_PROJECT=$projectB","DYLAN_PORT=$occupied","DYLAN_ADMIN_PORT=$preferredAdmin",'') -join [char]10))
  Launch $cloneA start -Refuse -ExpectedMessage 'needs a valid google-client.json on dev'
  Launch $cloneB start -Bash:([bool]$BashPath) -Refuse -ExpectedMessage 'needs a valid google-client.json on dev'
  Record 'Missing private file fails safely in both launchers and removes the temporary checkout'
  $publicClient = Get-Content -LiteralPath (Join-Path $seed 'booking/config/google-client.json') -Raw | ConvertFrom-Json
  $dummyJSON = @{installed=@{client_id=$publicClient.client_id;client_secret=$dummySecret}} | ConvertTo-Json -Depth 3
  [IO.File]::WriteAllText((Join-Path $privateFixture 'google-client.json'),$dummyJSON,(New-Object Text.UTF8Encoding($false)))
  [IO.File]::WriteAllText((Join-Path $privateFixture 'operator-access.json'),'{"schema":1,"googleSub":"launcher-fixture-operator","email":"operator@example.com"}',(New-Object Text.UTF8Encoding($false)))
  Native git @('-C',$privateFixture,'add','google-client.json','operator-access.json')
  Native git @('-C',$privateFixture,'commit','-m','Add synthetic test-only client configuration')
  Launch $cloneA start -ExpectedMessage 'Google connection configuration saved privately'
  Launch $cloneB start -Bash:([bool]$BashPath) -ExpectedMessage 'Google connection configuration saved privately'
  Record 'Fresh PowerShell and Bash installs retrieve and import dummy private Git configuration without printing it'
  if ((Get-Item -LiteralPath $privateFixture).Parent.FullName -ne (Get-Item -LiteralPath $qaRoot).FullName -or
      [IO.Path]::GetFullPath((Split-Path $privateOffline -Parent)) -ne [IO.Path]::GetFullPath($qaRoot)) { throw 'Private fixture move escaped the isolated test directory.' }
  Move-Item -LiteralPath $privateFixture -Destination $privateOffline
  $stateA = State $cloneA; $stateB = State $cloneB
  $ports = @($stateA.DYLAN_PORT,$stateA.DYLAN_ADMIN_PORT,$stateB.DYLAN_PORT,$stateB.DYLAN_ADMIN_PORT)
  if ($ports -contains "$occupied" -or @($ports | Select-Object -Unique).Count -ne 4) { throw 'Collision handling did not produce four independent usable ports.' }
  Record 'Occupied ports and two simultaneous clones select separate loopback addresses'
  [IO.File]::WriteAllText((Join-Path $cloneA '.env'), "DYLAN_PROJECT=$projectA$([char]10)")
  [IO.File]::WriteAllText((Join-Path $cloneB '.env'), "DYLAN_PROJECT=$projectB$([char]10)")
  $auth = SettingsFixture $stateA
  $settings = ReadOwnerSettings $auth
  $settings.businessName = "Persistence check $runId"
  SaveOwnerSettings $auth $settings
  $stateHash = (Get-FileHash -LiteralPath (Join-Path $cloneA '.local/runtime.env')).Hash
  Launch $cloneA stop
  Launch $cloneA start
  $restarted = State $cloneA
  if ((Get-FileHash -LiteralPath (Join-Path $cloneA '.local/runtime.env')).Hash -ne $stateHash) { throw 'Restart changed saved identity, ports or credential.' }
  $auth = SettingsFixture $restarted
  if ((ReadOwnerSettings $auth).businessName -ne $settings.businessName) { throw 'Restart lost database settings.' }
  Record 'Stop/start retains ports, install secret and database settings'
  Record 'Configured installs restart with their private Git source unavailable'
  if ($BashPath) {
    Launch $cloneA status -Bash
    if ((Get-FileHash -LiteralPath (Join-Path $cloneA '.local/runtime.env')).Hash -ne $stateHash) { throw 'Bash changed PowerShell install settings.' }
    Record 'PowerShell and Bash reuse the same private installation'
  }
  Launch $cloneA dev
  $index = Join-Path $cloneA 'dist/index.html'
  $original = [IO.File]::ReadAllText($index)
  [IO.File]::WriteAllText($index, $original.Replace('</body>', "<p>Mounted fixture $runId</p></body>"))
  $page = Invoke-WebRequest "http://127.0.0.1:$($stateA.DYLAN_PORT)/" -UseBasicParsing
  if ($page.Content -notmatch "Mounted fixture $runId") { throw 'Dev mount did not show source edits.' }
  Launch $cloneA update -Refuse
  [IO.File]::WriteAllText($index,$original)
  Record 'Dev source edits appear immediately and dirty update is refused'
  [IO.File]::WriteAllText((Join-Path $seed 'fixture-update.txt'),'A harmless source change from the local fixture remote.')
  Native git @('-C',$seed,'add','fixture-update.txt')
  Native git @('-C',$seed,'commit','-m','Advance isolated fixture')
  Launch $cloneA update
  $head = (& git -C $cloneA rev-parse HEAD).Trim()
  $expected = (& git -C $seed rev-parse HEAD).Trim()
  if ($head -ne $expected) { throw 'Fast-forward update did not follow the local fixture remote.' }
  if ((Get-FileHash -LiteralPath (Join-Path $cloneA '.local/runtime.env')).Hash -ne $stateHash) { throw 'Update changed saved state.' }
  $auth = SettingsFixture (State $cloneA)
  if ((ReadOwnerSettings $auth).businessName -ne $settings.businessName) { throw 'Update lost database settings.' }
  $web = (& docker ps -q --filter "label=com.docker.compose.project=$projectA" --filter 'label=com.docker.compose.service=web').Trim()
  $mounts = & docker inspect $web --format '{{json .Mounts}}' | ConvertFrom-Json
  if ($mounts | Where-Object Destination -eq '/srv') { throw 'Update did not return to packaged mode.' }
  Record 'Safe fast-forward update preserves data and private state and returns to packaged mode'
  Native git @('-C',$cloneA,'switch','-c','feature/refusal-check')
  Launch $cloneA update -Refuse
  Native git @('-C',$cloneA,'switch','dev')
  Native git @('-C',$cloneA,'-c','user.name=Local test','-c','user.email=test@example.invalid','commit','--allow-empty','-m','Ahead fixture')
  Launch $cloneA update -Refuse
  Record 'Feature branch and ahead-of-remote updates refuse safely'
  $results | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $qaRoot 'results.json')
  Write-Host "Launcher regression checks passed: $($results.Count). Record: $qaRoot/results.json"
} finally {
  CleanProject $projectA
  CleanProject $projectB
  $previous = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  try { & docker rm --force $blocker 2>&1 | Out-Null } finally { $ErrorActionPreference = $previous }
  Write-Host "Isolated fixture files remain at $qaRoot (ignored by Git)."
}
