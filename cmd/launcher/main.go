//go:build gui

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/server"
	"ai-proxy/internal/version"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"gopkg.in/yaml.v3"
)

type proxyManager struct {
	mu         sync.Mutex
	httpServer *http.Server
	listener   net.Listener
	srv        *server.Instance
	cfg        *config.Config
	configPath string
	startTime  time.Time
	running    bool
	runCtx     context.Context
	cancel     context.CancelFunc
}

func (pm *proxyManager) start() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.running {
		return fmt.Errorf("already running")
	}

	cfg, err := config.Load(pm.configPath)
	if err != nil {
		return err
	}
	pm.cfg = cfg

	srv := server.New(cfg, pm.configPath)
	listener, err := net.Listen("tcp", cfg.Global.ListenAddr)
	if err != nil {
		srv.StopWatcher()
		srv.SaveStats()
		if srv.Rot != nil {
			srv.Rot.Close()
		}
		return fmt.Errorf("listen %s: %w", cfg.Global.ListenAddr, err)
	}

	httpServer := &http.Server{
		Addr:              cfg.Global.ListenAddr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	runCtx, cancel := context.WithCancel(context.Background())
	pm.srv = srv
	pm.httpServer = httpServer
	pm.listener = listener
	pm.runCtx = runCtx
	pm.cancel = cancel

	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Server error: %v\n", err)
		}
	}()

	pm.startTime = time.Now()
	pm.running = true
	return nil
}

func (pm *proxyManager) stop() {
	pm.mu.Lock()
	if !pm.running {
		pm.mu.Unlock()
		return
	}
	pm.running = false
	httpServer := pm.httpServer
	srv := pm.srv
	cancel := pm.cancel
	pm.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if httpServer != nil {
		_ = httpServer.Shutdown(ctx)
	}
	if srv != nil {
		srv.StopWatcher()
		srv.SaveStats()
		if srv.Rot != nil {
			_ = srv.Rot.Close()
		}
	}

	pm.mu.Lock()
	pm.httpServer = nil
	pm.listener = nil
	pm.runCtx = nil
	pm.srv = nil
	pm.cancel = nil
	pm.mu.Unlock()
}

func (pm *proxyManager) restart() error {
	pm.stop()
	time.Sleep(500 * time.Millisecond)
	return pm.start()
}

func (pm *proxyManager) uptimeText() string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if !pm.running {
		return "未运行"
	}
	d := time.Since(pm.startTime)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("已运行 %dh %dm", h, m)
}

func (pm *proxyManager) addrText() string {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.cfg == nil {
		return "—"
	}
	return pm.cfg.Global.ListenAddr
}

func (pm *proxyManager) providerCount() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.cfg == nil {
		return 0
	}
	return len(pm.cfg.Providers)
}

func (pm *proxyManager) isRunning() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.running
}

func (pm *proxyManager) configSnapshot() *config.Config {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.cfg == nil {
		return nil
	}
	clone := *pm.cfg
	clone.Providers = append([]config.Provider(nil), pm.cfg.Providers...)
	clone.ModelRoutes = append([]config.ModelRoute(nil), pm.cfg.ModelRoutes...)
	clone.ModelRules = make([]config.ModelRule, len(pm.cfg.ModelRules))
	for i, rule := range pm.cfg.ModelRules {
		clone.ModelRules[i] = rule
		clone.ModelRules[i].Defaults = make(map[string]interface{}, len(rule.Defaults))
		for key, value := range rule.Defaults {
			clone.ModelRules[i].Defaults[key] = value
		}
	}
	return &clone
}

func (pm *proxyManager) dashboardURL() string {
	addr := pm.addrText()
	if addr == "" || addr == "—" {
		return "http://localhost:8080"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		return "http://" + net.JoinHostPort(host, port)
	}
	return "http://" + addr
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}

func openEditor(path string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("notepad", path).Start()
	case "darwin":
		exec.Command("open", "-a", "TextEdit", path).Start()
	default:
		exec.Command("xdg-open", path).Start()
	}
}

func saveConfig(cfg *config.Config, path string) error {
	cfg.SortProviders()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

const defaultConfigRelativePath = "config/providers.yaml"

// resolveConfigPath anchors runtime files to the launcher executable. A GUI
// started from a desktop shortcut must not depend on the process working
// directory.
func resolveConfigPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate launcher executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(filepath.Dir(exe), defaultConfigRelativePath), nil
}

