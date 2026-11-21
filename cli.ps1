[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateSet('Start', 'Stop')]
    [string]$Action,

    # Stop 前等待在途转发请求归零的最长秒数（GET /api/inflight）。
    # 0 = 不等待直接停止；默认 120s。运行中版本无该端点时提示后直接停止。
    [int]$InflightWaitSec = 120
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

function Get-ProxyPort {
    # 从配置文件读 listen_addr（如 ":8080" 或 "127.0.0.1:8080"），取端口。
    try {
        $line = (Select-String -LiteralPath $config -Pattern '^\s*listen_addr:\s*(\S+)' |
            Select-Object -First 1).Matches[0].Groups[1].Value
        $port = ($line -split ':')[-1]
        if ($port -match '^\d+$') { return [int]$port }
    } catch { }
    return 8080
}

# Stop 会打断在途转发（Ctrl+C 后进程只排空 10 秒）。先问运行中的进程
# （GET /api/inflight）还有多少在途请求，等到归零再停，长流就不会被硬切。
function Wait-InflightQuiet {
    param([int]$TimeoutSec)
    $url = "http://127.0.0.1:$(Get-ProxyPort)/api/inflight"
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSec)
    $reported = $false
    while ([DateTime]::UtcNow -lt $deadline) {
        $busy = $null
        $out = & curl.exe -sf -m 3 $url 2>$null
        if ($LASTEXITCODE -eq 0 -and $out) {
            try { $busy = [int]($out | ConvertFrom-Json).forwarding } catch { $busy = $null }
        }
        if ($null -eq $busy) {
            # 老版本二进制没有 /api/inflight（或服务已不可达）：无可依据的在途信息。
            if ($reported) { throw "在途查询中途失败：$url" }
            Write-Output '运行中的版本不支持在途查询（无 /api/inflight），直接停止。'
            return
        }
        if ($busy -le 0) { return }
        if (-not $reported) {
            Write-Output "仍有 $busy 个在途请求，最多等待 ${TimeoutSec}s（可用 -InflightWaitSec 0 跳过）..."
            $reported = $true
        } else {
            Write-Host -NoNewline '.'
        }
        Start-Sleep -Seconds 2
    }
    throw "等待超时：仍有在途转发请求。等它们结束，或确认可中断后用 -InflightWaitSec 0 强制停止。"
}

function Stop-ManagedProxy {
    $process = Get-ManagedProxy
    if (-not $process) {
        Write-Output 'AI Proxy CLI is not running.'
        return
    }

    # 先等在途转发请求归零，再发停止信号（信号后进程只排空 10 秒）。
    if ($InflightWaitSec -gt 0) {
        Wait-InflightQuiet -TimeoutSec $InflightWaitSec
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
