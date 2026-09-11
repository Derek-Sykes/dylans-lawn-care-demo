param(
  [ValidateSet('build','start','dev','update','stop','status','logs','check','open','help')]
  [string]$Command = 'start',
  [switch]$NoOpen
)
$ErrorActionPreference = 'Stop'
$script:SiteRoot = $PSScriptRoot
$script:StatePath = Join-Path $PSScriptRoot '.local/runtime.env'
$script:StateLock = $null
$script:InitialEnvironment = @{}
foreach ($key in @('DYLAN_INSTALL_ID','DYLAN_PROJECT','DYLAN_PORT','DYLAN_ADMIN_PORT','DYLAN_PORT_PREFERENCE','DYLAN_ADMIN_PORT_PREFERENCE','BOOTSTRAP_TOKEN','PUBLIC_ORIGIN','ADMIN_ORIGIN','DYLAN_WEB_IMAGE','DYLAN_BOOKING_IMAGE','DYLAN_REVISION','GOOGLE_CLIENT_ID','GOOGLE_OAUTH_MODE','GOOGLE_CLIENT_SECRET')) {
  $script:InitialEnvironment[$key] = [Environment]::GetEnvironmentVariable($key)
}

function Invoke-Native([string]$Program, [string[]]$Arguments) {
  & $Program @Arguments
  if ($LASTEXITCODE -ne 0) { throw "$Program failed with exit code $LASTEXITCODE." }
}
function Read-Settings([string]$Path) {
  $values = @{}
  if (Test-Path -LiteralPath $Path) {
    foreach ($line in Get-Content -LiteralPath $Path) {
      if ($line -match '^([A-Z][A-Z0-9_]*)=(.*)$') { $values[$Matches[1]] = $Matches[2].Trim() }
    }
  }
  return $values
}
function Get-Setting([string]$Name, [string]$Default) {
  $value = [Environment]::GetEnvironmentVariable($Name)
  if ($value) { return $value }
  if ($script:Overrides.ContainsKey($Name)) { return $script:Overrides[$Name] }
  if ($script:Saved.ContainsKey($Name)) { return $script:Saved[$Name] }
  return $Default
}
function New-Secret {
  $bytes = New-Object byte[] 32
  $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
  try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
  return [BitConverter]::ToString($bytes).Replace('-','').ToLowerInvariant()
}
function Initialize-Port([string]$Name, [string]$Default) {
  $preferenceName = "${Name}_PREFERENCE"
  $requested = [Environment]::GetEnvironmentVariable($Name)
  if (-not $requested -and $script:Overrides.ContainsKey($Name)) { $requested = $script:Overrides[$Name] }
  $chosen = $Default
  if ($script:Saved.ContainsKey($Name)) { $chosen = $script:Saved[$Name] }
  $previous = $script:Saved[$preferenceName]
  if ($requested -and $requested -ne $previous) { $chosen = $requested; $previous = $requested }
  [Environment]::SetEnvironmentVariable($Name,$chosen)
  [Environment]::SetEnvironmentVariable($preferenceName,$previous)
}
function Save-State {
  $lines = @(
    "DYLAN_INSTALL_ID=$env:DYLAN_INSTALL_ID",
    "DYLAN_PROJECT=$env:DYLAN_PROJECT",
    "DYLAN_PORT=$env:DYLAN_PORT",
    "DYLAN_ADMIN_PORT=$env:DYLAN_ADMIN_PORT",
    "DYLAN_PORT_PREFERENCE=$env:DYLAN_PORT_PREFERENCE",
    "DYLAN_ADMIN_PORT_PREFERENCE=$env:DYLAN_ADMIN_PORT_PREFERENCE",
    "BOOTSTRAP_TOKEN=$env:BOOTSTRAP_TOKEN"
  )
  $temporary = "$script:StatePath.new"
  [IO.File]::WriteAllText($temporary, ($lines -join [char]10) + [char]10, (New-Object Text.UTF8Encoding($false)))
  if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { Invoke-Native chmod @('600',$temporary) }
  Move-Item -LiteralPath $temporary -Destination $script:StatePath -Force
}
function Initialize-Local {
  $local = Join-Path $script:SiteRoot '.local'
  New-Item -ItemType Directory -Path $local -Force | Out-Null
  $guard = Join-Path $local 'launcher.guard'
  try { New-Item -ItemType Directory -Path $guard -ErrorAction Stop | Out-Null }
  catch { throw 'Another launcher is using this clone. Wait for it to finish. If a launcher crashed, remove .local/launcher.guard after confirming it is no longer running.' }
  $script:StateLock = $guard
  [IO.File]::WriteAllText((Join-Path $guard 'pid'), "$PID")
  $script:Saved = Read-Settings $script:StatePath
  if ((Test-Path -LiteralPath $script:StatePath) -and $script:Saved['BOOTSTRAP_TOKEN'] -notmatch '^[a-f0-9]{64}$') {
    throw 'The private setup file is incomplete. Restore .local/runtime.env from the matching backup; do not reset an existing database.'
  }
  $script:Overrides = Read-Settings (Join-Path $script:SiteRoot '.env')
  $env:DYLAN_INSTALL_ID = Get-Setting 'DYLAN_INSTALL_ID' ([guid]::NewGuid().ToString('N').Substring(0,12))
  $env:DYLAN_PROJECT = Get-Setting 'DYLAN_PROJECT' "dylan-$env:DYLAN_INSTALL_ID"
  Initialize-Port 'DYLAN_PORT' '4177'
  Initialize-Port 'DYLAN_ADMIN_PORT' '4178'
  if ($script:Saved.ContainsKey('BOOTSTRAP_TOKEN')) { $env:BOOTSTRAP_TOKEN = $script:Saved['BOOTSTRAP_TOKEN'] }
  else { $env:BOOTSTRAP_TOKEN = New-Secret }
  if ($env:DYLAN_PROJECT -notmatch '^[a-z0-9][a-z0-9_-]{0,49}$') { throw 'DYLAN_PROJECT must be 1-50 lowercase letters, numbers, underscores or hyphens.' }
  foreach ($value in @($env:DYLAN_PORT,$env:DYLAN_ADMIN_PORT)) {
    if ($value -notmatch '^\d+$' -or [int]$value -lt 1024 -or [int]$value -gt 65535) { throw 'Local ports must be numbers from 1024 to 65535.' }
  }
  foreach ($key in @('GOOGLE_CLIENT_ID','GOOGLE_OAUTH_MODE','GOOGLE_CLIENT_SECRET')) {
    [Environment]::SetEnvironmentVariable($key, (Get-Setting $key ''))
  }
  $env:DYLAN_WEB_IMAGE = "$($env:DYLAN_PROJECT):local"
  $env:DYLAN_BOOKING_IMAGE = "$($env:DYLAN_PROJECT)-booking:local"
  Set-Origins
  Save-State
}
function Set-Origins {
  $env:PUBLIC_ORIGIN = "http://127.0.0.1:$env:DYLAN_PORT"
  $env:ADMIN_ORIGIN = "http://127.0.0.1:$env:DYLAN_ADMIN_PORT"
}
function Get-Compose([switch]$DevMode) {
  $arguments = @('compose','--project-directory',$script:SiteRoot,'--project-name',$env:DYLAN_PROJECT,'--file',(Join-Path $script:SiteRoot 'compose.local.yaml'))
  if ($DevMode) { $arguments += @('--file',(Join-Path $script:SiteRoot 'compose.dev.yaml')) }
  return $arguments
}
function Assert-Docker {
  if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'Install Docker Desktop with Linux containers, then try again.' }
  Invoke-Native docker @('compose','version')
  $engine = & docker info --format '{{.OSType}}' 2>$null
  if ($LASTEXITCODE -ne 0 -or $engine -ne 'linux') { throw 'Start Docker Desktop with Linux containers and wait until it is ready.' }
}
function Set-Revision {
  $env:DYLAN_REVISION = 'local'
  if ((Get-Command git -ErrorAction SilentlyContinue) -and (Test-Path -LiteralPath (Join-Path $script:SiteRoot '.git'))) {
    $revision = & git -C $script:SiteRoot rev-parse HEAD 2>$null
    if ($LASTEXITCODE -eq 0 -and $revision) {
      $env:DYLAN_REVISION = $revision.Trim()
      $dirty = & git -C $script:SiteRoot status --porcelain --untracked-files=all
      if ($LASTEXITCODE -ne 0) { throw 'Could not inspect Git changes.' }
      if ($dirty) { $env:DYLAN_REVISION += '-dirty' }
    }
  }
}
function Build-Site {
  if (-not (Test-Path -LiteralPath (Join-Path $script:SiteRoot 'dist/index.html'))) { throw 'The public page dist/index.html is missing.' }
  Set-Revision
  Invoke-Native docker ((Get-Compose) + @('build'))
  Invoke-Native docker @('run','--rm',$env:DYLAN_WEB_IMAGE,'caddy','validate','--config','/etc/caddy/Caddyfile','--adapter','caddyfile')
}
function Test-Static {
  $name = "dylan-static-check-$([guid]::NewGuid().ToString('N').Substring(0,12))"
  try {
    Invoke-Native docker @('run','--detach','--name',$name,$env:DYLAN_WEB_IMAGE)
    Invoke-Native docker @('run','--rm','--network',"container:$name",'--env','DYLAN_REVISION',
      '--mount',"type=bind,source=$script:SiteRoot/dist,target=/expected,readonly",
      '--mount',"type=bind,source=$script:SiteRoot/scripts,target=/checks,readonly",
      '--entrypoint','sh',$env:DYLAN_WEB_IMAGE,'/checks/check-static.sh')
  } finally { $null = Invoke-PrivateProcess docker @('rm','--force',$name) }
}
function Test-Port([int]$Port) {
  $bindings = & docker ps --filter "label=com.docker.compose.project=$env:DYLAN_PROJECT" --format '{{.Ports}}'
  if (($bindings -join ' ') -match "127\.0\.0\.1:$Port->") { return $true }
  $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,$Port)
  $listener.Server.ExclusiveAddressUse = $true
  try { $listener.Start(); return $true }
  catch { return $false }
  finally { $listener.Stop() }
}
function Next-Port([int]$Port) {
  if ($Port -ge 65535) { return 1024 }
  return $Port + 1
}
function Select-Ports {
  $public = [int]$env:DYLAN_PORT; $admin = [int]$env:DYLAN_ADMIN_PORT; $attempts = 0
  while (-not (Test-Port $public)) {
    $public = Next-Port $public; $attempts++
    if ($attempts -gt 200) { throw 'Could not find an available public port.' }
  }
  while ($admin -eq $public -or -not (Test-Port $admin)) {
    $admin = Next-Port $admin; $attempts++
    if ($attempts -gt 400) { throw 'Could not find an available admin port.' }
  }
  $env:DYLAN_PORT = "$public"; $env:DYLAN_ADMIN_PORT = "$admin"
  Set-Origins
}
function Start-Stack([switch]$DevMode, [switch]$Temporary) {
  for ($attempt = 0; $attempt -lt 12; $attempt++) {
    Select-Ports
    if (-not $Temporary) { Save-State }
    $arguments = (Get-Compose -DevMode:$DevMode) + @('up','--detach','--no-build','--wait','--wait-timeout','100')
    $previousPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $output = (& docker @arguments 2>&1 | ForEach-Object { $_.ToString() } | Out-String)
    $result = $LASTEXITCODE
    $ErrorActionPreference = $previousPreference
    if ($result -eq 0) { Write-Host $output.TrimEnd(); return }
    if ($output -notmatch '(?i)port is already allocated|address already in use|ports are not available|bind.*forbidden|failed to bind') {
      throw "The local application could not become ready. $output"
    }
    $null = Invoke-PrivateProcess docker ((Get-Compose) + @('down'))
    $env:DYLAN_PORT = "$(Next-Port ([int]$env:DYLAN_PORT))"
    $env:DYLAN_ADMIN_PORT = "$(Next-Port ([int]$env:DYLAN_ADMIN_PORT))"
  }
  throw 'Ports kept changing while the application started. Try again.'
}
function Invoke-PrivateProcess([string]$Program, [string[]]$Arguments, [string]$InputText = '') {
  # Redirected streams keep credentials and Git diagnostics out of the console in PS 5.1 and 7.
  $info = New-Object Diagnostics.ProcessStartInfo
  $application = Get-Command $Program -CommandType Application -ErrorAction Stop | Select-Object -First 1
  $info.FileName = $application.Source
  $quoted = foreach ($argument in $Arguments) {
    '"' + ([regex]::Replace([regex]::Replace($argument, '(\\*)"', '$1$1\"'), '(\\+)$', '$1$1')) + '"'
  }
  $info.Arguments = $quoted -join ' '
  $info.UseShellExecute = $false; $info.CreateNoWindow = $true
  $info.RedirectStandardOutput = $true; $info.RedirectStandardError = $true; $info.RedirectStandardInput = $true
  $info.StandardOutputEncoding = New-Object Text.UTF8Encoding($false)
  foreach ($key in @($info.EnvironmentVariables.Keys)) {
    if ($key -match '^(GIT_TRACE|GCM_TRACE)' -or $key -in @('GIT_CURL_VERBOSE','GCM_DEBUG')) { $info.EnvironmentVariables.Remove($key) }
  }
  $info.EnvironmentVariables['GIT_TERMINAL_PROMPT'] = '0'
  $info.EnvironmentVariables['GCM_INTERACTIVE'] = '0'
  $info.EnvironmentVariables['GCM_TRACE_SECRETS'] = '0'
  $process = New-Object Diagnostics.Process
  $process.StartInfo = $info
  $started = $false
  try {
    $null = $process.Start()
    $started = $true
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    if ($InputText) {
      $bytes = (New-Object Text.UTF8Encoding($false)).GetBytes($InputText)
      $process.StandardInput.BaseStream.Write($bytes,0,$bytes.Length)
      [Array]::Clear($bytes,0,$bytes.Length)
    }
    $process.StandardInput.Close()
    if (-not $process.WaitForExit(120000)) { $process.Kill(); $process.WaitForExit(); throw 'Private configuration retrieval timed out. Check Git access and try start again.' }
    $result = @{ Code = $process.ExitCode; Text = $stdout.GetAwaiter().GetResult() }
    $null = $stderr.GetAwaiter().GetResult()
    return $result
  } finally {
    if ($started -and -not $process.HasExited) {
      try { $process.Kill($true) } catch { $process.Kill() }
      $process.WaitForExit()
    }
    $process.Dispose()
  }
}
function Ensure-GoogleConfig {
  $arguments = (Get-Compose) + @('exec','-T','booking','booking','google-config')
  $operatorArguments = (Get-Compose) + @('exec','-T','booking','booking','operator-config')
  $status = Invoke-PrivateProcess docker ($arguments + @('status'))
  $operatorStatus = Invoke-PrivateProcess docker ($operatorArguments + @('status'))
  if ($status.Code -eq 0 -and $operatorStatus.Code -eq 0) { return }
  if ($status.Code -notin @(0,3) -or $operatorStatus.Code -notin @(0,3)) { throw 'Could not check saved Google configuration or operator access. Run the logs command, resolve the local application error, and retry start.' }
  if (-not (Get-Command git -CommandType Application -ErrorAction SilentlyContinue)) {
    throw 'First Google setup requires Git with access to the private repository Derek-Sykes/xsolutions-booking-private. Install/sign in to Git on this machine and retry start.'
  }
  $local = Get-Item -LiteralPath (Join-Path $script:SiteRoot '.local') -Force
  if ($local.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Private setup requires .local to be a regular directory, not a link.' }
  $temporary = Join-Path $local.FullName ("gc-" + [guid]::NewGuid().ToString('N').Substring(0,12))
  $privateJSON = $null
  try {
    New-Item -ItemType Directory -Path $temporary | Out-Null
    if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
      $acl = New-Object Security.AccessControl.DirectorySecurity
      $acl.SetAccessRuleProtection($true,$false)
      $identity = [Security.Principal.WindowsIdentity]::GetCurrent().User
      $rule = New-Object Security.AccessControl.FileSystemAccessRule($identity,'FullControl','ContainerInherit,ObjectInherit','None','Allow')
      $acl.AddAccessRule($rule)
      Set-Acl -LiteralPath $temporary -AclObject $acl
    } else { Invoke-Native chmod @('700',$temporary) }
    $empty = Join-Path $temporary 'empty'
    New-Item -ItemType Directory -Path $empty | Out-Null
    $repository = Join-Path $temporary 'repo'
    $gitOptions = @('--no-pager','-c','credential.interactive=false','-c','http.sslVerify=true','-c','core.longpaths=true',
      '-c','http.lowSpeedLimit=1','-c','http.lowSpeedTime=30','-c',"core.hooksPath=$empty")
    $privateURL = 'https://github.com/Derek-Sykes/xsolutions-booking-private.git'
    $destination = Invoke-PrivateProcess git ($gitOptions + @('ls-remote','--get-url',$privateURL))
    $allowed = @($privateURL,'git@github.com:Derek-Sykes/xsolutions-booking-private.git','ssh://git@github.com/Derek-Sykes/xsolutions-booking-private.git')
    if ($destination.Code -ne 0 -or $destination.Text.Trim() -cnotin $allowed) {
      throw 'Git rewrites the private setup repository to an unexpected destination. Correct that repository URL rewrite in your Git settings, then retry start.'
    }
    Write-Host 'Preparing Google connection using your existing Git access...'
    $clone = Invoke-PrivateProcess git ($gitOptions + @('clone','--quiet','--depth','1','--single-branch','--branch','dev','--no-tags','--no-checkout',"--template=$empty",
      $privateURL,$repository))
    if ($clone.Code -ne 0) {
      throw 'Could not access private Google setup. Make sure the Git account on this machine has access to Derek-Sykes/xsolutions-booking-private, then retry start. Browser sign-in alone does not sign Git in.'
    }
    if ($status.Code -eq 3) {
    $size = Invoke-PrivateProcess git ($gitOptions + @('-C',$repository,'cat-file','-s','HEAD:google-client.json'))
    if ($size.Code -ne 0 -or $size.Text.Trim() -notmatch '^\d+$' -or [long]$size.Text.Trim() -gt 65536) {
      throw 'The private repository needs a valid google-client.json on dev (maximum 64 KiB). Ask its maintainer to correct the file, then retry start.'
    }
    $blob = Invoke-PrivateProcess git ($gitOptions + @('-C',$repository,'cat-file','blob','HEAD:google-client.json'))
    if ($blob.Code -ne 0) { throw 'Could not read the Google setup file from the private repository. Ask its maintainer to check the file, then retry start.' }
    $privateJSON = $blob.Text; $blob.Text = $null
    $import = Invoke-PrivateProcess docker ($arguments + @('import')) $privateJSON
    if ($import.Code -ne 0) { throw 'The private Google setup could not be imported. Ask its maintainer to check the registered client configuration, then retry start.' }
    Write-Host 'Google connection configuration saved privately for this installation.'
    }
    if ($operatorStatus.Code -eq 3) {
      $size = Invoke-PrivateProcess git ($gitOptions + @('-C',$repository,'cat-file','-s','HEAD:operator-access.json'))
      if ($size.Code -ne 0 -or $size.Text.Trim() -notmatch '^\d+$' -or [long]$size.Text.Trim() -gt 4096) {
        throw 'The private repository needs operator-access.json on dev (maximum 4 KiB). Ask its maintainer to provision the approved operator identity.'
      }
      $blob = Invoke-PrivateProcess git ($gitOptions + @('-C',$repository,'cat-file','blob','HEAD:operator-access.json'))
      if ($blob.Code -ne 0) { throw 'Could not read private operator access configuration.' }
      $privateJSON = $blob.Text; $blob.Text = $null
      $import = Invoke-PrivateProcess docker ($operatorArguments + @('import')) $privateJSON
      if ($import.Code -ne 0) { throw 'Operator access could not be imported. Existing accounts and calendar data were preserved.' }
      Write-Host 'Approved operator access saved privately for this installation.'
    }
  } finally {
    $privateJSON = $null
    if (Test-Path -LiteralPath $temporary) {
      $target = Get-Item -LiteralPath $temporary -Force
      $expectedParent = [IO.Path]::GetFullPath($local.FullName).TrimEnd([IO.Path]::DirectorySeparatorChar,[IO.Path]::AltDirectorySeparatorChar)
      if ($target.Parent.FullName.TrimEnd([IO.Path]::DirectorySeparatorChar,[IO.Path]::AltDirectorySeparatorChar) -ne $expectedParent -or
          $target.Name -notmatch '^gc-[a-f0-9]{12}$' -or ($target.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw 'Refused cleanup outside the private setup directory.'
      }
      Remove-Item -LiteralPath $target.FullName -Recurse -Force
    }
  }
}
function Test-Application {
  Invoke-Native docker @('build','--target','test',(Join-Path $script:SiteRoot 'booking'))
  $keys = @('DYLAN_PROJECT','DYLAN_PORT','DYLAN_ADMIN_PORT','PUBLIC_ORIGIN','ADMIN_ORIGIN','BOOTSTRAP_TOKEN','GOOGLE_CLIENT_ID','GOOGLE_CLIENT_SECRET','GOOGLE_OAUTH_MODE')
  $snapshot = @{}
  foreach ($key in $keys) { $snapshot[$key] = [Environment]::GetEnvironmentVariable($key) }
  $checkProject = "dylan-check-$([guid]::NewGuid().ToString('N').Substring(0,12))"
  try {
    $env:DYLAN_PROJECT = $checkProject
    $env:DYLAN_PORT = '24177'; $env:DYLAN_ADMIN_PORT = '24178'
    $env:BOOTSTRAP_TOKEN = New-Secret
    $env:GOOGLE_CLIENT_ID = ''; $env:GOOGLE_CLIENT_SECRET = ''
    $env:GOOGLE_OAUTH_MODE = 'desktop'
    Start-Stack -Temporary
    Invoke-Native docker @('run','--rm','--network',"$($checkProject)_default",
      '--env','PUBLIC_ORIGIN','--env','ADMIN_ORIGIN','--env','BOOTSTRAP_TOKEN',
      '--mount',"type=bind,source=$script:SiteRoot/scripts,target=/checks,readonly",
      'python:3.14-alpine','python','/checks/check-application.py')
  } finally {
    $cleanup = Invoke-PrivateProcess docker ((Get-Compose) + @('down','--volumes'))
    foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key,$snapshot[$key]) }
    if ($cleanup.Code -ne 0) { throw "Could not remove isolated check resources for $checkProject." }
  }
}
function Show-Site {
  Write-Host "Public website: $env:PUBLIC_ORIGIN/"
  Write-Host "Admin workspace: $env:ADMIN_ORIGIN/"
  if (-not $NoOpen) {
    $url = "$env:ADMIN_ORIGIN/"
    $operatorStatus = Invoke-PrivateProcess docker ((Get-Compose) + @('exec','-T','booking','booking','operator-config','status'))
    if ($operatorStatus.Code -eq 3) { $url += "#setup=$env:BOOTSTRAP_TOKEN" }
    elseif ($operatorStatus.Code -ne 0) { throw 'Could not check operator access before opening the portal.' }
    try {
      if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) { Start-Process $url }
      elseif (Get-Command xdg-open -ErrorAction SilentlyContinue) { & xdg-open $url 2>$null | Out-Null }
      elseif (Get-Command open -ErrorAction SilentlyContinue) { & open $url 2>$null | Out-Null }
      else { Write-Host 'Browser opening is unavailable here. Use the open command on a desktop.' }
    } catch { Write-Warning 'Could not open the browser. Run the open command again on a desktop.' }
  }
}
function Update-Source {
  if (-not (Get-Command git -ErrorAction SilentlyContinue)) { throw 'Git is required for safe updates.' }
  $branch = & git -C $script:SiteRoot branch --show-current 2>$null
  if ($LASTEXITCODE -ne 0 -or $branch -notin @('dev','main')) { throw 'Update follows the current dev or main branch. Switch to dev before updating.' }
  $dirty = & git -C $script:SiteRoot status --porcelain --untracked-files=all
  if ($LASTEXITCODE -ne 0) { throw 'Could not inspect Git changes.' }
  if ($dirty) { throw 'Commit or stash local changes before update. No files were overwritten.' }
  Invoke-Native git @('-C',$script:SiteRoot,'fetch','origin',$branch)
  & git -C $script:SiteRoot merge-base --is-ancestor HEAD "origin/$branch"
  if ($LASTEXITCODE -ne 0) { throw 'Your branch is ahead of or diverged from origin. Update will not reset your work.' }
  Invoke-Native git @('-C',$script:SiteRoot,'merge','--ff-only',"origin/$branch")
}
Push-Location $script:SiteRoot
try {
  if ($Command -eq 'help') {
    Write-Host 'Usage: .\website.ps1 start|dev|build|check|update|stop|status|logs|open [-NoOpen]'
    Write-Host 'Docker runs the application. First Google setup uses your existing private-repository Git access. Ports and booking data persist.'
  } else {
    Initialize-Local
    if ($Command -eq 'open') { Show-Site }
    else {
      Assert-Docker
      switch ($Command) {
        { $_ -in @('build','start','dev','check','update') } {
          if ($Command -eq 'update') { Update-Source }
          Build-Site
          Test-Static
          if ($Command -eq 'check') { Test-Application }
          if ($Command -in @('start','dev','update')) {
            Start-Stack -DevMode:($Command -eq 'dev')
            Ensure-GoogleConfig
            Show-Site
          }
        }
        'stop' { Invoke-Native docker ((Get-Compose) + @('down')); Write-Host 'Stopped this install. Booking data is preserved.' }
        'status' { Invoke-Native docker ((Get-Compose) + @('ps')); Write-Host "Public website: $env:PUBLIC_ORIGIN/"; Write-Host "Admin workspace: $env:ADMIN_ORIGIN/" }
        'logs' { Invoke-Native docker ((Get-Compose) + @('logs','--tail','80')) }
      }
    }
  }
} catch {
  Write-Host "Dylan's Lawn Care: $($_.Exception.Message)" -ForegroundColor Red
  exit 1
} finally {
  if ($script:StateLock) {
    Remove-Item -LiteralPath (Join-Path $script:StateLock 'pid') -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $script:StateLock -ErrorAction SilentlyContinue
  }
  foreach ($key in $script:InitialEnvironment.Keys) { [Environment]::SetEnvironmentVariable($key,$script:InitialEnvironment[$key]) }
  Pop-Location
}
