param([string]$Image = '', [string]$BashPath = '')
$ErrorActionPreference = 'Stop'
$siteRoot = Split-Path $PSScriptRoot -Parent
$runId = [guid]::NewGuid().ToString('N').Substring(0,10)
$qaRoot = Join-Path $siteRoot ".local/google-setup-tests/$runId"
$fixture = Join-Path $qaRoot 'private fixture'
$dummySecret = "dummy-google-client-secret-$runId"
$fixedURL = 'https://github.com/Derek-Sykes/xsolutions-booking-private.git'
$clientID = (Get-Content -LiteralPath (Join-Path $siteRoot 'booking/config/google-client.json') -Raw | ConvertFrom-Json).client_id
$results = [Collections.Generic.List[string]]::new()
$containers = [Collections.Generic.List[string]]::new()
$environmentKeys = @('TEST_BOOKING_CONTAINER','GIT_CONFIG_COUNT','GIT_CONFIG_KEY_0','GIT_CONFIG_VALUE_0')
$snapshot = @{}
foreach ($key in $environmentKeys) { $snapshot[$key] = [Environment]::GetEnvironmentVariable($key) }
function Native([string]$Program,[string[]]$Arguments) {
  & $Program @Arguments
  if ($LASTEXITCODE -ne 0) { throw "Test command failed: $Program ($LASTEXITCODE)." }
}
function WriteUTF8([string]$Path,[string]$Text) { [IO.File]::WriteAllText($Path,$Text,(New-Object Text.UTF8Encoding($false))) }
function CommitFixture([string]$Content) {
  if ($null -ne $Content -and $Content.Length) { WriteUTF8 (Join-Path $fixture 'google-client.json') $Content }
  Native git @('-C',$fixture,'add','.')
  Native git @('-C',$fixture,'commit','--quiet','--allow-empty','-m','Synthetic configuration case')
}
function RunCase($Engine,[string]$Name,[string]$Expected,[bool]$Success) {
  $previous=$ErrorActionPreference; $ErrorActionPreference='Continue'
  try {
    if ($Engine.Bash) { $output=(& $Engine.Path (Join-Path $qaRoot 'harness.sh') 2>&1 | Out-String) }
    else { $output=(& $Engine.Path -NoProfile -ExecutionPolicy Bypass -File (Join-Path $qaRoot 'harness.ps1') 2>&1 | Out-String) }
    $code=$LASTEXITCODE
  } finally { $ErrorActionPreference=$previous }
  if ($output.Contains($dummySecret)) { throw 'Private fixture contents leaked into launcher output.' }
  if (($code -eq 0) -ne $Success -or ($Expected -and -not $output.Contains($Expected))) { throw "$($Engine.Name) $Name failed: $output" }
  if (Get-ChildItem -LiteralPath (Join-Path $qaRoot '.local') -Directory -Filter 'gc-*') { throw 'A temporary private checkout survived.' }
  $results.Add("$($Engine.Name): $Name")
  Write-Host "PASS: $($Engine.Name): $Name"
}
try {
  New-Item -ItemType Directory -Path $fixture -Force | Out-Null
  New-Item -ItemType Directory -Path (Join-Path $qaRoot '.local') -Force | Out-Null
  Native git @('-C',$fixture,'init','--quiet','--initial-branch=main')
  Native git @('-C',$fixture,'config','user.name','Synthetic setup test')
  Native git @('-C',$fixture,'config','user.email','setup-test@example.invalid')
  WriteUTF8 (Join-Path $fixture 'README.md') 'Synthetic client configuration only.'
  CommitFixture ''
  $fixtureURL = ([uri]($fixture + [IO.Path]::DirectorySeparatorChar)).AbsoluteUri.TrimEnd('/')
  $tokens=$null; $errors=$null
  $ast=[Management.Automation.Language.Parser]::ParseFile((Join-Path $siteRoot 'website.ps1'),[ref]$tokens,[ref]$errors)
  if ($errors) { throw 'PowerShell launcher does not parse.' }
  $functions = foreach ($name in @('Invoke-Native','Invoke-PrivateProcess','Ensure-GoogleConfig')) {
    $fn=$ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name},$false)
    if (-not $fn) { throw "Missing launcher function $name." }
    if ($name -eq 'Invoke-PrivateProcess') { $fn.Extent.Text.Replace('function Invoke-PrivateProcess','function Invoke-CapturedProcess') }
    else { $fn.Extent.Text.Replace($fixedURL,$fixtureURL) }
  }
  # Adapt only the extracted fixture harness transport; production code has no test URL or container override.
  $psPrelude = @'
$ErrorActionPreference='Stop'
$script:SiteRoot=$PSScriptRoot
function Get-Compose { return @() }
function Invoke-PrivateProcess([string]$Program,[string[]]$Arguments,[string]$InputText='') {
  if ($Program -eq 'docker') { $Arguments=@('exec','-i',$env:TEST_BOOKING_CONTAINER,'booking')+$Arguments[4..($Arguments.Length-1)] }
  return Invoke-CapturedProcess $Program $Arguments $InputText
}
'@
  WriteUTF8 (Join-Path $qaRoot 'harness.ps1') ($psPrelude + [char]10 + ($functions -join [char]10) + [char]10 + 'try { Ensure-GoogleConfig } catch { Write-Host $_.Exception.Message; exit 1 }')
  $bashSource=[IO.File]::ReadAllText((Join-Path $siteRoot 'website'))
  $bashFunctions=[regex]::Match($bashSource,'(?ms)^private_git\(\) \(\r?\n.*?(?=^check_application\(\))').Value
  if (-not $bashFunctions) { throw 'Missing Bash private setup functions.' }
  $bashPrelude=@'
