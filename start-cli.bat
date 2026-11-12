@echo off
setlocal

"%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoLogo -NoProfile -NonInteractive -ExecutionPolicy RemoteSigned -File "%~dp0start-cli-tray.ps1" -BackgroundOnly
if errorlevel 1 (
    echo.
    echo AI Proxy CLI could not start. See the error above.
    pause
    exit /b 1
)

exit /b 0