// initializeConfig creates the first-run configuration, adopts a legacy
// config/providers.yaml from the current directory when possible, and writes
// newly introduced default fields back to an existing configuration.
func initializeConfig(path string) (firstRun bool, err error) {
	configDir := filepath.Dir(path)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("create config directory: %w", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		legacyPath, legacyErr := filepath.Abs(defaultConfigRelativePath)
		if legacyErr == nil && filepath.Clean(legacyPath) != filepath.Clean(path) {
			if _, statErr := os.Stat(legacyPath); statErr == nil {
				if err := copyFile(legacyPath, path); err != nil {
					return false, fmt.Errorf("copy existing config: %w", err)
				}
			} else if !os.IsNotExist(statErr) {
				return false, fmt.Errorf("check existing config: %w", statErr)
			}
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, defaultConfigYAML, 0644); err != nil {
				return false, fmt.Errorf("create default config: %w", err)
			}
			firstRun = true
		} else if err != nil {
			return false, fmt.Errorf("check created config: %w", err)
		}
	} else if err != nil {
		return false, fmt.Errorf("check config: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return firstRun, fmt.Errorf("read config: %w", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return firstRun, err
	}

	updated := configNeedsMigration(data)
	if updated {
		// These fields were not part of older templates and are intentionally
		// only filled when the YAML key is absent. Explicit empty values remain
		// user choices.
		if !yamlHasKey(data, "global", "default_format") {
			cfg.Global.DefaultFormat = "openai"
		}
		if !yamlHasKey(data, "global", "log_file") {
			cfg.Global.LogFile = "../proxy.log"
		}
		if !yamlHasKey(data, "global", "max_stream_minutes") {
			cfg.Global.MaxStreamMinutes = 3
		}
	}
	// Older GUI releases resolved proxy.log from the executable working
	// directory. Preserve that location now that relative paths are resolved
	// from config/providers.yaml.
	if filepath.Clean(cfg.Global.LogFile) == "proxy.log" {
		cfg.Global.LogFile = "../proxy.log"
		updated = true
	}
	if updated {
		if err := saveConfig(cfg, path); err != nil {
			return firstRun, fmt.Errorf("update config defaults: %w", err)
		}
	}

	if err := ensureLogFile(cfg, path); err != nil {
		return firstRun, err
	}
	return firstRun, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, copyErr := io.Copy(out, in)
	return copyErr
}

func ensureLogFile(cfg *config.Config, configPath string) error {
	if cfg.Global.LogFile == "" {
		return nil
	}
	logPath := cfg.Global.LogFile
	if !filepath.IsAbs(logPath) {
		logPath = filepath.Join(filepath.Dir(configPath), logPath)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	return f.Close()
}

func configNeedsMigration(data []byte) bool {
	keys := []string{
		"listen_addr", "default_format", "log_file", "log_request_body",
		"cb_threshold", "cb_cooldown", "cb_skip_requests", "control_allow_remote",
		"max_stream_minutes",
	}
	for _, key := range keys {
		if !yamlHasKey(data, "global", key) {
			return true
		}
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false
	}
	providers := yamlMapValue(rootMap(&root), "providers")
	if providers == nil || providers.Kind != yaml.SequenceNode {
		return false
	}
	for _, provider := range providers.Content {
		for _, key := range []string{"timeout", "retry", "auth_type", "rate_limit"} {
			if yamlMapValue(provider, key) == nil {
				return true
			}
		}
	}
	return false
}

func yamlHasKey(data []byte, path ...string) bool {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false
	}
	node := rootMap(&root)
	for _, key := range path {
		node = yamlMapValue(node, key)
		if node == nil {
			return false
		}
	}
	return true
}

func rootMap(root *yaml.Node) *yaml.Node {
	if root == nil || len(root.Content) == 0 {
		return nil
	}
	node := root.Content[0]
	if node.Kind != yaml.MappingNode {
		return nil
	}
	return node
}

func yamlMapValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func main() {
	configPath, resolveErr := resolveConfigPath()
	if resolveErr != nil {
		configPath = defaultConfigRelativePath
	}
	firstRun, initErr := initializeConfig(configPath)
	pm := &proxyManager{configPath: configPath}

	a := app.NewWithID("com.aiproxy.launcher")
	w := a.NewWindow(fmt.Sprintf("AI Proxy Launcher %s", version.Version))
	w.Resize(fyne.NewSize(520, 760))

	statusLabel := widget.NewLabel("● 已停用")
	addrLabel := widget.NewLabel("监听 —")
	uptimeLabel := widget.NewLabel("未运行")
	provLabel := widget.NewLabel("")

	startBtn := widget.NewButton("▶ 启动", func() {
		if err := pm.start(); err != nil {
			dialog.NewError(err, w)
		}
	})

	stopBtn := widget.NewButton("⏹ 停止", func() {
		pm.stop()
	})

	restartBtn := widget.NewButton("↻ 重启", func() {
		if err := pm.restart(); err != nil {
			dialog.NewError(err, w)
		}
	})

	openBtn := widget.NewButton("📊 打开面板", func() {
		openBrowser(pm.dashboardURL())
	})

	editBtn := widget.NewButton("⚙ 编辑配置", func() {
		showConfigEditor(a, w, pm)
	})

	logLabel := widget.NewLabel("最近日志")
	logView := widget.NewTextGrid()
	logView.Scroll = fyne.ScrollBoth
	var autoFollowLogs atomic.Bool
	autoFollowLogs.Store(true)
	followLogsCheck := widget.NewCheck("自动跟随", nil)
	followLogsCheck.SetChecked(true)
	followLogsCheck.OnChanged = func(enabled bool) {
		autoFollowLogs.Store(enabled)
		if enabled {
			logView.ScrollToBottom()
		}
	}
	clearLogBtn := widget.NewButton("清空", func() { logView.SetText("") })

	listenEntry := widget.NewEntry()
	listenEntry.SetPlaceHolder(":8080")
	logLevelSelect := widget.NewSelect([]string{"off", "snippet", "full"}, nil)
	remoteCheck := widget.NewCheck("允许远程控制", nil)
	cbThresholdEntry := widget.NewEntry()
	cbCooldownEntry := widget.NewEntry()
	cbSkipEntry := widget.NewEntry()
	maxStreamEntry := widget.NewEntry()
	defaultRouteSelect := widget.NewSelect(nil, nil)

	loadQuickSettings := func() {
		cfg := pm.configSnapshot()
		if cfg == nil {
			return
		}
		listenEntry.SetText(cfg.Global.ListenAddr)
		level := cfg.Global.LogRequestBody
		if level == "" {
			level = "snippet"
		}
		logLevelSelect.SetSelected(level)
		remoteCheck.SetChecked(cfg.Global.ControlAllowRemote)
		cbThresholdEntry.SetText(strconv.Itoa(cfg.Global.CBThreshold))
		cbCooldownEntry.SetText(strconv.Itoa(cfg.Global.CBCooldown))
		cbSkipEntry.SetText(strconv.Itoa(cfg.Global.CBSkipRequests))
		maxStreamEntry.SetText(strconv.Itoa(cfg.Global.MaxStreamMinutes))
		options := []string{"不设置"}
		selected := "不设置"
		for _, p := range cfg.Providers {
			options = append(options, p.Name)
		}
		for _, route := range cfg.ModelRoutes {
			if route.Alias == "default" {
				selected = route.Target
				break
			}
		}
		defaultRouteSelect.Options = options
		defaultRouteSelect.SetSelected(selected)
	}

	saveQuickBtn := widget.NewButton("保存并重启", func() {
		threshold, err1 := strconv.Atoi(cbThresholdEntry.Text)
		cooldown, err2 := strconv.Atoi(cbCooldownEntry.Text)
		skip, err3 := strconv.Atoi(cbSkipEntry.Text)
		maxStream, err4 := strconv.Atoi(maxStreamEntry.Text)
		if listenEntry.Text == "" || err1 != nil || err2 != nil || err3 != nil || err4 != nil || threshold <= 0 || cooldown <= 0 || skip < 0 || maxStream < 0 {
			dialog.NewInformation("设置无效", "监听地址不能为空，参数必须是有效数字", w)
			return
		}
		cfg := pm.configSnapshot()
		if cfg == nil {
			return
		}
		cfg.Global.ListenAddr = listenEntry.Text
		cfg.Global.LogRequestBody = logLevelSelect.Selected
		cfg.Global.ControlAllowRemote = remoteCheck.Checked
		cfg.Global.CBThreshold = threshold
		cfg.Global.CBCooldown = cooldown
		cfg.Global.CBSkipRequests = skip
		cfg.Global.MaxStreamMinutes = maxStream
		target := defaultRouteSelect.Selected
		found := false
		for i := range cfg.ModelRoutes {
			if cfg.ModelRoutes[i].Alias == "default" {
				found = true
				if target == "不设置" {
					cfg.ModelRoutes = append(cfg.ModelRoutes[:i], cfg.ModelRoutes[i+1:]...)
				} else {
					cfg.ModelRoutes[i].Target = target
				}
				break
			}
		}
		if !found && target != "不设置" {
			cfg.ModelRoutes = append(cfg.ModelRoutes, config.ModelRoute{Alias: "default", Target: target})
		}
		if err := saveConfig(cfg, pm.configPath); err != nil {
			dialog.NewError(err, w)
			return
		}
		if err := pm.restart(); err != nil {
			dialog.NewError(err, w)
			return
		}
		loadQuickSettings()
	})

	quickSettings := container.NewVBox(
		widget.NewLabel("快速设置"),
		container.NewGridWithColumns(2,
			widget.NewLabel("监听地址"), listenEntry,
			widget.NewLabel("日志级别"), logLevelSelect,
			widget.NewLabel("远程控制"), remoteCheck,
			widget.NewLabel("熔断阈值"), cbThresholdEntry,
			widget.NewLabel("冷却(秒)"), cbCooldownEntry,
			widget.NewLabel("探测请求"), cbSkipEntry,
			widget.NewLabel("流式时长(分)"), maxStreamEntry,
			widget.NewLabel("默认路由"), defaultRouteSelect,
		),
		saveQuickBtn,
	)

	logActions := container.NewHBox(followLogsCheck, clearLogBtn)
	logHeader := container.NewBorder(nil, nil, nil, logActions, logLabel)
	settingsPane := container.NewVBox(
		container.NewHBox(statusLabel, addrLabel, uptimeLabel, provLabel),
		widget.NewSeparator(),
		container.NewHBox(startBtn, stopBtn, restartBtn, openBtn, editBtn),
		widget.NewSeparator(),
		quickSettings,
	)
	logPane := container.NewBorder(logHeader, nil, nil, nil, logView)
	w.SetContent(container.NewBorder(settingsPane, nil, nil, nil, logPane))

	if err := pm.start(); err != nil {
		statusLabel.SetText("● 启动失败")
		if initErr != nil {
			dialog.ShowError(initErr, w)
		}
	} else if firstRun {
		dialog.ShowInformation("首次启动", "已创建配置文件，请在“编辑配置”中填写供应商 API Key。", w)
	}
	loadQuickSettings()

	if desk, ok := a.(desktop.App); ok {
		trayMenu := fyne.NewMenu("AI Proxy",
			fyne.NewMenuItem("▶ 启动服务", func() { _ = pm.start() }),
			fyne.NewMenuItem("⏹ 停止服务", pm.stop),
			fyne.NewMenuItem("↻ 重启服务", func() { _ = pm.restart() }),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("📊 打开监控面板", func() { openBrowser(pm.dashboardURL()) }),
			fyne.NewMenuItem("⚙ 编辑配置", func() { showConfigEditor(a, w, pm) }),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("🖥 显示窗口", w.Show),
			fyne.NewMenuItem("✕ 退出", func() { pm.stop(); a.Quit() }),
		)
		desk.SetSystemTrayMenu(trayMenu)
		desk.SetSystemTrayWindow(w)
		desk.SetSystemTrayIcon(theme.ComputerIcon())
		w.SetCloseIntercept(w.Hide)
	}

	go pollStatus(pm, statusLabel, addrLabel, uptimeLabel, provLabel)
	go pollLogs(pm, logView, &autoFollowLogs)

	w.ShowAndRun()
	pm.stop()
}