#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
case "$(uname -s)" in MINGW*|MSYS*) ROOT=$(cygpath -m "$ROOT"); export MSYS_NO_PATHCONV=1 ;; esac
compose() { shift 3; docker exec -i "$TEST_BOOKING_CONTAINER" "$@"; }
'@
  WriteUTF8 (Join-Path $qaRoot 'harness.sh') (($bashPrelude + [char]10 + $bashFunctions.Replace($fixedURL,$fixtureURL) + [char]10 + 'ensure_google_config' + [char]10).Replace(([string][char]13),''))
  $engines=@()
  foreach ($program in @('pwsh','powershell')) {
    $application=Get-Command $program -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($application) { $engines+=@{Name=$program;Path=$application.Source;Bash=$false} }
  }
  if (-not $BashPath) {
    $git=(Get-Command git -CommandType Application | Select-Object -First 1).Source
    $candidate=Join-Path (Split-Path (Split-Path $git -Parent) -Parent) 'bin/bash.exe'
    if (Test-Path -LiteralPath $candidate) { $BashPath=$candidate }
    elseif ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { $BashPath=(Get-Command bash).Source }
  }
  if ($BashPath) { $engines+=@{Name='bash';Path=$BashPath;Bash=$true} }
  if (-not $Image) {
    $Image="dylan-google-setup-test-$runId"
    Native docker @('build','--tag',$Image,(Join-Path $siteRoot 'booking'))
  }
  foreach ($engine in $engines) {
    $container="dylan-google-setup-test-$runId-$($engine.Name)"
    $containers.Add($container)
    $env:TEST_BOOKING_CONTAINER=$container
    Native docker @('run','--detach','--name',$container,'--network','none','--no-healthcheck',
      '--env','BOOTSTRAP_TOKEN=synthetic-test-bootstrap-credential-only-123456789',
      '--env',"GOOGLE_CLIENT_ID=$clientID",'--env','GOOGLE_OAUTH_MODE=desktop',
      '--env','PUBLIC_ORIGIN=http://127.0.0.1:4177','--env','ADMIN_ORIGIN=http://127.0.0.1:4178',
      '--entrypoint','sh',$Image,'-c','sleep 600')
    # Every engine starts with its own empty database; only dummy Git data is ever imported.
    $file=Join-Path $fixture 'google-client.json'
    if (Test-Path -LiteralPath $file) { Remove-Item -LiteralPath $file }
    Native git @('-C',$fixture,'add','-A')
    CommitFixture ''
    $offline = Join-Path $qaRoot 'private fixture offline'
    if ((Get-Item -LiteralPath $fixture).Parent.FullName -ne (Get-Item -LiteralPath $qaRoot).FullName -or
        [IO.Path]::GetFullPath((Split-Path $offline -Parent)) -ne [IO.Path]::GetFullPath($qaRoot)) { throw 'Fixture move escaped the test directory.' }
    Move-Item -LiteralPath $fixture -Destination $offline
    try { RunCase $engine 'private repository unavailable and cleanup' 'Could not access private Google setup' $false }
    finally {
      if ((Get-Item -LiteralPath $offline).Parent.FullName -ne (Get-Item -LiteralPath $qaRoot).FullName) { throw 'Fixture restore escaped the test directory.' }
      Move-Item -LiteralPath $offline -Destination $fixture
    }
    RunCase $engine 'missing file and cleanup' 'needs a valid google-client.json on main' $false
    CommitFixture ('x' * 65537)
    RunCase $engine 'oversize file and cleanup' 'needs a valid google-client.json on main' $false
    CommitFixture ('{"client_id":"wrong.apps.googleusercontent.com","client_secret":"' + $dummySecret + '"}')
    RunCase $engine 'mismatched registered client and no output leak' 'could not be imported' $false
    CommitFixture ('not-json-' + $dummySecret)
    RunCase $engine 'malformed file and no output leak' 'could not be imported' $false
    $env:GIT_CONFIG_COUNT='1'; $env:GIT_CONFIG_KEY_0='url.https://invalid.example/blocked.insteadOf'; $env:GIT_CONFIG_VALUE_0=$fixtureURL
    RunCase $engine 'unexpected URL rewrite rejected before network' 'unexpected destination' $false
    foreach ($key in @('GIT_CONFIG_COUNT','GIT_CONFIG_KEY_0','GIT_CONFIG_VALUE_0')) { [Environment]::SetEnvironmentVariable($key,$null) }
    $valid=@{installed=@{client_id=$clientID;client_secret=$dummySecret}} | ConvertTo-Json -Depth 3
    CommitFixture $valid
    RunCase $engine 'UTF-8 stdin import and private temporary cleanup' 'configuration saved privately' $true
    $env:GIT_CONFIG_COUNT='1'; $env:GIT_CONFIG_KEY_0='url.https://invalid.example/blocked.insteadOf'; $env:GIT_CONFIG_VALUE_0=$fixtureURL
    RunCase $engine 'configured install skips all private Git work' '' $true
    foreach ($key in @('GIT_CONFIG_COUNT','GIT_CONFIG_KEY_0','GIT_CONFIG_VALUE_0')) { [Environment]::SetEnvironmentVariable($key,$null) }
  }
  $results | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $qaRoot 'results.json')
  Write-Host "Private setup checks passed: $($results.Count). Record: $qaRoot/results.json"
} finally {
  $previous = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
  try { foreach ($container in $containers) { & docker rm --force $container 2>&1 | Out-Null } }
  finally { $ErrorActionPreference = $previous }
  foreach ($key in $environmentKeys) { [Environment]::SetEnvironmentVariable($key,$snapshot[$key]) }
}
