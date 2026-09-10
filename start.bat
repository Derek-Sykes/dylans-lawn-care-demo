@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0website.ps1" start %*
set "SITE_EXIT=%ERRORLEVEL%"
if not "%SITE_EXIT%"=="0" (
  echo.
  echo The command could not finish. Read the message above.
  pause
)
exit /b %SITE_EXIT%