func showConfigEditor(a fyne.App, parent fyne.Window, pm *proxyManager) {
	cfg := pm.configSnapshot()
	if cfg == nil {
		dialog.NewInformation("配置不可用", "请先修复配置文件并重新启动", parent)
		return
	}

	cfgWin := a.NewWindow("编辑配置 — " + pm.configPath)
	cfgWin.SetFixedSize(false)
	cfgWin.Resize(fyne.NewSize(720, 720))

	provListLabel := widget.NewLabel(fmt.Sprintf("供应商列表 (%d)", len(cfg.Providers)))
	provList := container.NewVBox()

	nameEntry := widget.NewEntry()
	nameEntry.SetPlaceHolder("唯一标识，如 glm-5.2")
	modelEntry := widget.NewEntry()
	modelEntry.SetPlaceHolder("实际模型 ID，如 gpt-4o")
	apiKeyEntry := widget.NewPasswordEntry()
	apiKeyEntry.SetPlaceHolder("sk-...")
	urlEntry := widget.NewEntry()
	urlEntry.SetPlaceHolder("https://api.openai.com/v1")
	formatSelect := widget.NewSelect([]string{"openai", "anthropic"}, nil)
	formatSelect.SetSelected("openai")
	priorityEntry := widget.NewEntry()
	priorityEntry.SetPlaceHolder("1")
	timeoutEntry := widget.NewEntry()
	timeoutEntry.SetPlaceHolder("60")
	editingIndex := -1
	formTitle := widget.NewLabel("添加供应商")
	var submitBtn *widget.Button

	resetForm := func() {
		editingIndex = -1
		nameEntry.SetText("")
		modelEntry.SetText("")
		apiKeyEntry.SetText("")
		urlEntry.SetText("")
		priorityEntry.SetText("")
		timeoutEntry.SetText("")
		formatSelect.SetSelected("openai")
		formTitle.SetText("添加供应商")
		if submitBtn != nil {
			submitBtn.SetText("➕ 添加供应商")
		}
	}

	var refreshList func()
	refreshList = func() {
		provListLabel.SetText(fmt.Sprintf("供应商列表 (%d)", len(cfg.Providers)))
		provList.RemoveAll()
		for i := range cfg.Providers {
			idx := i
			p := cfg.Providers[idx]
			info := fmt.Sprintf("P%d  %s  (%s, %s)", p.Priority, p.Name, p.ModelID, p.Format)
			editProviderBtn := widget.NewButton("编辑", func() {
				editingIndex = idx
				nameEntry.SetText(p.Name)
				modelEntry.SetText(p.ModelID)
				apiKeyEntry.SetText("")
				apiKeyEntry.SetPlaceHolder("留空则保留现有密钥")
				urlEntry.SetText(p.BaseURL)
				formatSelect.SetSelected(p.Format)
				priorityEntry.SetText(strconv.Itoa(p.Priority))
				timeoutEntry.SetText(strconv.Itoa(p.Timeout))
				formTitle.SetText("编辑供应商: " + p.Name)
				submitBtn.SetText("保存供应商")
			})
			deleteProviderBtn := widget.NewButton("删除", func() {
				cfg.Providers = append(cfg.Providers[:idx], cfg.Providers[idx+1:]...)
				resetForm()
				refreshList()
			})
			actions := container.NewHBox(editProviderBtn, deleteProviderBtn)
			provList.Add(container.NewBorder(nil, nil, nil, actions, widget.NewLabel(info)))
		}
	}

	submitBtn = widget.NewButton("➕ 添加供应商", func() {
		if nameEntry.Text == "" || modelEntry.Text == "" || urlEntry.Text == "" || (editingIndex < 0 && apiKeyEntry.Text == "") {
			dialog.NewInformation("缺少信息", "名称、模型 ID、API Key、Base URL 不能为空", cfgWin)
			return
		}
		for i, provider := range cfg.Providers {
			if provider.Name == nameEntry.Text && i != editingIndex {
				dialog.NewInformation("名称重复", "供应商名称必须唯一", cfgWin)
				return
			}
		}
		priority := 1
		if n, err := strconv.Atoi(priorityEntry.Text); err == nil && n > 0 {
			priority = n
		}
		timeout := 60
		if n, err := strconv.Atoi(timeoutEntry.Text); err == nil && n > 0 {
			timeout = n
		}
		apiKey := apiKeyEntry.Text
		provider := config.Provider{
			Name:     nameEntry.Text,
			Vendor:   formatSelect.Selected,
			ModelID:  modelEntry.Text,
			APIKey:   apiKey,
			BaseURL:  urlEntry.Text,
			Format:   formatSelect.Selected,
			Priority: priority,
			Timeout:  timeout,
			Retry:    config.RetryConfig{MaxRetries: 3, RetryInterval: 2, BackoffFactor: 2},
		}
		if editingIndex >= 0 {
			old := cfg.Providers[editingIndex]
			if provider.APIKey == "" {
				provider.APIKey = old.APIKey
			}
			provider.AuthType = old.AuthType
			if old.Vendor != "" {
				provider.Vendor = old.Vendor
			}
			provider.RateLimit = old.RateLimit
			provider.Retry = old.Retry
			cfg.Providers[editingIndex] = provider
		} else {
			cfg.Providers = append(cfg.Providers, provider)
		}
		resetForm()
		refreshList()
	})
	refreshList()

	saveRestartBtn := widget.NewButton("💾 保存并重启服务", func() {
		if len(cfg.Providers) == 0 {
			dialog.NewInformation("配置无效", "至少需要一个供应商", cfgWin)
			return
		}
		if err := saveConfig(cfg, pm.configPath); err != nil {
			dialog.NewError(err, cfgWin)
			return
		}
		if err := pm.restart(); err != nil {
			dialog.NewError(err, cfgWin)
			return
		}
		dialog.NewInformation("完成", "配置已保存，服务已重启", cfgWin)
	})

	openYamlBtn := widget.NewButton("📝 打开 YAML", func() {
		openEditor(pm.configPath)
	})

	providerPane := container.NewBorder(
		container.NewVBox(provListLabel, widget.NewSeparator()),
		nil, nil, nil,
		container.NewVScroll(provList),
	)
	editorPane := container.NewVScroll(container.NewVBox(
		formTitle,
		container.NewGridWithColumns(2,
			widget.NewLabel("名称"), nameEntry,
			widget.NewLabel("模型ID"), modelEntry,
			widget.NewLabel("API Key"), apiKeyEntry,
			widget.NewLabel("Base URL"), urlEntry,
			widget.NewLabel("格式"), formatSelect,
			widget.NewLabel("优先级"), priorityEntry,
			widget.NewLabel("超时(秒)"), timeoutEntry,
		),
		submitBtn,
		widget.NewSeparator(),
		container.NewHBox(saveRestartBtn, openYamlBtn),
	))
	providerSplit := container.NewVSplit(providerPane, editorPane)
	providerSplit.SetOffset(0.42)
	cfgWin.SetContent(providerSplit)

	cfgWin.Show()
}

