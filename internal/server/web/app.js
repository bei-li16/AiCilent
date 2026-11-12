(function () {
  'use strict';
  const C = window.AiDashboardCore;
  const $ = id => document.getElementById(id);
  const state = {
    data: null, lastGood: 0, statsError: '', fetching: false, pane: 'logs', filters: { query: '', priority: '', model: '', result: '', level: '' },
    providerView: 'all', groupBy: 'model',
    providerPage: 1, requestPage: 1, sort: 'priority', requests: [], requestSignature: '', logs: new C.LogBuffer(), follow: true,
    unread: 0, stopped: false, eventSource: null, selectedProvider: null, selectedRequest: null,
    chartSelections: {}, chartCache: {}, trendWindow: 0, confirm: null
  };
  let pollTimer, freshnessTimer, logFrame, toastTimer, statsController, chartResizeTimer;
  const setText = (element, text) => { if (element && element.textContent !== String(text)) element.textContent = String(text); };
  const text = (id, value) => setText($(id), value);
  const timingText = ms => Number.isFinite(ms) ? C.formatDuration(ms) : '—';
  const requestSignature = data => JSON.stringify([data.recent_attempts, data.recent_requests]);
  function node(tag, className, content) {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (content !== undefined) el.textContent = String(content);
    return el;
  }
  function badge(outcome) { return node('span', 'badge ' + outcome.tone, outcome.label); }
  function replaceBadge(el, outcome) { el.className = 'badge ' + outcome.tone; setText(el, outcome.label); }
  function timeLabel(value, full) {
    const when = typeof value === 'number' ? value : C.timestamp(value);
    if (!when) return '—';
    return new Date(when).toLocaleString('zh-CN', full ? { hour12: false } : { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
  }
  function toast(message) {
    text('toast', message); $('toast').hidden = false;
    clearTimeout(toastTimer); toastTimer = setTimeout(() => { $('toast').hidden = true; }, 3500);
  }
  function setConnection(id, message, tone) {
    const el = $(id); el.className = 'connection ' + (tone || '');
    if (el.dataset.message !== message) { el.replaceChildren(node('i'), document.createTextNode(message)); el.dataset.message = message; }
  }
  function updateFreshness() {
    const age = state.lastGood ? Math.floor((Date.now() - state.lastGood) / 1000) : null;
    if (state.statsError) setConnection('statsStatus', '统计中断 · 保留缓存', 'bad');
    else if (age === null) setConnection('statsStatus', '统计连接中', '');
    else if (age > 15) setConnection('statsStatus', '统计数据过期', 'warning');
    else setConnection('statsStatus', '统计已更新', 'good');
    text('updatedAt', age === null ? '等待首次更新' : '更新于 ' + timeLabel(state.lastGood) + ' · ' + age + ' 秒前');
    const fixture = state.data && state.data.fixture === true;
    $('dataNotice').hidden = !state.statsError && !fixture && !(age > 15);
    if (!$('dataNotice').hidden) text('dataNotice', (fixture ? '隔离验收数据 · 非生产统计。' : '') + (state.statsError ?
      '统计暂不可用：' + state.statsError + (state.lastGood ? '。当前显示最后一次有效数据。' : '。尚未收到有效数据。') : age > 15 ? '统计数据已过期，正在尝试重新获取。' : ''));
  }
  async function fetchStats() {
    if (state.fetching || state.stopped) return;
    state.fetching = true; statsController = new AbortController();
    const timeout = setTimeout(() => statsController.abort(), 8000);
    try {
      const response = await fetch('/api/stats', { cache: 'no-store', signal: statsController.signal });
      if (!response.ok) throw new Error('HTTP ' + response.status);
      const data = C.validateSnapshot(await response.json());
      state.data = data; state.lastGood = Date.now(); state.statsError = '';
      state.logModelFor = C.logProviderIndex(data.providers, data.recent_attempts);
      state.logView = null;
      renderMetrics(); syncFilters(); renderDiagnostic(); renderPriorities(); renderProviders();
      const signature = requestSignature(data);
      if (!state.requestSignature) refreshRequestView();
      else if (signature !== state.requestSignature) {
        text('refreshRequests', '有新记录 · 更新列表'); $('refreshRequests').classList.add('warning-text');
      }
      if (state.selectedProvider) renderProviderDetail(state.selectedProvider, false);
      drawCharts();
      if (state.pane === 'logs' && state.follow) scheduleLogFrame();
    } catch (error) {
      if (!state.stopped) state.statsError = error.name === 'AbortError' ? '请求超过 8 秒' : error.message;
    } finally {
      clearTimeout(timeout); state.fetching = false; updateFreshness();
      if (!state.stopped) { clearTimeout(pollTimer); pollTimer = setTimeout(fetchStats, document.hidden ? 10000 : 2000); }
    }
  }
  async function fetchVersion() {
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 8000);
    try {
      const response = await fetch('/api/version', { cache: 'no-store', signal: controller.signal });
      if (!response.ok) throw new Error('HTTP ' + response.status);
      const version = await response.json();
      const build = version.build_date && version.build_date !== 'unknown' ? timeLabel(version.build_date, true) : '构建时间未知';
      text('buildInfo', (version.version || '版本未知') + ' · ' + (version.commit || 'commit 未知') + ' · 构建 ' + build);
      $('buildInfo').title = '构建时间原值：' + (version.build_date || 'unknown');
      const embedded = document.querySelector('meta[name="dashboard-build"]').content;
      if (embedded !== version.version) toast('页面资源与服务版本不一致，请刷新页面。');
    } catch (_) { text('buildInfo', '版本信息不可用'); }
    finally { clearTimeout(timer); }
  }
  function renderMetrics() {
    const d = state.data, rate = C.ratio(d.total_success, d.total_fail);
    text('totalClient', C.formatCount(d.total_client_req)); text('totalAttempts', C.formatCount(d.total_req));
    const completed = C.count(d.total_client_completed);
    text('totalClientSuccess', C.formatCount(d.total_client_success));
    text('clientSuccessScope', completed ? '已记录结果 ' + C.formatCount(completed) + ' 笔' : '暂无已记录的请求结果');
    text('totalFailed', C.formatCount(d.total_fail)); text('totalCanceled', C.formatCount(d.total_canceled));
    text('attemptRate', C.formatRate(rate));
    text('rateFraction', C.formatCount(d.total_success) + ' / ' + C.formatCount(d.total_success + d.total_fail) + ' 次有效尝试');
    text('attemptAverage', d.total_client_req > 0 ? '平均 ' + (d.total_req / d.total_client_req).toFixed(2) + ' 次上游尝试 / 请求' : '暂无客户端请求样本');
    const service = $('serviceStatus'); service.className = 'badge ' + (d.running ? 'success' : 'neutral');
    service.replaceChildren(node('i'), document.createTextNode(d.running ? '请求转发运行中' : '请求转发已停用'));
    text('serviceControl', d.running ? '停用转发' : '启用转发');
    $('serviceControl').disabled = false;
    text('uptime', C.formatUptime(d.uptime)); $('uptime').title = '服务启动：' + timeLabel(d.start_time, true) + ' · 原始时长：' + d.uptime;
  }
  function setOptions(el, options, selected) {
    const signature = JSON.stringify(options);
    if (el.dataset.options !== signature) {
      const values = options.slice();
      if (selected && !values.some(o => o[0] === selected)) values.push([selected, selected + '（当前筛选）']);
      el.replaceChildren(...values.map(([value, label]) => { const option = node('option', '', label); option.value = value; return option; }));
      el.dataset.options = signature;
    }
    el.value = selected;
  }
  function renderProviderNavigation() {
    const providers = state.data.providers;
    const views = [['all', '全部'], ['streak', '连续失败'], ['limited', '最近限流'], ['success', '最近成功'], ['unknown', '无最近状态']];
    const items = views.map(([key, label]) => ({ key, label, count: providers.filter(p => C.providerMatchesView(p, key)).length }));
    const create = () => { const button = node('button', 'filter-chip'); button.type = 'button'; button.append(node('span'), node('span', 'chip-count')); return button; };
    reconcile($('providerViews'), items, v => v.key, create, (button, v) => {
      button.dataset.view = v.key; button.setAttribute('aria-pressed', String(state.providerView === v.key));
      setText(button.firstChild, v.label); setText(button.lastChild, v.count);
    });
    const field = state.groupBy, values = new Map(), active = new Set(C.activePriorities(state.data));
    providers.forEach(p => { const value = field === 'model' ? p.model_id : String(p.priority); if (value) values.set(value, (values.get(value) || 0) + 1); });
    const selected = state.filters[field];
    const groups = [{ key: '', label: '全部', count: providers.length }, ...Array.from(values).sort((a, b) => field === 'priority' ? Number(a[0]) - Number(b[0]) : a[0].localeCompare(b[0], 'zh-CN', { numeric: true })).map(([key, count]) => ({ key, count, label: field === 'priority' ? priorityFilterLabel(Number(key), active) : key }))];
    reconcile($('providerGroups'), groups, v => v.key, create, (button, v) => {
      button.dataset.group = v.key; button.setAttribute('aria-pressed', String(selected === v.key));
      setText(button.firstChild, v.label); setText(button.lastChild, v.count);
      button.title = v.label + ' · ' + v.count + ' 个已记录供应商；点击切换该分组';
    });
  }
  const priorityFilterLabel = (priority, active) => 'P' + priority + (active.has(priority) ? '' : '（历史）');
  function syncFilters() {
    const d = state.data;
    const active = new Set(C.activePriorities(d));
    const priorities = new Set([...active, ...d.providers.map(p => Number(p.priority)), ...d.recent_attempts.map(a => Number(a.priority))]);
    const models = new Set([...d.providers.map(p => p.model_id), ...d.recent_attempts.map(a => a.model_id)].filter(Boolean));
    setOptions($('priorityFilter'), [['', '全部优先级'], ...Array.from(priorities).filter(p => p > 0).sort((a, b) => a - b).map(p => [String(p), priorityFilterLabel(p, active)])], state.filters.priority);
    setOptions($('modelFilter'), [['', '全部模型'], ...Array.from(models).sort().map(m => [m, m])], state.filters.model);
    const labels = state.pane === 'providers' ? ['全部最近状态', '最近成功', '最近限流 429', '最近其他失败', '最近取消', '暂无尝试'] :
      ['全部样本结果', '含成功尝试', '含限流 429', '含其他失败', '含取消', '暂无尝试'];
    const options = state.pane === 'logs' ? [
      ['', '全部日志结果'], ['success', '成功'], ['limited', '限流 429'],
      ['route-switch', '账号转接'], ['route-skip', '跳过账号'], ['client-limited', '返回 429'], ['failed', '其他失败'], ['canceled', '取消']
    ] : ['', 'success', 'limited', 'failed', 'canceled', 'unseen'].map((key, i) => [key, labels[i]]);
    setOptions($('resultFilter'), options, state.filters.result);
  }
  function renderDiagnostic() {
    const attempts = state.data.recent_attempts.slice(-C.ATTEMPT_LIMIT), counts = { limited: 0, failed: 0, canceled: 0, success: 0 };
    attempts.forEach(a => { counts[C.attemptOutcome(a).key]++; });
    const issue = counts.limited ? 'limited' : counts.failed ? 'failed' : counts.canceled ? 'canceled' : '';
    $('diagnostic').classList.toggle('calm', !issue);
    text('diagnosticIcon', issue ? '!' : 'i');
    text('diagnosticTitle', attempts.length ? counts.limited ? '最近样本存在限流，检查重试与账号切换' : counts.failed ? '最近样本存在上游失败' : counts.canceled ? '最近样本包含客户端取消' : '最近样本未观察到上游失败' : '暂无最近尝试样本');
    text('diagnosticText', attempts.length ? '最近 ' + attempts.length + ' 条尝试：成功 ' + counts.success + ' · 限流 ' + counts.limited + ' · 其他失败 ' + counts.failed + ' · 取消 ' + counts.canceled + '。该窗口不代表全部历史或客户端最终结果。' : '历史累计计数与最近尝试窗口独立；真实尝试发生后可在这里检查。');
    $('inspectDiagnostic').disabled = !attempts.length; $('inspectDiagnostic').dataset.result = issue;
    text('providerTabCount', state.data.providers.length); text('requestTabCount', C.groupRequests(attempts, state.data.recent_requests).length);
  }
  function renderPriorities() {
    const grid = $('priorityGrid'), d = state.data, states = new Map((d.cb || []).map(c => [Number(c.priority), c]));
    const all = new Set(C.activePriorities(d));
    if (!all.size) { if (!grid.querySelector('.empty-inline')) grid.replaceChildren(node('p', 'empty-inline', '暂无优先级记录')); return; }
    grid.querySelector('.empty-inline')?.remove();
    const retained = new Set();
    Array.from(all).filter(p => p > 0).sort((a, b) => a - b).forEach(priority => {
      retained.add(String(priority));
      let card = grid.querySelector('[data-priority="' + priority + '"]');
      if (!card) {
        card = node('button', 'priority-card'); card.type = 'button'; card.dataset.priority = priority;
        card.append(node('span', 'priority-label', 'P' + priority));
        const copy = node('span', 'priority-copy'); copy.append(node('span'), node('small')); card.append(copy, node('span', 'priority-state')); grid.append(card);
      }
      card.style.setProperty('--priority-color', C.priorityColor(priority));
      const cb = states.get(priority), sample = d.recent_attempts.filter(a => Number(a.priority) === priority), limited = sample.filter(a => C.attemptOutcome(a).key === 'limited').length;
      const lines = card.querySelector('.priority-copy');
      setText(lines.children[0], cb ? cb.open ? '熔断打开' : '熔断关闭' : '无熔断记录');
      setText(lines.children[1], cb ? '失败记录 ' + C.count(cb.failures) + (cb.open ? ' · 冷却剩余 ' + C.count(cb.cooldown_rem_sec) + 's' : ' · 允许路由') : '不能据此判定健康');
      const quality = card.querySelector('.priority-state'); quality.className = 'priority-state' + (limited ? ' warning-text' : '');
      setText(quality, sample.length ? '近样本 ' + limited + ' 次限流' : '暂无近样本');
      card.title = 'P' + priority + '：点击筛选最近尝试；熔断状态与上游质量分别统计。';
    });
    Array.from(grid.children).forEach(child => { if (!retained.has(child.dataset.priority)) child.remove(); });
  }
  function reconcile(body, items, key, create, update) {
    const existing = new Map(Array.from(body.children).map(el => [el.dataset.key, el]));
    const wanted = new Set();
    items.forEach((item, i) => {
      const id = key(item); wanted.add(id);
      const row = existing.get(id) || create(item); row.dataset.key = id;
      update(row, item);
      if (body.children[i] !== row) body.insertBefore(row, body.children[i] || null);
    });
    Array.from(body.children).forEach(el => { if (!wanted.has(el.dataset.key)) el.remove(); });
  }
  function tableRow(columns) {
    const row = node('tr'); for (let i = 0; i < columns; i++) row.append(node('td'));
    return row;
  }
  function pagination(prefix, page, count, unit) {
    const pages = Math.ceil(count / C.PAGE_SIZE), start = count ? (page - 1) * C.PAGE_SIZE + 1 : 0;
    text(prefix + 'PageInfo', start + '–' + Math.min(page * C.PAGE_SIZE, count) + ' / ' + count + ' ' + unit);
    text(prefix + 'PageNumber', (pages ? page : 0) + ' / ' + pages);
    $(prefix + 'Prev').disabled = page <= 1; $(prefix + 'Next').disabled = !pages || page >= pages;
  }
  function renderProviders() {
    if (!state.data) return;
    renderProviderNavigation();
    const all = C.sortProviders(C.filterProviders(state.data.providers, state.filters, state.providerView), state.sort);
    state.providerPage = Math.min(state.providerPage, Math.max(1, Math.ceil(all.length / C.PAGE_SIZE)));
    const page = all.slice((state.providerPage - 1) * C.PAGE_SIZE, state.providerPage * C.PAGE_SIZE);
    text('providerCount', '匹配 ' + all.length + ' / ' + state.data.providers.length + ' 个供应商 · 结果筛选按最近状态');
    $('providerEmpty').hidden = all.length > 0; $('providerTable').hidden = all.length === 0;
    reconcile($('providerBody'), page, p => p.name, () => {
      const row = tableRow(11);
      row.cells[0].append(node('span', 'priority-tag'));
      row.cells[1].append(node('button', 'table-link provider-name'), node('span', 'provider-model'));
      row.cells[1].firstChild.type = 'button'; row.cells[1].firstChild.dataset.action = 'provider';
      for (let i = 2; i <= 8; i++) row.cells[i].className = 'number';
      row.cells[9].append(badge({ tone: 'neutral', label: '' }));
      const button = node('button', 'row-detail', '↗'); button.type = 'button'; button.dataset.action = 'provider'; row.cells[10].append(button);
      return row;
    }, (row, p) => {
      const priority = row.cells[0].firstChild; setText(priority, 'P' + p.priority); priority.style.setProperty('--priority-color', C.priorityColor(p.priority));
      const name = row.cells[1].firstChild; setText(name, C.providerLabel(p)); name.title = p.name; name.dataset.name = p.name;
      setText(row.cells[1].lastChild, p.model_id); row.cells[1].lastChild.title = p.model_id;
      [p.total, p.success, p.fail, p.client_abort].forEach((n, i) => setText(row.cells[i + 2], C.formatCount(n)));
      const streak = C.providerStreak(p); setText(row.cells[6], streak === null ? '—' : C.formatCount(streak));
      row.cells[6].classList.toggle('warning-text', streak > 0);
      row.cells[6].title = streak === null ? '旧记录缺少最近状态，无法还原连续失败次数' : '成功后归零；取消不增加也不中断';
      setText(row.cells[7], C.formatRate(C.ratio(p.success, p.fail)));
      setText(row.cells[8], p.latency_count > 0 ? C.formatDuration(p.latency_avg_ms) : '—');
      replaceBadge(row.cells[9].firstChild, C.providerOutcome(p));
      const button = row.cells[10].firstChild; button.dataset.name = p.name; button.setAttribute('aria-label', '查看供应商 ' + p.name + ' 详情');
    });
    pagination('provider', state.providerPage, all.length, '条');
  }
  function refreshRequestView() {
    if (!state.data) return;
    state.requests = C.groupRequests(state.data.recent_attempts, state.data.recent_requests); state.requestSignature = requestSignature(state.data);
    text('refreshRequests', '更新请求列表'); $('refreshRequests').classList.remove('warning-text'); renderRequests();
  }
  function renderRequests() {
    const all = C.filterRequests(state.requests, state.filters);
    state.requestPage = Math.min(state.requestPage, Math.max(1, Math.ceil(all.length / C.PAGE_SIZE)));
    const page = all.slice((state.requestPage - 1) * C.PAGE_SIZE, state.requestPage * C.PAGE_SIZE);
    text('requestCount', '匹配 ' + all.length + ' 组 · 本次快照含 ' + state.requests.filter(g => g.timing).length + ' 条请求计时及 ' + state.requests.reduce((n, g) => n + g.attempts.length, 0) + ' 条上游尝试');
    $('requestEmpty').hidden = all.length > 0; $('requestTable').hidden = all.length === 0;
    reconcile($('requestBody'), page, g => g.id, () => {
      const row = tableRow(9), id = node('button', 'table-link request-id'); id.type = 'button'; id.dataset.action = 'request'; row.cells[0].append(id);
      row.cells[1].append(node('span', 'provider-name'), node('span', 'provider-model'));
      row.cells[2].className = 'number'; row.cells[4].append(badge({ tone: 'neutral', label: '' })); row.cells[5].className = row.cells[6].className = 'number';
      const button = node('button', 'row-detail', '↗'); button.type = 'button'; button.dataset.action = 'request'; row.cells[8].append(button);
      return row;
    }, (row, g) => {
      const id = row.cells[0].firstChild; setText(id, g.missingId ? '未提供请求 ID' : g.id.slice(0, 12)); id.dataset.id = g.id; id.title = g.id;
      setText(row.cells[1].firstChild, g.models.join(' / ') || '模型未知'); setText(row.cells[1].lastChild, g.priorities.map(p => 'P' + p).join(' / '));
      setText(row.cells[2], g.attempts.length); setText(row.cells[3], g.providers.length + ' 个账号'); replaceBadge(row.cells[4].firstChild, g.outcome);
      setText(row.cells[5], timingText(g.timing?.first_response_ms)); setText(row.cells[6], timingText(g.timing?.total_ms)); setText(row.cells[7], timeLabel(g.latest));
      row.cells[8].firstChild.dataset.id = g.id; row.cells[8].firstChild.setAttribute('aria-label', '查看请求 ' + g.id + ' 的已知尝试');
    });
    pagination('request', state.requestPage, all.length, '组');
  }

  function drawCharts(force) {
    if (!state.data) return;
    for (const kind of ['rate', 'latency']) {
      const curves = C.chartSeries(state.data, kind, state.trendWindow);
      if (!state.chartSelections[kind]) {
        let saved = null;
        try { saved = JSON.parse(localStorage.getItem('ai-dashboard.curves.' + kind)); } catch (_) { /* Storage can be disabled. */ }
        state.chartSelections[kind] = C.chartSelection(curves.map(c => c.priority), saved);
      }
      const selection = state.chartSelections[kind];
      reconcile($(kind + 'Legend'), curves, c => String(c.priority), c => {
        const button = node('button', 'legend-button'); button.type = 'button'; button.dataset.priority = c.priority;
        button.append(node('span', 'legend-check', '✓'), node('span', 'legend-swatch'), node('span')); return button;
      }, (button, c) => {
        button.style.setProperty('--series-color', c.color); button.children[1].style.background = c.color;
        const value = c.points.length ? kind === 'rate' ? C.formatRate(c.points.at(-1)) : C.formatDuration(c.points.at(-1) * 1000) : '—';
        setText(button.lastChild, c.label + ' ' + value);
        button.setAttribute('aria-pressed', String(selection.has(c.priority)));
        button.setAttribute('aria-label', c.label + '曲线');
        button.title = c.label + ' · ' + c.points.length + ' 个独立采样点；点击显示或隐藏，可多选';
      });
      const selected = curves.filter(c => selection.has(c.priority));
      if (kind === 'latency') drawLatencyStatistics(selected);
      text(kind + 'Caption', selected.length ? '独立样本序号 · 非时间轴' + (kind === 'latency' ? ' · 秒 / 不含退避' : ' · 按 50 次上游尝试滚动计算') : '点击图例选择一条或多条曲线');
      setText($(kind + 'Empty').querySelector('strong'), !selected.length ? '未选择曲线' : kind === 'rate' ? '暂无成功率样本' : '暂无成功耗时样本');
      drawChart(kind, selected.filter(c => c.points.length), force);
    }
  }
  function drawLatencyStatistics(curves) {
    const samples = $('latencySamples'), quantiles = $('latencyQuantiles');
    samples.hidden = quantiles.hidden = !curves.length;
    if (!curves.length) return;
    const summary = C.latencySummary(curves);
    samples.style.setProperty('--series-color', summary.color);
    quantiles.style.setProperty('--series-color', summary.color);
    text('latencySampleCount', C.formatCount(summary.count));
    for (const [id, seconds] of [['latencyP50', summary.p50], ['latencyP95', summary.p95]]) {
      const value = $(id);
      const formatted = seconds === null ? '—' : C.formatDuration(seconds * 1000);
      if (value.textContent !== formatted) {
        value.replaceChildren(...formatted.split(/(\d+(?:\.\d+)?)/).map((part, i) =>
          i % 2 ? node('strong', 'latency-number', part) : part));
      }
    }
  }
  function drawChart(kind, series, force) {
    const svg = $(kind + 'Chart'), canvas = $(kind + 'Canvas'), empty = $(kind + 'Empty');
    const rect = canvas.getBoundingClientRect(), width = Math.max(250, Math.round(rect.width)), height = Math.max(48, Math.round(rect.height));
    const signature = JSON.stringify([width, height, series]);
    if (!force && state.chartCache[kind]?.signature === signature) return;
    const maxLength = Math.max(0, ...series.map(s => s.points.length)), pad = { left: 38, right: 13, top: 8, bottom: 23 };
    const plotWidth = width - pad.left - pad.right, plotHeight = height - pad.top - pad.bottom;
    const points = series.flatMap(s => s.points), rawMax = Math.max(0, ...points);
    const scale = Math.pow(10, Math.floor(Math.log10(Math.max(.01, rawMax))));
    const multiple = rawMax / scale;
    const steps = height < 110 ? 2 : 4;
    const { minimum, maximum } = kind === 'rate' ? C.rateAxis(points, steps) :
      { minimum: 0, maximum: Math.max(.01, (multiple > 5 ? 10 : multiple > 2 ? 5 : multiple > 1 ? 2 : 1) * scale) };
    state.chartCache[kind] = { signature, width, height, series, maxLength, pad, plotWidth, maximum, active: 0 };
    empty.hidden = maxLength > 0;
    if (!maxLength) { svg.replaceChildren(); svg.setAttribute('aria-label', kind === 'rate' ? '暂无成功率样本' : '暂无成功耗时样本'); $(kind + 'Tooltip').hidden = true; return; }
    const x = i => pad.left + plotWidth * i / Math.max(1, maxLength - 1);
    const y = value => pad.top + plotHeight - (value - minimum) / (maximum - minimum) * plotHeight;
    svg.setAttribute('viewBox', '0 0 ' + width + ' ' + height);
    svg.setAttribute('aria-label', (kind === 'rate' ? '上游尝试成功率' : '成功上游尝试耗时') + '，' + maxLength + ' 个独立采样点。左右方向键查看数值。');
    let markup = '';
    for (let i = 0; i <= steps; i++) {
      const value = minimum + (maximum - minimum) * i / steps, at = y(value);
      markup += '<line x1="' + pad.left + '" y1="' + at + '" x2="' + (width - pad.right) + '" y2="' + at + '" stroke="#273448" stroke-width="1"/>';
      markup += '<text x="' + (pad.left - 7) + '" y="' + (at + 3) + '" fill="#a5b9d2" font-size="10" text-anchor="end">' + Number(value.toFixed(2)) + (kind === 'rate' ? '%' : 's') + '</text>';
    }
    const ticks = Array.from(new Set([0, Math.floor((maxLength - 1) / 2), maxLength - 1]));
    for (const index of ticks) markup += '<text x="' + x(index) + '" y="' + (height - 10) + '" fill="#91a3bb" font-size="10" text-anchor="middle">#' + (index + 1) + '</text>';
    for (const s of series) {
      const points = s.points.map((v, i) => x(i).toFixed(2) + ',' + y(v).toFixed(2)).join(' ');
      markup += '<polyline data-priority="' + s.priority + '" points="' + points + '" fill="none" stroke="' + s.color + '" stroke-width="' + (s.priority === 0 ? '1.5' : '2') + '"' + (s.priority === 0 ? ' stroke-dasharray="5 4" opacity=".8"' : '') + ' stroke-linejoin="round" stroke-linecap="round"/>';
      if (s.points.length) markup += '<circle cx="' + x(s.points.length - 1) + '" cy="' + y(s.points.at(-1)) + '" r="3" fill="' + s.color + '"/>';
    }
    svg.innerHTML = markup; // Only finite geometry and generated colors enter this SVG.
  }
  function chartTooltip(kind, index) {
    const cache = state.chartCache[kind]; if (!cache?.maxLength) return;
    cache.active = Math.max(0, Math.min(cache.maxLength - 1, index));
    const lines = ['采样 #' + (cache.active + 1) + ' · 非时间轴'];
    cache.series.forEach(s => { if (cache.active < s.points.length) lines.push(s.label + '：' + (kind === 'rate' ? C.formatRate(s.points[cache.active]) : C.formatDuration(s.points[cache.active] * 1000))); });
    text(kind + 'Tooltip', lines.join('\n')); $(kind + 'Tooltip').hidden = false;
  }
  function bindChart(kind) {
    const svg = $(kind + 'Chart');
    svg.addEventListener('pointermove', event => {
      const cache = state.chartCache[kind]; if (!cache?.maxLength) return;
      const x = event.clientX - svg.getBoundingClientRect().left;
      chartTooltip(kind, Math.round((x - cache.pad.left) / cache.plotWidth * (cache.maxLength - 1)));
    });
    svg.addEventListener('pointerleave', () => { $(kind + 'Tooltip').hidden = true; });
    svg.addEventListener('focus', () => chartTooltip(kind, (state.chartCache[kind]?.maxLength || 1) - 1));
    svg.addEventListener('blur', () => { $(kind + 'Tooltip').hidden = true; });
    svg.addEventListener('keydown', event => {
      const cache = state.chartCache[kind]; if (!cache?.maxLength) return;
      if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') { event.preventDefault(); chartTooltip(kind, cache.active + (event.key === 'ArrowLeft' ? -1 : 1)); }
    });
  }
  function detailGrid(items) {
    const grid = node('dl', 'detail-grid');
    items.forEach(([label, value]) => { const item = node('div', 'detail-item'); item.append(node('dt', '', label), node('dd', '', value)); grid.append(item); });
    return grid;
  }
  function actionButton(label, action, className = 'secondary') { const button = node('button', 'button ' + className, label); button.type = 'button'; button.addEventListener('click', action); return button; }
  function showDetail(kind, title, contents) {
    text('detailKind', kind); text('detailTitle', title); $('detailBody').replaceChildren(...contents);
    if (!$('detailDrawer').open) $('detailDrawer').showModal();
    $('detailDrawer').scrollTop = 0;
  }
  async function copyText(value) {
    try {
      if (navigator.clipboard?.writeText && window.isSecureContext) await navigator.clipboard.writeText(value);
      else {
        const focused = document.activeElement, input = node('textarea'); input.value = value; input.setAttribute('aria-label', '待复制的完整文本');
        input.style.cssText = 'position:fixed;opacity:0;width:1px;height:1px;left:0;top:0';
        ($('detailDrawer').open ? $('detailDrawer') : document.body).append(input); input.select();
        const copied = document.execCommand('copy'); input.remove(); if (focused?.focus) focused.focus();
        if (!copied) throw new Error('Clipboard unavailable');
      }
      toast('已复制完整内容');
    } catch (_) { toast('复制不可用，请在详情中选择完整文本手动复制。'); }
  }
  function filterTo(pane, values) {
    Object.assign(state.filters, { query: '', priority: '', model: '', result: '', level: '' }, values); state.providerView = 'all';
    $('logLevelFilter').value = state.filters.level;
    $('searchInput').value = state.filters.query; state.providerPage = state.requestPage = 1;
    setPane(pane); syncFilters(); renderActive(); $('workspace').scrollIntoView({ block: 'start', behavior: 'auto' });
  }
  function renderProviderDetail(name, open = true) {
    if (!open || !state.data) return;
    const p = state.data.providers.find(p => p.name === name); if (!p) return;
    state.selectedProvider = name; state.selectedRequest = null;
    const title = node('pre', 'detail-raw', p.name), status = badge(C.providerOutcome(p));
    const actions = node('div', 'drawer-actions');
    actions.append(actionButton('复制完整名称', () => copyText(p.name)), actionButton('筛选关联请求', () => { $('detailDrawer').close(); filterTo('requests', { query: p.name }); }),
      actionButton('查看关联日志', () => { $('detailDrawer').close(); filterTo('logs', { query: p.name }); }));
    const items = [['模型', p.model_id || '—'], ['优先级', 'P' + p.priority], ['已记录尝试', C.formatCount(p.total)], ['成功 / 失败', C.formatCount(p.success) + ' / ' + C.formatCount(p.fail)],
      ['取消尝试', C.formatCount(p.client_abort)], ['最近连续失败', C.providerStreak(p) === null ? '未知（旧存档无最近状态）' : C.formatCount(C.providerStreak(p))], ['有效成功率', C.formatRate(C.ratio(p.success, p.fail))], ['平均尝试耗时', p.latency_count > 0 ? C.formatDuration(p.latency_avg_ms) : '—'], ['最近活动', timeLabel(p.last_activity, true)]];
    const note = node('p', 'detail-description', '打开时的数据快照。平均耗时包含所有已记录的上游尝试，不是客户端端到端耗时。');
    const error = node('pre', 'detail-raw', p.last_err || '当前快照没有最近错误。');
    showDetail('供应商详情 · 累计统计', C.providerLabel(p), [status, actions, title, detailGrid(items), note, node('h3', '', '最近错误原文'), error]);
  }
  function renderRequestDetail(id) {
    const g = state.requests.find(g => g.id === id); if (!g) return;
    state.selectedRequest = id; state.selectedProvider = null;
    const note = node('p', 'detail-description', '仅最近尝试窗口中的已知记录，可能缺少起点或结尾。重试和切换次数仅统计窗口内相邻记录；完整次数未知。下面的序号是记录顺序，不是完整请求的重试编号。');
    const fullId = node('pre', 'detail-raw', g.missingId ? '服务端未提供请求 ID' : g.id), actions = node('div', 'drawer-actions');
    if (!g.missingId) actions.append(actionButton('复制完整请求 ID', () => copyText(g.id)), actionButton('关联原始日志', () => { $('detailDrawer').close(); filterTo('logs', { query: g.id }); }));
    const timeline = node('ol', 'timeline');
    g.attempts.forEach((a, i) => {
      const outcome = C.attemptOutcome(a), entry = node('li'), heading = node('div', 'timeline-title');
      entry.style.setProperty('--timeline-color', outcome.key === 'limited' ? '#f2b65a' : outcome.key === 'success' ? '#38c795' : outcome.key === 'failed' ? '#f16e7a' : '#9babbe');
      heading.append(node('span', '', '记录 ' + String(i + 1).padStart(2, '0')), badge(outcome));
      const code = a.status_code ? (a.outcome === 'canceled' ? '取消记录码 ' : '记录状态码 ') + a.status_code : '未提供状态码';
      entry.append(heading, node('p', 'mono', a.provider || '未知账号'), node('p', '', (a.model_id || '模型未知') + ' · P' + a.priority + ' · ' + code + ' · ' + C.formatDuration(a.latency_ms)), node('p', '', timeLabel(a.timestamp, true)));
      timeline.append(entry);
    });
    showDetail('请求追踪 · 最近记录', g.missingId ? '未提供请求 ID' : g.id.slice(0, 12), [badge(g.outcome), actions, fullId, note,
      detailGrid([['已知尝试', g.attempts.length], ['涉及账号', g.providers.length], ['同账号重试（可见记录）', g.knownRetries], ['账号切换（可见记录）', g.knownSwitches], ['首次成功响应', timingText(g.timing?.first_response_ms)], ['请求总耗时', timingText(g.timing?.total_ms)], ['最近记录', timeLabel(g.latest, true)]]),
      node('p', 'detail-description', '请求计时包含排队、重试和换号。首响是代理首次写出 HTTP 2xx 正文；请求总耗时截至代理处理结束。无测量值显示 —。'), timeline]);
  }

  function updateLogControls() {
    const buffer = state.logs;
    text('logTabCount', buffer.rows.length);
    text('logFollowState', state.follow ? '自动跟随' : '阅读模式');
    text('pauseFollow', state.follow ? '暂停跟随' : '继续跟随');
    $('jumpLatest').hidden = state.follow && !state.unread;
    text('jumpLatest', state.unread ? '新增 ' + C.formatCount(state.unread) + ' 条 · 跳到最新' : '跳到最新');
    text('logBufferInfo', '缓存 ' + buffer.rows.length + '/' + C.LOG_LIMIT + ' · 最多显示 ' + C.LOG_VIEW_LIMIT + ' 条' +
      (buffer.dropped ? ' · 已丢弃 ' + C.formatCount(buffer.dropped) + ' 条更早记录' : '') + (state.logReconnected ? ' · 重连期间可能有缺口' : ''));
  }
  function renderLogs(incremental = false) {
    updateLogControls();
    if (state.pane !== 'logs') return;
    const modelFor = state.logModelFor || (() => '');
    const rows = state.logs.rows;
    const priorityFor = C.logPriorityIndex(rows, state.data?.recent_attempts);
    // A new attempt can resolve the priority of an earlier summary row.
    const previous = incremental && !state.filters.priority && state.logView;
    let matching;
    if (previous) {
      let from = rows.length;
      while (from > 0 && rows[from - 1].id > previous.lastId) from--;
      matching = previous.matching;
      let expired = 0;
      while (expired < matching.length && matching[expired].id < (rows[0]?.id ?? Infinity)) expired++;
      if (expired) matching.splice(0, expired);
      matching.push(...C.filterLogs(rows.slice(from), state.filters, modelFor, priorityFor));
    } else matching = C.filterLogs(rows, state.filters, modelFor, priorityFor);
    state.logView = { matching, lastId: rows.at(-1)?.id ?? 0 };
    const visible = matching.slice(-C.LOG_VIEW_LIMIT);
    const container = $('logRows'), scrollTop = container.scrollTop;
    text('logFilterInfo', '匹配 ' + matching.length + ' 条 · 当前显示 ' + visible.length + ' 条。级别按事件和结果归类；仅当前浏览器缓存，点击行展开原文。');
    $('logEmpty').hidden = visible.length > 0; container.hidden = visible.length === 0;
    const entries = $('logEntries');
    const create = log => {
      const details = node('details', 'log-row'), summary = node('summary');
      const id = node('button', 'table-link request-id'); id.type = 'button'; id.dataset.copyId = log.requestId; id.title = '复制完整请求 ID';
      const time = node('span', 'log-timestamp');
      time.append(node('span', 'log-time', log.time), node('span', 'log-event', log.event));
      const provider = node('span', 'log-provider');
      if (log.provider) {
        const heading = node('span', 'log-provider-heading');
        const priority = node('span', 'badge log-priority'); priority.hidden = true;
        heading.append(priority, node('span', 'log-provider-model'));
        provider.append(heading);
      }
      const providerName = node('span', 'log-provider-name', log.provider || '—');
      if (log.provider) providerName.title = log.provider;
      provider.append(providerName);
      if (log.provider && log.providerRole) provider.append(node('span', 'log-provider-role', log.providerRole));
      const result = node('span', 'log-result');
      result.append(node('span', 'log-severity level-' + log.level, log.level.toUpperCase()));
      if (log.key !== 'neutral') {
        const resultBadge = badge({ tone: log.tone, label: log.label });
        const meanings = { limited: '本次上游尝试实际收到 HTTP 429',
          'route-switch': '路由选中了下一个可尝试的账号。实际调用及其结果见后续的上游尝试日志。',
          'route-skip': '该候选账号未通过限流检查，本次请求直接略过该账号。每个略过的账号各有一条记录。',
          'client-limited': '这是代理返回给客户端的状态，不另计一次上游尝试' };
        if (meanings[log.key] || log.description) resultBadge.title = meanings[log.key] || log.description;
        result.append(resultBadge);
      }
      if (log.milestone) {
        details.classList.add(log.milestone.tone);
        const milestone = badge(log.milestone);
        if (log.description) milestone.title = log.description;
        result.append(milestone);
      }
      const elapsed = node('span', 'log-elapsed');
      if (log.timingLabel) {
        const value = node('span', '', C.formatLogDuration(log.timingMs));
        value.title = log.timingDescription + (Number.isFinite(log.timingMs) ? ' 原始耗时：' + log.timingMs.toLocaleString('zh-CN', { maximumFractionDigits: 6 }) + ' ms。' : '');
        elapsed.append(node('span', 'log-elapsed-label', log.timingLabel), value);
      }
      else setText(elapsed, '—');
      if (log.retryGapMs !== null) {
        const label = node('span', 'log-elapsed-label log-retry-gap', '重试发起间隔');
        label.title = '同一账号上一次尝试发起到本次发起的实际间隔';
        elapsed.append(label, node('span', '', C.formatLogDuration(log.retryGapMs)));
      }
      const attempt = node('span', 'log-attempt mono', log.attemptNumber || '—');
      if (log.attemptNumber) attempt.title = '本账号第几次尝试 / 本账号最大尝试次数（含首次）';
      summary.append(time, id, provider, result, attempt, elapsed, node('span', 'log-message', log.detail.slice(0, 450)));
      setText(id, log.requestId ? log.requestId.slice(0, 10) : '—'); id.disabled = !log.requestId;
      details.append(summary, node('pre', '', log.raw));
      return details;
    };
    const update = (row, log) => {
      if (!log.provider) return;
      const knownModel = modelFor(log), model = knownModel || '模型未知';
      const label = row.querySelector('.log-provider-model');
      setText(label, model);
      if (label.title !== model) label.title = model;
      const priority = priorityFor(log), tag = row.querySelector('.log-priority');
      row.dataset.logPriority = String(priority);
      row.classList.toggle('log-model-row', Boolean(knownModel));
      row.style.setProperty('--log-priority-color', C.priorityColor(priority));
      tag.hidden = priority <= 0;
      if (priority > 0) {
        setText(tag, 'P' + priority);
        tag.style.setProperty('--priority-color', C.priorityColor(priority));
        tag.title = '优先级 P' + priority + (log.priority ? '' : '（关联同一请求、同一供应商的尝试记录）');
      }
    };
    if (previous) {
      const oldest = visible[0]?.id ?? Infinity;
      while (entries.firstElementChild && Number(entries.firstElementChild.dataset.key) < oldest) entries.firstElementChild.remove();
      const newest = Number(entries.lastElementChild?.dataset.key || 0);
      const existing = new Map(Array.from(entries.children, row => [Number(row.dataset.key), row]));
      const additions = document.createDocumentFragment();
      for (const log of visible) {
        if (log.id <= newest) {
          const row = existing.get(log.id);
          if (row && row.dataset.logPriority !== String(priorityFor(log))) update(row, log);
          continue;
        }
        const row = create(log); row.dataset.key = String(log.id); update(row, log); additions.append(row);
      }
      entries.append(additions);
    } else reconcile(entries, visible, log => String(log.id), create, update);
    if (state.follow) {
      state.unread = 0; state.adjustingLogScroll = true; container.scrollTop = container.scrollHeight;
      requestAnimationFrame(() => { state.adjustingLogScroll = false; });
    } else container.scrollTop = scrollTop;
    updateLogControls();
  }
  function scheduleLogFrame() {
    if (logFrame !== undefined || state.stopped || document.hidden) return;
    logFrame = requestAnimationFrame(() => {
      logFrame = undefined;
      if (state.stopped || document.hidden) return;
      if (state.follow && state.pane === 'logs') renderLogs(true);
      else updateLogControls();
    });
  }
  function receiveLog(line) {
    if (!line.trim()) return;
    const structured = /^\[\d{4}-\d{2}-\d{2}/.test(line);
    state.logSeen ||= new Set();
    if (structured && state.logSeen.has(line)) return;
    if (state.logs.rows.length >= C.LOG_LIMIT) state.logSeen.delete(state.logs.rows[0].raw);
    const row = state.logs.push(line);
    if (structured && row) state.logSeen.add(row.raw);
    if (!state.follow) state.unread++;
    scheduleLogFrame();
  }
  function startLogStream() {
    if (state.stopped || (state.eventSource && state.eventSource.readyState !== EventSource.CLOSED)) return;
    const source = new EventSource('/api/logs'); state.eventSource = source;
    source.onopen = () => {
      if (state.logConnectedBefore) state.logReconnected = true;
      state.logConnectedBefore = true; setConnection('logStatus', '日志已连接', 'good'); updateLogControls();
    };
    source.onmessage = event => receiveLog(event.data);
    source.onerror = () => setConnection('logStatus', '日志重连中', 'warning');
  }
  function setPane(pane, focus = false) {
    if ((pane !== 'logs' && ['route-switch', 'route-skip', 'client-limited'].includes(state.filters.result)) ||
        (pane === 'logs' && state.filters.result === 'unseen')) state.filters.result = '';
    state.pane = pane;
    $('logLevelControl').hidden = pane !== 'logs';
    const names = { providers: '供应商累计统计 · 结果按最近状态', requests: '最近尝试窗口 · 非完整请求历史', logs: '当前浏览器有界缓存' };
    text('workspaceScope', names[pane]);
    document.querySelectorAll('[role="tab"]').forEach(button => {
      const selected = button.dataset.pane === pane;
      button.setAttribute('aria-selected', String(selected)); button.tabIndex = selected ? 0 : -1;
      $(button.getAttribute('aria-controls')).hidden = !selected;
      if (selected && focus) button.focus();
    });
    if (state.data) syncFilters();
    if (pane === 'requests') refreshRequestView();
    renderActive();
  }
  function renderActive() {
    if (state.pane === 'providers') renderProviders();
    else if (state.pane === 'requests') renderRequests();
    else renderLogs();
  }
  function openConfirm(kind, trigger) {
    if (!state.data && kind === 'service') { toast('服务状态尚不可用，请稍后重试。'); return; }
    const actions = {
      service: { title: state.data?.running ? '停用请求转发？' : '启用请求转发？', message: state.data?.running ?
        '将停用网关的新请求转发，已有请求继续按原业务逻辑处理。此操作会影响真实服务。' : '将恢复网关的新请求转发。', endpoint: '/api/control', payload: { running: !state.data?.running } },
      reset: { title: '重置服务端统计？', message: '累计统计和最近尝试样本将被清零，操作不可恢复。不会删除 YAML 配置或日志文件。', endpoint: '/api/stats/reset' },
      logs: { title: '清空服务端日志文件？', message: '将截断服务端当前日志文件，操作不可恢复；现存备份文件保留。其他浏览器的日志缓存不会因此自动清空。', endpoint: '/api/logs/clear' }
    };
    state.confirm = { ...actions[kind], kind, trigger, busy: false };
    text('confirmTitle', state.confirm.title); text('confirmMessage', state.confirm.message); $('confirmError').hidden = true;
    text('confirmAction', kind === 'service' ? '确认' : '确认并执行'); $('confirmDialog').showModal();
  }
  async function confirmAction() {
    const action = state.confirm; if (!action || action.busy) return;
    action.busy = true; $('confirmAction').disabled = $('cancelAction').disabled = true;
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 10000);
    try {
      const response = await fetch(action.endpoint, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: action.payload ? JSON.stringify(action.payload) : undefined, signal: controller.signal });
      if (!response.ok) throw new Error(response.status === 403 ? '管理操作被拒绝：仅允许本机或已配置的远程管理访问。' : '服务返回 HTTP ' + response.status);
      await response.json();
      if (action.kind === 'reset') { state.providerPage = state.requestPage = 1; state.requestSignature = ''; state.requests = []; }
      if (action.kind === 'logs') { state.logs.clear(); state.logSeen?.clear(); state.unread = 0; renderLogs(); }
      $('confirmDialog').close(); toast('操作已完成'); fetchStats();
    } catch (error) {
      text('confirmError', error.name === 'AbortError' ? '请求超时，操作结果未知，请核对服务状态后再试。' : error.message); $('confirmError').hidden = false;
    } finally { clearTimeout(timer); action.busy = false; $('confirmAction').disabled = $('cancelAction').disabled = false; }
  }
  let searchTimer;
  $('filterForm').addEventListener('submit', event => event.preventDefault());
  $('searchInput').addEventListener('input', () => {
    clearTimeout(searchTimer); searchTimer = setTimeout(() => { state.filters.query = $('searchInput').value; state.providerPage = state.requestPage = 1; renderActive(); }, 150);
  });
  for (const [id, key] of [['priorityFilter', 'priority'], ['modelFilter', 'model'], ['resultFilter', 'result']]) $(id).addEventListener('change', () => {
    state.filters[key] = $(id).value; state.providerPage = state.requestPage = 1; renderActive();
  });
  $('logLevelFilter').addEventListener('change', () => { state.filters.level = $('logLevelFilter').value; renderLogs(); });
  $('providerGroupBy').addEventListener('change', () => {
    state.groupBy = $('providerGroupBy').value; state.filters.model = state.filters.priority = ''; state.providerPage = 1;
    if (state.data) syncFilters(); renderProviders();
  });
  $('providerGroups').addEventListener('click', event => {
    const button = event.target.closest('button[data-group]'); if (!button) return;
    state.filters[state.groupBy] = button.dataset.group; state.filters[state.groupBy === 'model' ? 'priority' : 'model'] = '';
    state.providerPage = 1; syncFilters(); renderProviders(); $('providerScroll').scrollTop = 0;
  });
  $('providerViews').addEventListener('click', event => {
    const button = event.target.closest('button[data-view]'); if (!button) return;
    state.providerView = button.dataset.view; state.providerPage = 1;
    if (state.providerView === 'streak') { state.sort = 'streak'; $('providerSort').value = state.sort; }
    renderProviders(); $('providerScroll').scrollTop = 0;
  });
  $('resetFilters').addEventListener('click', () => {
    state.filters = { query: '', priority: '', model: '', result: '', level: '' }; state.providerView = 'all'; $('searchInput').value = ''; $('logLevelFilter').value = ''; state.providerPage = state.requestPage = 1;
    if (state.data) syncFilters(); renderActive();
  });
  $('providerSort').addEventListener('change', () => { state.sort = $('providerSort').value; state.providerPage = 1; renderProviders(); });
  $('providerPrev').addEventListener('click', () => { state.providerPage = Math.max(1, state.providerPage - 1); renderProviders(); });
  $('providerNext').addEventListener('click', () => { state.providerPage++; renderProviders(); });
  $('requestPrev').addEventListener('click', () => { state.requestPage = Math.max(1, state.requestPage - 1); renderRequests(); });
  $('requestNext').addEventListener('click', () => { state.requestPage++; renderRequests(); });
  $('refreshRequests').addEventListener('click', refreshRequestView);
  document.querySelectorAll('[role="tab"]').forEach(button => {
    button.addEventListener('click', () => setPane(button.dataset.pane));
    button.addEventListener('keydown', event => {
      const panes = ['logs', 'providers', 'requests']; let index = panes.indexOf(state.pane);
      if (event.key === 'ArrowRight') index = (index + 1) % 3;
      else if (event.key === 'ArrowLeft') index = (index + 2) % 3;
      else if (event.key === 'Home') index = 0;
      else if (event.key === 'End') index = 2;
      else return;
      event.preventDefault(); setPane(panes[index], true);
    });
  });
  $('providerBody').addEventListener('click', event => { const button = event.target.closest('button[data-action="provider"]'); if (button) renderProviderDetail(button.dataset.name); });
  $('requestBody').addEventListener('click', event => { const button = event.target.closest('button[data-action="request"]'); if (button) renderRequestDetail(button.dataset.id); });
  $('closeDetail').addEventListener('click', () => $('detailDrawer').close());
  $('detailDrawer').addEventListener('close', () => { state.selectedProvider = state.selectedRequest = null; });
  $('inspectDiagnostic').addEventListener('click', () => filterTo('requests', { result: $('inspectDiagnostic').dataset.result || '' }));
  $('priorityGrid').addEventListener('click', event => { const card = event.target.closest('[data-priority]'); if (card) filterTo('requests', { priority: card.dataset.priority }); });
  for (const kind of ['rate', 'latency']) {
    const save = () => { try { localStorage.setItem('ai-dashboard.curves.' + kind, JSON.stringify([...state.chartSelections[kind]])); } catch (_) { /* Optional preference. */ } };
    $(kind + 'Legend').addEventListener('click', event => {
      const button = event.target.closest('[data-priority]'); if (!button || !state.chartSelections[kind]) return;
      const priority = Number(button.dataset.priority), selection = state.chartSelections[kind];
      if (selection.has(priority)) selection.delete(priority); else selection.add(priority);
      save(); drawCharts(true);
    });
    $(kind + 'All').addEventListener('click', () => { if (!state.data) return; state.chartSelections[kind] = new Set(C.chartSeries(state.data, kind).map(c => c.priority)); save(); drawCharts(true); });
    $(kind + 'Overall').addEventListener('click', () => { state.chartSelections[kind] = new Set([0]); save(); drawCharts(true); });
  }
  for (const kind of ['rate', 'latency']) bindChart(kind);
  $('trendRequestWindow').addEventListener('change', () => {
    state.trendWindow = Number($('trendRequestWindow').value);
    drawCharts(true);
  });
  $('logRows').addEventListener('click', event => {
    const button = event.target.closest('[data-copy-id]'); if (!button?.dataset.copyId) return;
    event.preventDefault(); event.stopPropagation(); copyText(button.dataset.copyId);
  });
  $('logRows').addEventListener('scroll', () => {
    if (state.manualLogPause || state.adjustingLogScroll || state.pane !== 'logs') return;
    const el = $('logRows'), atBottom = el.scrollHeight - el.scrollTop - el.clientHeight <= 30;
    if (state.follow && !atBottom) { state.follow = false; updateLogControls(); }
    else if (!state.follow && atBottom) { state.follow = true; renderLogs(); }
  }, { passive: true });
  $('pauseFollow').addEventListener('click', () => { state.follow = !state.follow; state.manualLogPause = !state.follow; if (state.follow) renderLogs(); else updateLogControls(); });
  $('jumpLatest').addEventListener('click', () => { state.manualLogPause = false; state.follow = true; renderLogs(); });
  $('clearLogView').addEventListener('click', () => { state.logs.clear(); state.logSeen?.clear(); state.unread = 0; renderLogs(); toast('当前浏览器视图已清空，服务端日志保留。'); });
  $('serviceControl').addEventListener('click', event => openConfirm('service', event.currentTarget));
  $('resetStats').addEventListener('click', event => openConfirm('reset', event.currentTarget));
  $('clearServerLogs').addEventListener('click', event => openConfirm('logs', event.currentTarget));
  $('confirmAction').addEventListener('click', confirmAction);
  $('cancelAction').addEventListener('click', () => $('confirmDialog').close());
  $('confirmDialog').addEventListener('cancel', event => { if (state.confirm?.busy) event.preventDefault(); });
  $('confirmDialog').addEventListener('close', () => {
    state.confirm?.trigger?.focus();
    state.confirm = null;
  });
  const resize = new ResizeObserver(() => { clearTimeout(chartResizeTimer); chartResizeTimer = setTimeout(() => drawCharts(true), 100); });
  resize.observe($('rateCanvas')); resize.observe($('latencyCanvas'));
  function start() {
    state.stopped = false; setPane(state.pane); fetchStats(); fetchVersion(); startLogStream();
    clearInterval(freshnessTimer); freshnessTimer = setInterval(updateFreshness, 1000);
  }
  window.addEventListener('pagehide', () => {
    state.stopped = true; state.eventSource?.close(); statsController?.abort(); clearTimeout(pollTimer); clearInterval(freshnessTimer); cancelAnimationFrame(logFrame); logFrame = undefined; clearTimeout(searchTimer); clearTimeout(chartResizeTimer);
  });
  window.addEventListener('pageshow', event => { if (event.persisted) start(); });
  document.addEventListener('visibilitychange', () => { if (!document.hidden && !state.stopped) { fetchStats(); scheduleLogFrame(); } });
  start();
})();
