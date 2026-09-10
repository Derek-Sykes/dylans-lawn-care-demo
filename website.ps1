param(
  [ValidateSet('build','start','dev','update','stop','status','logs','check','open','help')]
  [string]$Command = 'start',
  [switch]$NoOpen
)
$ErrorActionPreference = 'Stop'
$script:SiteRoot = $PSScriptRoot

function Invoke-Native([string]$Program, [string[]]$Arguments) {
  & $Program @Arguments
  if ($LASTEXITCODE -ne 0) { throw "$Program failed with exit code $LASTEXITCODE." }
}

function Get-Setting([string]$Name, [string]$Default) {
  $value = [Environment]::GetEnvironmentVariable($Name)
  if ($value) { return $value }
  $envFile = Join-Path $script:SiteRoot '.env'
  if (Test-Path -LiteralPath $envFile) {
    $line = Get-Content -LiteralPath $envFile | Where-Object { $_ -match "^$Name=" } | Select-Object -Last 1
    if ($line) { return ($line -split '=', 2)[1].Trim() }
  }
  return $Default
}

function Assert-Docker {
  if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'Install Docker Desktop and select Linux containers, then try again.' }
  Invoke-Native docker @('compose','version')
  & docker info --format '{{.OSType}}' | Out-Null
  if ($LASTEXITCODE -ne 0) { throw 'Start Docker Desktop and wait until its engine is ready.' }
}

function Set-Revision {
  $env:DYLAN_REVISION = 'local'
  if ((Get-Command git -ErrorAction SilentlyContinue) -and (Test-Path -LiteralPath (Join-Path $script:SiteRoot '.git'))) {
    $revision = & git rev-parse HEAD
    if ($LASTEXITCODE -eq 0 -and $revision) {
      $env:DYLAN_REVISION = $revision.Trim()
      $dirty = & git status --porcelain --untracked-files=all
      if ($LASTEXITCODE -ne 0) { throw 'Could not inspect Git changes.' }
      if ($dirty) { $env:DYLAN_REVISION += '-dirty' }
    }
  }
}

function Build-Site {
  if (-not (Test-Path -LiteralPath 'dist/index.html')) { throw 'The public page dist/index.html is missing.' }
  Set-Revision
  Invoke-Native docker ($script:Compose + @('build'))
  Invoke-Native docker @('run','--rm',$script:Image,'caddy','validate','--config','/etc/caddy/Caddyfile','--adapter','caddyfile')
}

function Test-Image {
  $checkName = "dylan-check-$([guid]::NewGuid().ToString('N').Substring(0,10))"
  $download = Join-Path ([System.IO.Path]::GetTempPath()) "$checkName.download"
  $started = $false
  try {
    Invoke-Native docker @('run','--detach','--rm','--name',$checkName,'--publish','127.0.0.1::8080',$script:Image)
    $started = $true
    $address = & docker port $checkName '8080/tcp'
    if ($LASTEXITCODE -ne 0 -or $address -notmatch '^127\.0\.0\.1:(\d+)$') { throw 'Could not find the isolated container check port.' }
    $baseUrl = "http://127.0.0.1:$($Matches[1])"
    $ready = $false
    for ($attempt = 0; $attempt -lt 25; $attempt++) {
      try {
        $health = Invoke-WebRequest "$baseUrl/healthz" -UseBasicParsing -TimeoutSec 2
        if ($health.StatusCode -eq 200) { $ready = $true; break }
      } catch { Start-Sleep -Milliseconds 300 }
    }
    if (-not $ready) { throw 'The candidate container did not become ready.' }
    $index = Invoke-WebRequest "$baseUrl/" -UseBasicParsing -TimeoutSec 10
    if ($index.Headers['X-Robots-Tag'] -notmatch 'noindex') { throw 'Demo indexing protection is missing.' }
    $version = Invoke-RestMethod "$baseUrl/version.json" -TimeoutSec 10
    if ($version.revision -ne $env:DYLAN_REVISION) { throw 'The packaged source revision does not match the build.' }
    $publicRoot = (Resolve-Path -LiteralPath 'dist').Path
    $files = @(Get-ChildItem -LiteralPath $publicRoot -Recurse -File)
    foreach ($file in $files) {
      $relative = $file.FullName.Substring($publicRoot.Length + 1).Replace('\','/')
      $encodedPath = (($relative -split '/') | ForEach-Object { [Uri]::EscapeDataString($_) }) -join '/'
      Invoke-WebRequest "$baseUrl/$encodedPath" -UseBasicParsing -TimeoutSec 20 -OutFile $download
      if ((Get-FileHash -LiteralPath $download).Hash -ne (Get-FileHash -LiteralPath $file.FullName).Hash) { throw "Served file differs: $relative" }
    }
    foreach ($privatePath in @('/.git/config','/.env','/README.md','/Dockerfile')) {
      $status = 0
      try { $status = (Invoke-WebRequest "$baseUrl$privatePath" -UseBasicParsing -TimeoutSec 5).StatusCode }
      catch { if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode } else { throw } }
      if ($status -ne 404) { throw "Unexpected public access to $privatePath ($status)." }
    }
    Write-Host "Container checks passed: $($files.Count) public files match; revision, health, demo headers and private-file exclusions verified."
  } finally {
    if ($started) { & docker rm --force $checkName | Out-Null }
    if (Test-Path -LiteralPath $download) { Remove-Item -LiteralPath $download -Force }
  }
}

