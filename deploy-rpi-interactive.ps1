# deploy-rpi-interactive.ps1 — 部署 ai-proxy 到树莓派（交互式）
# 用法: .\deploy-rpi-interactive.ps1
# 需要 Posh-SSH 模块（首次运行自动安装）
#
# 文件布局（全部与本脚本同目录，均可从 GitHub Release 页下载）:
#   deploy-rpi-interactive.ps1     本脚本
#   ai-proxy-linux-arm64           代理二进制（Release 资产，必需）
#   config\providers.yaml          部署配置（必需）；providers.example.yaml 为模板，
#                                  填好 API Key 后改名为 providers.yaml
#
# 重启前会等待远端在途转发请求归零（GET /api/inflight，默认最多 120s，
# -InflightWaitSec 0 跳过），避免硬切正在转发的长流。

param(
    # 重启前等待远端在途转发请求归零（GET /api/inflight）的最长秒数。
    # 0 = 不等待；默认 120s。远端版本无该端点时提示后直接重启。
    [int]$InflightWaitSec = 120
)

$ErrorActionPreference = 'Stop'

# ── 配置 ──
# 脚本相对定位：二进制、配置均以本脚本所在目录为基准，无硬编码盘符
$LOCAL_ROOT = $PSScriptRoot

# ── 交互式输入用户名 / IP / 密码 ──
$PI_USER = (Read-Host "请输入树莓派用户名").Trim()
$PI_HOST = (Read-Host "请输入树莓派 IP 地址").Trim()
$cred = Get-Credential -UserName $PI_USER -Message "输入树莓派密码 ($PI_USER@$PI_HOST)"
# 远程部署目录跟随输入的用户名，避免硬编码特定 home 路径
$PI_BASE = "/home/$PI_USER/Project/ai-proxy"

# ── 确保 Posh-SSH ──
if (-not (Get-Module -ListAvailable Posh-SSH)) {
    Install-Module Posh-SSH -Force -Scope CurrentUser -AcceptLicense
}
Import-Module Posh-SSH

# ── 在途请求闸门 ──
# 重启 = SIGTERM，代理进程收到后只排空 10 秒。先问远端（curl /api/inflight）
# 还有多少在途转发请求，等到归零再重启，长流就不会被硬切。
function Wait-RemoteInflightQuiet {
    param([PSObject]$SshSession, [int]$TimeoutSec = 120, [int]$Port = 8080)
    $url = "http://127.0.0.1:$Port/api/inflight"
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSec)
    $reported = $false
    while ([DateTime]::UtcNow -lt $deadline) {
        $result = Invoke-SSHCommand -SessionId $SshSession.SessionId -Command "curl -sf -m 3 $url"
        $busy = $null
        if ($result.ExitStatus -eq 0 -and $result.Output) {
            try { $busy = [int](($result.Output -join '') | ConvertFrom-Json).forwarding } catch { $busy = $null }
        }
        if ($null -eq $busy) {
            # 远端版本没有 /api/inflight（或已不可达）：无可依据的在途信息。
            if ($reported) { throw "远端在途查询中途失败: $url" }
            Write-Host '  [!] 远端版本不支持在途查询（无 /api/inflight），不等待直接重启'
            return
        }
        if ($busy -le 0) { Write-Host '  [OK] 远端无在途转发请求'; return }
        if (-not $reported) {
            Write-Host "  [!] 远端仍有 $busy 个在途请求，最多等待 ${TimeoutSec}s（-InflightWaitSec 0 跳过）"
            $reported = $true
        } else {
            Write-Host -NoNewline '.'
        }
        Start-Sleep -Seconds 3
    }
    throw "等待超时: 远端仍有在途转发请求。等其结束，或加 -InflightWaitSec 0 强制重启。"
}

