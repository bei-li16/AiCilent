[CmdletBinding()]
param(
    [string]$BinaryPath,
    [string]$ConfigPath,
    [switch]$BackgroundOnly
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
        throw '这个 ai-proxy.exe 已由其他方式启动。请先关闭原来的实例，再用托盘脚本启动。'
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

function Get-ProxyUrl {
    $process = Get-ManagedProxy
    if (-not $process) { throw '后台 CLI 未运行。请在托盘菜单选择「启动后台 CLI」。' }
    $listeners = @(Get-NetTCPConnection -OwningProcess $process.Id -State Listen -ErrorAction SilentlyContinue)
    if (-not $listeners.Count) { throw '后台 CLI 尚未开始监听，请稍后再试。' }

    $listener = $listeners | Sort-Object {
        switch ($_.LocalAddress) {
            '127.0.0.1' { 0 }
            '0.0.0.0' { 1 }
            '::1' { 2 }
            '::' { 3 }
            default { 4 }
        }
    } | Select-Object -First 1
    $address = switch ($listener.LocalAddress) {
        '0.0.0.0' { '127.0.0.1' }
        '::' { '::1' }
        default { $listener.LocalAddress }
    }
    return [UriBuilder]::new('http', $address, $listener.LocalPort).Uri
}

function Invoke-TrayAction([scriptblock]$Action) {
    try {
        & $Action
    } catch {
        [void][Windows.Forms.MessageBox]::Show($_.Exception.Message, 'AI Proxy CLI')
    }
}

function Set-Forwarding([bool]$Running) {
    $uri = [Uri]::new((Get-ProxyUrl), '/api/control')
    $request = [Net.HttpWebRequest][Net.WebRequest]::Create($uri)
    $request.Proxy = $null
    $request.Timeout = 3000
    $request.ReadWriteTimeout = 3000
    $request.AllowAutoRedirect = $false
    $request.Method = 'POST'
    $request.ContentType = 'application/json'
    $body = [Text.Encoding]::UTF8.GetBytes((@{ running = $Running } | ConvertTo-Json -Compress))
    $request.ContentLength = $body.Length
    $stream = $request.GetRequestStream()
    try { $stream.Write($body, 0, $body.Length) } finally { $stream.Dispose() }
    $response = $request.GetResponse()
    try {
        if ($response.StatusCode -ne [Net.HttpStatusCode]::OK) {
            throw "代理控制请求返回 HTTP $([int]$response.StatusCode)。"
        }
    } finally {
        $response.Dispose()
    }
    $tray.Text = if ($Running) { 'AI Proxy CLI - 转发运行中' } else { 'AI Proxy CLI - 已暂停转发' }
}

$mutex = $null
$ownsMutex = $false
$tray = $null
$menu = $null
$context = $null
try {
    if (-not $BackgroundOnly) {
        Add-Type -AssemblyName System.Windows.Forms, System.Drawing
    }
    if (-not $BinaryPath) { $BinaryPath = Join-Path $PSScriptRoot 'ai-proxy.exe' }
    if (-not $ConfigPath) { $ConfigPath = Join-Path $PSScriptRoot 'config/providers.yaml' }
    $binary = (Resolve-Path -LiteralPath $BinaryPath).ProviderPath
    $config = (Resolve-Path -LiteralPath $ConfigPath).ProviderPath
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf) -or
        -not (Test-Path -LiteralPath $config -PathType Leaf)) {
        throw '请将 ai-proxy.exe 和 config/providers.yaml 放在启动脚本旁边。'
    }

    $hasher = [Security.Cryptography.SHA256]::Create()
    try {
        $identity = "$binary`n$config".ToUpperInvariant()
        $key = [BitConverter]::ToString($hasher.ComputeHash([Text.Encoding]::UTF8.GetBytes($identity))).Replace('-', '')
    } finally {
        $hasher.Dispose()
    }

    $mutex = [Threading.Mutex]::new($false, "Local\AiProxy.CliTray.$key")
    try { $ownsMutex = $mutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $ownsMutex = $true }
    if (-not $ownsMutex) { return }

    $stateDirectory = Join-Path (Join-Path $PSScriptRoot '.cli-tray') $key
    [void][IO.Directory]::CreateDirectory($stateDirectory)
    $statePath = Join-Path $stateDirectory 'process.json'
    $process = Start-ManagedProxy
    if ($BackgroundOnly) {
        Write-Output "AI Proxy CLI PID $($process.Id)"
        return
    }

    $context = [Windows.Forms.ApplicationContext]::new()
    $menu = [Windows.Forms.ContextMenuStrip]::new()
    $open = $menu.Items.Add('打开监控面板')
    $open.Add_Click({ Invoke-TrayAction { Start-Process -FilePath (Get-ProxyUrl).AbsoluteUri } })
    $start = $menu.Items.Add('启动后台 CLI')
    $start.Add_Click({ Invoke-TrayAction { $null = Start-ManagedProxy; $tray.Text = 'AI Proxy CLI' } })
    $pause = $menu.Items.Add('暂停转发')
    $pause.Add_Click({ Invoke-TrayAction { Set-Forwarding $false } })
    $resume = $menu.Items.Add('恢复转发')
    $resume.Add_Click({ Invoke-TrayAction { Set-Forwarding $true } })
    [void]$menu.Items.Add([Windows.Forms.ToolStripSeparator]::new())
    $exit = $menu.Items.Add('退出托盘（CLI 继续运行）')
    $exit.Add_Click({ $context.ExitThread() })

    $tray = [Windows.Forms.NotifyIcon]::new()
    $tray.Icon = [Drawing.SystemIcons]::Application
    $tray.Text = 'AI Proxy CLI'
    $tray.ContextMenuStrip = $menu
    $tray.Add_DoubleClick({ Invoke-TrayAction { Start-Process -FilePath (Get-ProxyUrl).AbsoluteUri } })
    $tray.Visible = $true
    [Windows.Forms.Application]::Run($context)
} catch {
    if ($BackgroundOnly -or -not ('System.Windows.Forms.MessageBox' -as [type])) { throw }
    [void][Windows.Forms.MessageBox]::Show($_.Exception.Message, 'AI Proxy CLI')
    exit 1
} finally {
    if ($tray) { $tray.Visible = $false; $tray.Dispose() }
    if ($menu) { $menu.Dispose() }
    if ($context) { $context.Dispose() }
    if ($ownsMutex) { $mutex.ReleaseMutex() }
    if ($mutex) { $mutex.Dispose() }
}
