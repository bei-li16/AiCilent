[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateSet('Start', 'Stop')]
    [string]$Action
)

$ErrorActionPreference = 'Stop'

function Get-ManagedProxy {
    if (-not (Test-Path -LiteralPath $statePath -PathType Leaf)) { return }
    try {
        $saved = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
        $process = Get-Process -Id $saved.pid -ErrorAction Stop
        if ($process.Path -eq $binary -and
            [string]$process.StartTime.ToUniversalTime().Ticks -eq $saved.startedUtcTicks) {
            return $process
        }
    } catch {
        # The saved process has exited or the state file cannot identify it.
    }
}

function Start-ManagedProxy {
    $process = Get-ManagedProxy
    if ($process) { return $process }

    $name = [IO.Path]::GetFileNameWithoutExtension($binary)
    $existing = Get-Process -Name $name -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -eq $binary }
    if ($existing) {
        throw '这个 ai-proxy.exe 已由其他方式启动。请先关闭原来的实例，再运行 start-cli.bat。'
    }

    $process = Start-Process -FilePath $binary -WorkingDirectory (Split-Path -Parent $binary) `
        -ArgumentList ('--config "{0}"' -f $config) -WindowStyle Hidden -PassThru
    try {
        @{
            pid = $process.Id
            startedUtcTicks = [string]$process.StartTime.ToUniversalTime().Ticks
        } | ConvertTo-Json -Compress | Set-Content -LiteralPath $statePath -Encoding UTF8
    } catch {
        throw "CLI 已尝试启动，但无法记录实例。请检查 $statePath。$($_.Exception.Message)"
    }

    if ($process.WaitForExit(750)) {
        throw "CLI 启动后退出（退出码 $($process.ExitCode)）。请检查监听端口和配置，或在终端运行 ai-proxy.exe 查看错误。"
    }
    return $process
}

function Stop-ManagedProxy {
    $process = Get-ManagedProxy
    if (-not $process) {
        Write-Output 'AI Proxy CLI is not running.'
        return
    }

    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;

public static class AiProxyConsole {
    const uint CtrlCEvent = 0;
    const uint AttachParentProcess = uint.MaxValue;

    [DllImport("kernel32.dll", SetLastError = true)]
    static extern bool AttachConsole(uint processId);
    [DllImport("kernel32.dll")]
    static extern bool FreeConsole();
    [DllImport("kernel32.dll")]
    static extern uint GetCurrentProcessId();
    [DllImport("kernel32.dll", SetLastError = true)]
    static extern uint GetConsoleProcessList([Out] uint[] processIds, uint count);
    [DllImport("kernel32.dll", SetLastError = true)]
    static extern bool SetConsoleCtrlHandler(IntPtr handler, bool add);
    [DllImport("kernel32.dll", SetLastError = true)]
    static extern bool GenerateConsoleCtrlEvent(uint controlEvent, uint processGroup);
    [DllImport("kernel32.dll")]
    static extern IntPtr GetStdHandle(int handle);
    [DllImport("kernel32.dll")]
    static extern bool SetStdHandle(int handle, IntPtr value);
    [DllImport("kernel32.dll")]
    static extern uint GetFileType(IntPtr handle);

    public static void Interrupt(uint processId) {
        int[] standardHandles = { -10, -11, -12 };
        IntPtr[] redirected = Array.ConvertAll(standardHandles, handle => {
            IntPtr value = GetStdHandle(handle);
            uint type = GetFileType(value);
            return type == 1 || type == 3 ? value : IntPtr.Zero;
        });
        FreeConsole();
        try {
            if (!AttachConsole(processId))
                throw new Win32Exception(Marshal.GetLastWin32Error());

            uint[] ids = new uint[2];
            if (GetConsoleProcessList(ids, (uint)ids.Length) != 2 ||
                Array.IndexOf(ids, processId) < 0 || Array.IndexOf(ids, GetCurrentProcessId()) < 0)
                throw new InvalidOperationException("The CLI does not have a dedicated console.");

            if (!SetConsoleCtrlHandler(IntPtr.Zero, true) || !GenerateConsoleCtrlEvent(CtrlCEvent, 0))
                throw new Win32Exception(Marshal.GetLastWin32Error());
        } finally {
            FreeConsole();
            AttachConsole(AttachParentProcess);
            for (int i = 0; i < standardHandles.Length; i++)
                if (redirected[i] != IntPtr.Zero)
                    SetStdHandle(standardHandles[i], redirected[i]);
        }
    }
}
'@
    # Keep the process handle open while signaling its private console.
    [void]$process.Handle
    if (-not $process.HasExited) {
        [AiProxyConsole]::Interrupt([uint32]$process.Id)
        if (-not $process.WaitForExit(15000)) {
            throw 'CLI has not exited yet. Close the dashboard log stream and try again.'
        }
    }
    Remove-Item -LiteralPath $statePath
    if ($process.ExitCode -ne 0) {
        throw "CLI stopped with exit code $($process.ExitCode). Check proxy.log."
    }
    Write-Output 'AI Proxy CLI stopped.'
}

$binary = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot 'ai-proxy.exe')).ProviderPath
$config = Join-Path $PSScriptRoot 'config\providers.yaml'
if (-not (Test-Path -LiteralPath $binary -PathType Leaf) -or
    ($Action -eq 'Start' -and -not (Test-Path -LiteralPath $config -PathType Leaf))) {
    throw '请将 ai-proxy.exe 和 config/providers.yaml 放在脚本旁边。'
}

$hasher = [Security.Cryptography.SHA256]::Create()
try {
    $identity = "$binary`n$config".ToUpperInvariant()
    $key = [BitConverter]::ToString($hasher.ComputeHash([Text.Encoding]::UTF8.GetBytes($identity))).Replace('-', '')
} finally {
    $hasher.Dispose()
}

$mutex = [Threading.Mutex]::new($false, "Local\AiProxy.Cli.$key")
$ownsMutex = $false
try {
    try { $ownsMutex = $mutex.WaitOne(2000) } catch [Threading.AbandonedMutexException] { $ownsMutex = $true }
    if (-not $ownsMutex) { throw 'Another CLI start or stop operation is in progress.' }

    $stateDirectory = Join-Path (Join-Path $PSScriptRoot '.cli-state') $key
    $statePath = Join-Path $stateDirectory 'process.json'

    # Older BAT versions put the same process identity in .cli-tray.
    $legacy = Join-Path (Join-Path (Join-Path $PSScriptRoot '.cli-tray') $key) 'process.json'
    if (-not (Test-Path -LiteralPath $statePath -PathType Leaf) -and (Test-Path -LiteralPath $legacy -PathType Leaf)) {
        [void][IO.Directory]::CreateDirectory($stateDirectory)
        Move-Item -LiteralPath $legacy -Destination $statePath
    }

    if ($Action -eq 'Stop') {
        Stop-ManagedProxy
    } else {
        [void][IO.Directory]::CreateDirectory($stateDirectory)
        $process = Start-ManagedProxy
        Write-Output "AI Proxy CLI PID $($process.Id)"
    }
} finally {
    if ($ownsMutex) { $mutex.ReleaseMutex() }
    $mutex.Dispose()
}
