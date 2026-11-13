@echo off
setlocal

"%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoLogo -NoProfile -NonInteractive -ExecutionPolicy RemoteSigned -File "%~dp0cli.ps1" -Action Stop
if errorlevel 1 (
    echo.
    echo AI Proxy CLI shutdown reported an error. See the message above.
    pause
    exit /b 1
)

exit /b 0
