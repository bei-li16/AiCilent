Option Explicit

Dim fs, shell, scriptPath, powershell, quote
Set fs = CreateObject("Scripting.FileSystemObject")
Set shell = CreateObject("WScript.Shell")
quote = Chr(34)
scriptPath = fs.BuildPath(fs.GetParentFolderName(WScript.ScriptFullName), "start-cli-tray.ps1")
powershell = shell.ExpandEnvironmentStrings("%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe")

shell.Run quote & powershell & quote & " -NoLogo -NoProfile -STA -ExecutionPolicy RemoteSigned -WindowStyle Hidden -File " & quote & scriptPath & quote, 0, False