function Show-Site {
  Write-Host "Local preview: $script:SiteUrl"
  if (-not $NoOpen) {
    try { Start-Process $script:SiteUrl }
    catch { Write-Warning "Open $script:SiteUrl in your browser." }
  }
}

function Update-Source {
  if (-not (Get-Command git -ErrorAction SilentlyContinue)) { throw 'Git is required for update.' }
  if (-not (Test-Path -LiteralPath '.git')) { throw 'This folder is not a Git clone. Use start for the local demo.' }
  $branch = & git branch --show-current
  if ($LASTEXITCODE -ne 0 -or $branch -notin @('dev','main')) { throw 'Update follows the current dev or main branch. Switch to dev before updating.' }
  $dirty = & git status --porcelain --untracked-files=all
  if ($LASTEXITCODE -ne 0) { throw 'Could not inspect Git changes.' }
  if ($dirty) { throw 'Commit or stash your local changes before update. No files were overwritten.' }
  Invoke-Native git @('fetch','origin',$branch)
  & git merge-base --is-ancestor HEAD "origin/$branch"
  if ($LASTEXITCODE -ne 0) { throw 'Your branch is ahead of or diverged from origin. Update will not reset your work.' }
  Invoke-Native git @('merge','--ff-only',"origin/$branch")
}

Push-Location $script:SiteRoot
try {
  $port = Get-Setting 'DYLAN_PORT' '4177'
  $project = Get-Setting 'DYLAN_PROJECT' 'dylans-lawn-demo'
  if ($port -notmatch '^\d+$' -or [int]$port -lt 1024 -or [int]$port -gt 65535) { throw 'DYLAN_PORT must be a port from 1024 to 65535.' }
  if ($project -notmatch '^[a-z0-9][a-z0-9_-]*$') { throw 'DYLAN_PROJECT must use lowercase letters, numbers, underscores or hyphens.' }
  $env:DYLAN_PORT = $port
  $env:DYLAN_PROJECT = $project
  $script:Image = "${project}:local"
  $script:SiteUrl = "http://127.0.0.1:$port/"
  $script:Compose = @('compose','--project-directory',$script:SiteRoot,'--file','compose.local.yaml')
  if ($Command -eq 'help') {
    Write-Host 'Usage: .\website.ps1 start|dev|build|check|update|stop|status|logs|open [-NoOpen]'
    Write-Host 'start builds and checks the packaged demo. dev mounts dist for immediate edit/refresh.'
    Write-Host 'update safely pulls the current dev/main branch, then builds, checks and starts locally.'
  } elseif ($Command -eq 'open') { Show-Site }
  else {
    Assert-Docker
    switch ($Command) {
      { $_ -in @('build','start','dev','check','update') } {
        if ($Command -eq 'update') { Update-Source }
        Build-Site
        Test-Image
        if ($Command -in @('start','dev','update')) {
          $runCompose = $script:Compose
          if ($Command -eq 'dev') { $runCompose += @('--file','compose.dev.yaml') }
          Invoke-Native docker ($runCompose + @('up','--detach','--no-build','--wait','--wait-timeout','90'))
          Show-Site
        }
      }
      'stop' { Invoke-Native docker ($script:Compose + @('down')) }
      'status' { Invoke-Native docker ($script:Compose + @('ps')) }
      'logs' { Invoke-Native docker ($script:Compose + @('logs','--tail','80')) }
    }
  }
} catch {
  Write-Host "Dylan's Lawn Care: $($_.Exception.Message)" -ForegroundColor Red
  exit 1
} finally { Pop-Location }
exit 0
