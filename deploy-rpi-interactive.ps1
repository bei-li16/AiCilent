# deploy-rpi.ps1 — 部署 ai-proxy 到树莓派
# 用法: .\deploy-rpi-interactive.ps1
# 需要 Posh-SSH 模块（首次运行自动安装）
#
# 文件布局（全部与本脚本同目录，均可从 GitHub Release 页下载）:
#   deploy-rpi-interactive.ps1     本脚本
#   ai-proxy-linux-arm64           代理二进制（Release 资产，必需）
#   config\providers.yaml          可选；无则沿用树莓派上的现有配置
#                                  （providers.example.yaml 为模板，填好 API Key 后改名）

$ErrorActionPreference = 'Stop'

# ── 配置 ──
# 脚本相对定位：二进制、配置均以本脚本所在目录为基准，无硬编码盘符
$LOCAL_ROOT = $PSScriptRoot

# ── 交互式输入用户名 / IP / 密码 ──
$PI_USER = Read-Host "请输入树莓派用户名"
$PI_HOST = Read-Host "请输入树莓派 IP 地址"
$cred = Get-Credential -UserName $PI_USER -Message "输入树莓派密码 ($PI_USER@$PI_HOST)"
# 远程部署目录跟随输入的用户名，避免硬编码特定 home 路径
$PI_BASE = "/home/$PI_USER/Project/ai-proxy"

# ── 确保 Posh-SSH ──
if (-not (Get-Module -ListAvailable Posh-SSH)) {
    Install-Module Posh-SSH -Force -Scope CurrentUser -AcceptLicense
}
Import-Module Posh-SSH

# ── 本地文件 ──
$binLocal = Join-Path $LOCAL_ROOT 'ai-proxy-linux-arm64'
if (-not (Test-Path $binLocal)) {
    Write-Error "找不到二进制: $binLocal`n请从 GitHub Release 页下载 ai-proxy-linux-arm64，放到本脚本同一目录后重试。"
    exit 1
}

# 配置可选：优先 config\providers.yaml，其次脚本同目录的 providers.yaml；
# 都没有则跳过上传，沿用树莓派上的现有配置
$cfgLocal = Join-Path $LOCAL_ROOT 'config\providers.yaml'
if (-not (Test-Path $cfgLocal)) {
    $cfgLocal = Join-Path $LOCAL_ROOT 'providers.yaml'
}
$uploadCfg = Test-Path $cfgLocal
if (-not $uploadCfg) {
    Write-Warning "未找到 config\providers.yaml，将跳过配置上传，沿用树莓派上的现有配置"
}

Write-Host '== ai-proxy 树莓派部署 =='

# ── 连接 ──
Write-Host '[1/5] 连接树莓派...'
$ssh = New-SSHSession -ComputerName $PI_HOST -Credential $cred -AcceptKey
$sftp = New-SFTPSession -ComputerName $PI_HOST -Credential $cred -AcceptKey

try {
    # ── 创建远程目录 ──
    Write-Host '[2/5] 创建远程目录...'
    Invoke-SSHCommand -SessionId $ssh.SessionId -Command "mkdir -p $PI_BASE/config" | Out-Null

    # ── 上传二进制并重命名 ──
    Write-Host '[3/5] 上传二进制...'
    Set-SFTPItem -SessionId $sftp.SessionId -Path $binLocal -Destination $PI_BASE -Force
    Invoke-SSHCommand -SessionId $ssh.SessionId -Command "mv -f $PI_BASE/ai-proxy-linux-arm64 $PI_BASE/ai-proxy" | Out-Null

    # ── 上传配置（可选） ──
    if ($uploadCfg) {
        Write-Host '[4/5] 上传配置...'
        Set-SFTPItem -SessionId $sftp.SessionId -Path $cfgLocal -Destination "$PI_BASE/config" -Force
    } else {
        Write-Host '[4/5] 跳过配置上传（本地未找到 providers.yaml）'
    }

    # ── 清理旧状态/日志，重启服务 ──
    Write-Host '[5/5] 清理并重启服务...'
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