func pollStatus(pm *proxyManager, statusLabel, addrLabel, uptimeLabel, provLabel *widget.Label) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if !pm.isRunning() {
			fyne.Do(func() {
				statusLabel.SetText("● 已停用")
				addrLabel.SetText("监听 " + pm.addrText())
				uptimeLabel.SetText("未运行")
				provLabel.SetText(fmt.Sprintf("%d 个供应商", pm.providerCount()))
			})
			continue
		}
		url := pm.dashboardURL() + "/api/stats"
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		var stats map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&stats)
		resp.Body.Close()

		running, _ := stats["running"].(bool)
		statusText := "● 已停用"
		if running {
			statusText = "● 运行中"
		}
		up := pm.uptimeText()
		addr := "监听 " + pm.addrText()
		provT := fmt.Sprintf("%d 个供应商", pm.providerCount())

		fyne.Do(func() {
			statusLabel.SetText(statusText)
			uptimeLabel.SetText(up)
			addrLabel.SetText(addr)
			provLabel.SetText(provT)
		})
	}
}

func pollLogs(pm *proxyManager, logView *widget.TextGrid, autoFollow *atomic.Bool) {
	const historyLimit = 200

	for {
		if !pm.isRunning() {
			time.Sleep(time.Second)
			continue
		}
		pm.mu.Lock()
		ctx := pm.runCtx
		if ctx == nil {
			ctx = context.Background()
		}
		pm.mu.Unlock()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, pm.dashboardURL()+"/api/logs", nil)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		resp, err := (&http.Client{}).Do(request)
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		scanner := bufio.NewScanner(resp.Body)
		lines := make([]string, 0, historyLimit)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				lines = append(lines, strings.TrimSpace(line[5:]))
				if len(lines) > historyLimit {
					lines = lines[len(lines)-historyLimit:]
				}
				text := strings.Join(lines, "\n")
				fyne.Do(func() {
					logView.SetText(text)
					if autoFollow.Load() {
						logView.ScrollToBottom()
					}
				})
			}
		}
		_ = resp.Body.Close()
		time.Sleep(time.Second)
	}
}
