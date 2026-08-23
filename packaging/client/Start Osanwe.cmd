@echo off
setlocal
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0start-osanwe.ps1"
if errorlevel 1 (
  echo.
  echo Osanwe stopped with an error. The message above explains what to fix.
  pause
)
endlocal