# ── 本地文件 ──
$binLocal = Join-Path $LOCAL_ROOT 'ai-proxy-linux-arm64'
if (-not (Test-Path $binLocal)) {
    Write-Error "找不到二进制: $binLocal`n请从 GitHub Release 页下载 ai-proxy-linux-arm64，放到本脚本同一目录后重试。"
    exit 1
}

$cfgLocal = Join-Path $LOCAL_ROOT 'config\providers.yaml'
if (-not (Test-Path $cfgLocal)) {
    Write-Error "找不到配置: $cfgLocal`n请将 config\providers.example.yaml 填好 API Key 后改名为 providers.yaml。"
    exit 1
}

Write-Host '== ai-proxy 树莓派部署 =='

# ── 连接 ──
Write-Host '[1/6] 连接树莓派...'
$ssh = New-SSHSession -ComputerName $PI_HOST -Credential $cred -AcceptKey
$sftp = New-SFTPSession -ComputerName $PI_HOST -Credential $cred -AcceptKey

try {
    # ── 创建远程目录 ──
    Write-Host '[2/6] 创建远程目录...'
    Invoke-SSHCommand -SessionId $ssh.SessionId -Command "mkdir -p $PI_BASE/config" | Out-Null

    # ── 上传二进制并重命名 ──
    Write-Host '[3/6] 上传二进制...'
    Set-SFTPItem -SessionId $sftp.SessionId -Path $binLocal -Destination $PI_BASE -Force
    Invoke-SSHCommand -SessionId $ssh.SessionId -Command "mv -f $PI_BASE/ai-proxy-linux-arm64 $PI_BASE/ai-proxy" | Out-Null

    # ── 上传配置 ──
    Write-Host '[4/6] 上传配置...'
    Set-SFTPItem -SessionId $sftp.SessionId -Path $cfgLocal -Destination "$PI_BASE/config" -Force

    # ── 在途请求闸门：归零后才重启 ──
    Write-Host '[5/6] 重启前等待在途请求归零...'
    if ($InflightWaitSec -le 0) {
        Write-Host '  [!] 跳过在途请求检查（-InflightWaitSec 0）'
    } else {
        Wait-RemoteInflightQuiet -SshSession $ssh -TimeoutSec $InflightWaitSec
    }

    # ── 清理旧状态/日志，重启服务 ──
    Write-Host '[6/6] 清理并重启服务...'
    $cleanCmd = "rm -fv $PI_BASE/config/.cb_state.json $PI_BASE/config/.stats.json $PI_BASE/proxy.log.* 2>&1; echo '--- 清理后 config 内容 ---'; ls -la $PI_BASE/config/ 2>&1"
    $cleanResult = Invoke-SSHCommand -SessionId $ssh.SessionId -Command $cleanCmd
    Write-Host "清理输出:"
    $cleanResult.Output
    $restartCmd = "chmod +x $PI_BASE/ai-proxy && (sudo systemctl restart ai-proxy 2>/dev/null || (pkill -f ai-proxy 2>/dev/null; sleep 1; nohup $PI_BASE/ai-proxy --config $PI_BASE/config/providers.yaml >> $PI_BASE/proxy.log 2>&1 &) || true)"
    Invoke-SSHCommand -SessionId $ssh.SessionId -Command $restartCmd | Out-Null

    # ── 验证 ──
    Start-Sleep -Seconds 3
    Write-Host ''
    Write-Host '== 验证 =='
    $ver = Invoke-SSHCommand -SessionId $ssh.SessionId -Command "$PI_BASE/ai-proxy --version"
    Write-Host "版本: $($ver.Output)"
    $health = Invoke-SSHCommand -SessionId $ssh.SessionId -Command "curl -sf http://localhost:8080/health || echo '服务未就绪'"
    Write-Host "健康检查: $($health.Output)"
    Write-Host ''
    Write-Host '部署完成。'
}
finally {
    Remove-SSHSession -SessionId $ssh.SessionId -ErrorAction SilentlyContinue
    Remove-SFTPSession -SessionId $sftp.SessionId -ErrorAction SilentlyContinue
}
