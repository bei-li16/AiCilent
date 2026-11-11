/* Shared, DOM-free dashboard semantics. Browser and Node tests use the same rules. */
(function (root, factory) {
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.AiDashboardCore = factory();
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';
  const ATTEMPT_LIMIT = 200;
  const LOG_LIMIT = 1000;
  const LOG_VIEW_LIMIT = 200;
  const PAGE_SIZE = 15;
  const number = value => Number.isFinite(Number(value)) ? Math.max(0, Number(value)) : 0;
  const count = value => Math.floor(number(value));
  const ratio = (success, failed) => number(success) + number(failed) > 0 ? number(success) / (number(success) + number(failed)) * 100 : null;
  const formatCount = value => new Intl.NumberFormat('zh-CN').format(count(value));
  const formatRate = value => value === null || !Number.isFinite(value) ? '—' : value.toFixed(1) + '%';
  function formatDuration(ms) {
    if (ms === null || ms === undefined || !Number.isFinite(Number(ms)) || Number(ms) < 0) return '—';
    ms = Number(ms);
    if (ms < 1000) return Math.round(ms) + ' ms';
    if (ms < 60000) return (ms / 1000).toFixed(ms < 10000 ? 2 : 1) + ' s';
    return Math.floor(ms / 60000) + 'm ' + Math.round(ms % 60000 / 1000) + 's';
  }
  function formatLogDuration(ms) {
    if (!Number.isFinite(ms) || ms < 0) return '—';
    if (ms < 1 && ms > 0) return (ms * 1000).toFixed(3) + ' µs';
    return ms < 1000 ? Number(ms.toFixed(3)) + ' ms' : (ms / 1000).toFixed(3) + ' s';
  }
  function formatUptime(text) {
    if (!text) return '运行时长未知';
    const hours = Number((String(text).match(/(\d+)h/) || [])[1] || 0);
    const minutes = Number((String(text).match(/(\d+)m/) || [])[1] || 0);
    if (hours >= 24) return '已运行 ' + Math.floor(hours / 24) + ' 天 ' + hours % 24 + ' 小时';
    if (hours) return '已运行 ' + hours + ' 小时 ' + minutes + ' 分钟';
    return '已运行 ' + minutes + ' 分钟';
  }
  function timestamp(value) { const n = Date.parse(value || ''); return Number.isFinite(n) && n > 0 ? n : 0; }
  function priorityColor(priority) {
    if (!Number.isFinite(Number(priority)) || Number(priority) <= 0) return '#7eaeed';
    const palette = ['#54d6bb', '#b5a0ff', '#f6bd70'];
    if (Number(priority) <= palette.length) return palette[Number(priority) - 1];
    return 'hsl(' + Math.round((Number(priority) * 137.508 + 18) % 360) + ' 61% 69%)';
  }
  function activePriorities(data) {
    const values = Array.isArray(data.configured_priorities) ? data.configured_priorities :
      [...(data.providers || []).map(p => p.priority), ...(data.cb || []).map(p => p.priority)];
    return [...new Set(values.map(Number).filter(p => Number.isInteger(p) && p > 0))].sort((a, b) => a - b);
  }
  function trendPoints(series, requestSeq, requestWindow) {
    const minSeq = Math.max(1, number(requestSeq) - requestWindow + 1);
    return (series?.points || []).filter((v, i) => Number.isFinite(v) && v >= 0 &&
      (!requestWindow || (series.requests?.[i] >= minSeq && series.requests[i] <= requestSeq))).slice(-240);
  }
  function chartSeries(data, kind, requestWindow = 0) {
    const source = kind === 'rate' ? data.curves : Array.isArray(data.latency_curves) ? data.latency_curves : [{ priority: 0, points: data.latency_curve }];
    const byPriority = new Map((source || []).filter(c => c && Array.isArray(c.points)).map(c => [Number(c.priority), c]));
    return [0, ...activePriorities(data)].map(priority => ({ priority, label: priority === 0 ? '汇总' : 'P' + priority,
      color: priorityColor(priority), points: trendPoints(byPriority.get(priority), data.trend_request_seq, requestWindow)
        .map(v => kind === 'rate' ? Math.min(100, v) : v) }));
  }
  function modelLatencySeries(data, requestWindow = 0) {
    const models = Array.isArray(data.model_latency_curves) ? data.model_latency_curves : [];
    return models.filter(c => c && typeof c.model === 'string' && c.model && Array.isArray(c.points))
      .map(c => ({ model: c.model, points: trendPoints(c, data.trend_request_seq, requestWindow) }));
  }
  // null means the first visit: show every curve. An explicit [] means the
  // user hid every curve and must survive polling/reloads unchanged.
  function chartSelection(available, saved) {
    return new Set(Array.isArray(saved) ? saved.map(Number).filter(p => available.includes(p)) : available);
  }
  function rateAxis(values, steps = 4) {
    if (!values.length) return { minimum: 0, maximum: 100 };
    const low = Math.min(...values), high = Math.max(...values);
    const margin = Math.max(2, (high - low) * .08);
    const lower = Math.max(0, low - margin), upper = Math.min(100, high + margin);
    const ideal = (upper - lower) / steps;
    const power = 10 ** Math.floor(Math.log10(ideal));
    const step = [1, 2, 2.5, 5, 10].map(n => n * power).find(n => n >= ideal);
    return { minimum: Math.max(0, Math.floor(lower / step) * step),
      maximum: Math.min(100, Math.ceil(upper / step) * step) };
  }
  function quantile(values, p) {
    const sorted = (values || []).filter(v => Number.isFinite(v) && v >= 0).slice().sort((a, b) => a - b);
    if (!sorted.length) return null;
    const index = (sorted.length - 1) * Math.min(1, Math.max(0, p));
    const lo = Math.floor(index), hi = Math.ceil(index);
    return sorted[lo] + (sorted[hi] - sorted[lo]) * (index - lo);
  }
  function attemptOutcome(a) {
    if (a.outcome === 'canceled') return { key: 'canceled', label: '取消', tone: 'canceled' };
    if (a.success === true || a.outcome === 'success') return { key: 'success', label: '成功', tone: 'success' };
    if (Number(a.status_code) === 429) return { key: 'limited', label: '限流 429', tone: 'limited' };
    return { key: 'failed', label: a.status_code ? '失败 ' + a.status_code : '连接/协议失败', tone: 'failed' };
  }
  function providerOutcome(p) {
    if (!number(p.total)) return { key: 'unseen', label: '暂无尝试', tone: 'unseen' };
    if (!(timestamp(p.last_activity) > 0)) return { key: 'neutral', label: '有历史记录', tone: 'neutral' };
    if (p.last_err_type === 'client_abort') return { key: 'canceled', label: '最近取消', tone: 'canceled' };
    if (p.last_err_type || number(p.consecutive_fail)) {
      const limited = /rate|tpm|rpm|plan_exhausted/i.test(p.last_err_type || '');
      return { key: limited ? 'limited' : 'failed', label: limited ? '最近限流' : '最近失败', tone: limited ? 'limited' : 'failed' };
    }
    if (number(p.success)) return { key: 'success', label: '最近成功', tone: 'success' };
    return { key: 'neutral', label: '有历史记录', tone: 'neutral' };
  }
  function providerLabel(p) {
    const name = String(p.name || '未命名供应商'), suffix = '-' + String(p.model_id || '');
    return p.model_id && name.endsWith(suffix) ? name.slice(0, -suffix.length) : name;
  }
  function providerStreak(p) {
    if (!number(p.total)) return 0;
    return timestamp(p.last_activity) > 0 ? count(p.consecutive_fail) : null;
  }
  function providerMatchesView(p, view) {
    if (view === 'streak') return providerStreak(p) > 0;
    if (view === 'unknown') return number(p.total) > 0 && !(timestamp(p.last_activity) > 0);
    if (view === 'limited' || view === 'success') return providerOutcome(p).key === view;
    return true;
  }
  function filterProviders(providers, filter, view = 'all') {
    const query = String(filter.query || '').trim().toLowerCase();
    return providers.filter(p => (!query || [p.name, p.model_id].join(' ').toLowerCase().includes(query)) &&
      (!filter.priority || Number(p.priority) === Number(filter.priority)) &&
      (!filter.model || p.model_id === filter.model) && (!filter.result || providerOutcome(p).key === filter.result) && providerMatchesView(p, view));
  }
  function sortProviders(providers, sort) {
    const nameOrder = (a, b) => String(a.name).localeCompare(String(b.name), 'zh-CN', { numeric: true });
    return providers.slice().sort((a, b) => {
      let delta = 0;
      if (sort === 'streak') delta = (providerStreak(b) ?? -1) - (providerStreak(a) ?? -1);
      else if (sort === 'latency') delta = number(b.latency_avg_ms) - number(a.latency_avg_ms);
      else if (sort === 'fail') delta = number(b.fail) - number(a.fail);
      else if (sort === 'rate') delta = (ratio(a.success, a.fail) ?? Infinity) - (ratio(b.success, b.fail) ?? Infinity);
      else if (sort === 'recent') delta = timestamp(b.last_activity) - timestamp(a.last_activity);
      else if (sort !== 'name') delta = number(a.priority) - number(b.priority);
      return (Number.isFinite(delta) ? delta : (delta === Infinity ? 1 : delta === -Infinity ? -1 : 0)) || nameOrder(a, b);
    });
  }
  function groupRequests(attempts, timings = []) {
    const groups = new Map();
    const group = (id, missingId = false) => {
      if (!groups.has(id)) groups.set(id, { id, missingId, attempts: [], latest: 0, duration: 0, providers: [], models: [], priorities: [], timing: null });
      return groups.get(id);
    };
    (attempts || []).slice(-ATTEMPT_LIMIT).forEach((a, i) => {
      const id = a.request_id || 'missing-id-' + i;
      const g = group(id, !a.request_id);
      g.attempts.push(a); g.latest = Math.max(g.latest, timestamp(a.timestamp)); g.duration += number(a.latency_ms);
      if (!g.providers.includes(a.provider)) g.providers.push(a.provider);
      if (!g.models.includes(a.model_id)) g.models.push(a.model_id);
      if (!g.priorities.includes(a.priority)) g.priorities.push(a.priority);
    });
    for (const timing of timings) {
      if (!timing.request_id) continue;
      const g = group(timing.request_id);
      g.timing = timing;
      g.latest = Math.max(g.latest, timestamp(timing.ended_at));
      if (!g.models.length && timing.model) g.models.push(timing.model);
      const priority = /^P([1-9]\d*)$/i.exec(timing.model || '');
      if (!g.priorities.length && priority) g.priorities.push(Number(priority[1]));
    }
    return Array.from(groups.values()).map(g => {
      g.attempts.sort((a, b) => timestamp(a.timestamp) - timestamp(b.timestamp));
      g.knownRetries = 0; g.knownSwitches = 0;
      g.attempts.forEach((a, i) => {
        if (!i || !a.provider || !g.attempts[i - 1].provider) return;
        if (a.provider === g.attempts[i - 1].provider) g.knownRetries++;
        else g.knownSwitches++;
      });
      const success = g.attempts.some(a => attemptOutcome(a).key === 'success');
      const canceled = g.attempts.some(a => attemptOutcome(a).key === 'canceled');
      g.outcome = success ? { key: 'success', label: '观察到成功', tone: 'success' } : canceled ?
        { key: 'canceled', label: '观察到取消', tone: 'canceled' } : g.timing?.status_code === 429 ?
        { key: 'limited', label: '请求限流 429', tone: 'limited' } : g.timing?.status_code >= 400 ?
        { key: 'failed', label: '请求失败 ' + g.timing.status_code, tone: 'failed' } : { key: 'unknown', label: '最终结果未知', tone: 'neutral' };
      return g;
    }).sort((a, b) => b.latest - a.latest || a.id.localeCompare(b.id));
  }
  function filterRequests(groups, filter) {
    const q = String(filter.query || '').trim().toLowerCase();
    return groups.filter(g => (!q || [g.id, ...g.providers, ...g.models].join(' ').toLowerCase().includes(q)) &&
      (!filter.priority || g.priorities.some(p => Number(p) === Number(filter.priority))) &&
      (!filter.model || g.models.includes(filter.model)) &&
      (!filter.result || g.outcome.key === filter.result || g.attempts.some(a => attemptOutcome(a).key === filter.result)));
  }
  function durationMs(text) {
    if (!text) return null;
    const unit = { h: 3600000, m: 60000, s: 1000, ms: 1, 'µs': .001, 'μs': .001, us: .001, ns: .000001 };
    const parts = [...text.matchAll(/(\d+(?:\.\d+)?)(ms|[µμu]s|ns|h|m|s)/g)];
    if (!parts.length || parts.map(part => part[0]).join('') !== text) return null;
    return parts.reduce((sum, [, value, scale]) => sum + Number(value) * unit[scale], 0);
  }
  function parseLog(raw, id) {
    raw = String(raw || '');
    if (raw.length > 16384) raw = raw.slice(0, 16384) + '\n[当前行超过 16 KiB，显示已截断]';
    const match = raw.match(/^\[([^\]]+)\]\s*\[([^\]]+)\]\s*(\w+)\s*\|\s*(.*)$/s);
    const body = match ? match[4] : raw;
    const event = match ? match[3].toUpperCase() : 'TEXT';
    const resultBody = ['INBOUND', 'TEXT'].includes(event) ? '' : body;
    const attempt = event === 'PROVIDER' && body.match(/^\[P(\d+)\]\[(\d+\/\d+)\]\s+(.+?)\s*\|\s*(.*)$/s);
    const next = event === 'ROUTER' && body.match(/^degrade to next provider\s*\|\s*(.+?)\s+\[P(\d+)\]\s*\|\s*reason=(.*)$/s);
    const skipped = event === 'QUEUE' && body.match(/^\[P(\d+)\]\s*\|\s*provider (.+?) skipping \((.*)\)\s*\|\s*rr=/s);
    const ttfb = event === 'TTFB' && body.match(/^(.+?)\s*\[P(\d+)\]\s*\|/);
    const retry = event === 'ROUTER' && body.match(/^non-retryable status=\d+\s*\|\s*skip retry\s*\|\s*(.+?)\s+\[P(\d+)\]$/);
    const summary = event === 'SUMMARY' && body.match(/\|\s*provider=([^|]*?)\s*\|\s*rr=/);
    const provider = (attempt?.[3] || next?.[1] || skipped?.[2] || ttfb?.[1] || retry?.[1] || summary?.[1] || '').trim();
    const priority = Number(attempt?.[1] || next?.[2] || skipped?.[1] || ttfb?.[2] || retry?.[2] ||
      (resultBody.match(/\[P(\d+)\]/) || [])[1] || 0);
    const providerRole = next ? '切换目标' : skipped ? '跳过对象' : '';
    let detail = attempt ? attempt[4] :
      next ? '切换原因：' + next[3] : skipped ? '跳过原因：' + skipped[3] : body;
    let elapsed, timingLabel, retryGapMs = null, attemptError = '';
    if (attempt) {
      elapsed = /^(OK|FAIL|CANCELED)\s*\|\s*(\S+)(?:\s*\|\s*(.*))?$/s.exec(attempt[4]);
      if (elapsed && durationMs(elapsed[2]) !== null) {
        timingLabel = '单次尝试';
        let reason = elapsed[3] || '';
        const gap = /^retry_start_gap=(\S+)(?:\s*\|\s*(.*))?$/s.exec(reason);
        if (gap && durationMs(gap[1]) !== null) {
          retryGapMs = durationMs(gap[1]); reason = gap[2] || '';
        }
        attemptError = reason;
        detail = elapsed[1] + (reason ? ' | ' + reason : '');
      }
    } else if (event === 'INBOUND') {
      timingLabel = '请求起点';
    } else {
      const rules = {
        TTFB: [/\s*\|\s*upstream header=(\S+)/, '上游响应头'],
        FIRST_RESPONSE: [/^elapsed=(\S+)$/, '请求首响'],
        STREAM: [/\s*\|\s*request_elapsed=(\S+)(?=\s*\|\s*end_reason=)/, '请求至流结束'],
        SUMMARY: [/\s*\|\s*total=(\S+)$/, '路由耗时'],
        ACCESS: [/\s*\|\s*\d{3}\s*\|\s*(\S+)(?=\s*\|)/, '请求总耗时']
      };
      const rule = rules[event];
      const value = rule?.[0].exec(body);
      if (value && durationMs(value[1]) !== null) {
        timingLabel = rule[1];
        elapsed = ['', '', value[1]];
        if (event === 'ACCESS') detail = body.replace(/(\|\s*\d{3})\s*\|\s*\S+(?=\s*\|)/, '$1');
        else if (event === 'FIRST_RESPONSE') detail = '首次写出 HTTP 2xx 响应正文';
        else detail = body.replace(rule[0], '');
      }
    }
    const status = attempt ? Number(/^API error \(status (\d{3})\)(?::|$)/.exec(attemptError)?.[1] || 0) :
      Number((resultBody.match(/status\s*[= ]\s*(\d{3})/) || [])[1] || (resultBody.match(/\|\s+(\d{3})\s+\|/) || [])[1] || 0);
    let key = 'neutral', label = '信息';
    if (next) { key = 'route-switch'; label = status === 429 ? '限流转接' : '账号转接'; }
    else if (skipped) { key = 'route-skip'; label = status === 429 ? '因 429 跳过' : '跳过账号'; }
    else if (attempt ? elapsed?.[1] === 'CANCELED' : /\bCANCELED\b/.test(resultBody)) { key = 'canceled'; label = '取消'; }
    else if (attempt && elapsed?.[1] === 'FAIL' && status === 429) { key = 'limited'; label = '限流 429'; }
    else if (event === 'ACCESS' && status === 429) { key = 'client-limited'; label = '返回 429'; }
    else if (/\|\s*FAIL\s*\||result=FAIL|stream interrupted/i.test(resultBody) || status >= 400) { key = 'failed'; label = status ? '失败 ' + status : '失败'; }
    else if (attempt ? elapsed?.[1] === 'OK' : /\|\s*OK\s*\||result=SUCCESS/.test(resultBody)) {
      key = 'success'; label = attempt ? '上游成功' : event === 'SUMMARY' ? '路由成功' : '成功';
    }
    // The backend emits event categories rather than a severity field. Keep
    // user-supplied request/body text from creating false ERROR/WARN levels.
    const explicit = ['DEBUG', 'INFO', 'WARN', 'WARNING', 'ERROR', 'FATAL'].includes(event) ? event :
      (!match ? (raw.match(/^\s*(?:\d{4}[-/]\d{2}[-/]\d{2}\s+\d{2}:\d{2}:\d{2}\s+)?\[?(DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL)\]?\b/) || [])[1] : '');
    let level = 'info';
    if (explicit) level = /ERROR|FATAL/.test(explicit) ? 'error' : /WARN/.test(explicit) ? 'warn' : explicit.toLowerCase();
    else if (!['INBOUND', 'TEXT'].includes(event) && key === 'failed') level = 'error';
    else if (key === 'limited' || key === 'client-limited' || (status === 429 && (next || skipped))) level = 'warn';
    else if (event === 'CB' && /\b(?:OPEN|opened|tripped)\b/.test(body)) level = 'warn';
    else if (['ROUTER', 'QUEUE', 'TTFB', 'STREAM'].includes(event) && key !== 'canceled') level = 'debug';
    const milestone = event === 'INBOUND' ? { label: '请求发起', tone: 'request-start' } :
      event === 'FIRST_RESPONSE' ? { label: '首次成功响应', tone: 'request-first' } :
      event === 'STREAM' && /\bterminal_written=true\s*\|\s*(?:request_elapsed=\S+\s*\|\s*)?end_reason=protocol_complete$/.test(body) ?
        { label: '流式完成', tone: 'request-end' } : null;
    const tone = key === 'success' && attempt ? 'attempt-success' : key === 'success' && event === 'SUMMARY' ? 'route-success' : key;
    const description = attempt ? '本账号的一次上游调用结果；流式调用包含读取、转换和向客户端写出。仅计一次上游尝试。' :
      event === 'SUMMARY' ? '整个路由的汇总结果，包含此前的重试、退避和换号；不是另一次上游尝试。请求处理总耗时见 ACCESS。' :
      event === 'STREAM' ? '本次流式转发的协议终止状态；完成表示已写出结束标记，不代表客户端应用已经消费完响应，也不另计一次成功。' : '';
    const timingDescription = {
      PROVIDER: '本次账号尝试开始到转发返回，包含本次流式传输，不含此前尝试和两次尝试之间的退避。',
      SUMMARY: '从路由记录器创建到输出汇总，包含重试、退避及换号，不是最后一个账号的单次耗时。',
      STREAM: '从代理接收客户端请求到本次流式转发结束，不是纯传输时间。',
      ACCESS: '从代理接收客户端请求到请求处理结束，包含请求解析、路由、重试及收尾。',
      FIRST_RESPONSE: '从代理接收客户端请求到首次写出 HTTP 2xx 响应正文，不代表完整响应已结束。',
      TTFB: '从本次上游 HTTP 调用开始到收到响应头，不包含后续响应正文传输。'
    }[event] || '';
    return { id, raw, body, time: match ? match[1].slice(11) : '—', requestId: match && match[2] !== '-' ? match[2] : '',
      event, provider, providerRole, priority, status, key, label, tone, description, timingDescription, level, detail, milestone, attemptNumber: attempt?.[2] || null,
      retryGapMs, timingLabel, timingMs: event === 'INBOUND' ? 0 : timingLabel ? durationMs(elapsed[2]) : null };
  }
  function logProviderIndex(providers, attempts) {
    const models = new Map(providers.map(p => [p.name, p.model_id]));
    const requestModels = new Map();
    for (const attempt of attempts) {
      if (!attempt.request_id || !attempt.provider || !attempt.model_id) continue;
      const entries = requestModels.get(attempt.request_id) || new Map();
      entries.set(attempt.provider, attempt.model_id);
      requestModels.set(attempt.request_id, entries);
    }
    return log => requestModels.get(log.requestId)?.get(log.provider) || models.get(log.provider) || '';
  }
  function logPriorityIndex(logs, attempts = []) {
    const byRequest = new Map();
    const remember = (requestId, provider, priority) => {
      if (!requestId || !provider || !Number.isInteger(priority) || priority <= 0) return;
      if (!byRequest.has(requestId)) byRequest.set(requestId, new Map());
      byRequest.get(requestId).set(provider, priority);
    };
    for (const attempt of attempts) remember(attempt.request_id, attempt.provider, Number(attempt.priority));
    for (const log of logs) remember(log.requestId, log.provider, log.priority);
    return log => log.priority > 0 ? log.priority : byRequest.get(log.requestId)?.get(log.provider) || 0;
  }
  function filterLogs(logs, filter, modelFor = () => '', priorityFor = log => log.priority) {
    const query = String(filter.query || '').trim().toLowerCase();
    return logs.filter(log => {
      const model = modelFor(log);
      return (!query || log.raw.toLowerCase().includes(query) || model.toLowerCase().includes(query)) &&
        (!filter.priority || priorityFor(log) === Number(filter.priority)) && (!filter.result || log.key === filter.result) &&
        (!filter.model || (model ? model === filter.model : log.raw.includes(filter.model))) &&
        (!filter.level || (filter.level === 'warn+' ? ['warn', 'error'].includes(log.level) : log.level === filter.level));
    });
  }
  class LogBuffer {
    constructor(limit = LOG_LIMIT) { this.limit = Math.max(1, count(limit)); this.rows = []; this.dropped = 0; this.sequence = 0; }
    push(raw) {
      if (!String(raw || '').trim()) return null;
      const row = parseLog(raw, ++this.sequence);
      this.rows.push(row);
      if (this.rows.length > this.limit) { const excess = this.rows.length - this.limit; this.rows.splice(0, excess); this.dropped += excess; }
      return row;
    }
    clear() { this.rows.length = 0; this.dropped = 0; }
  }
  function validateSnapshot(data) {
    if (!data || typeof data !== 'object') throw new Error('统计响应不是有效对象');
    for (const key of ['total_client_req', 'total_req', 'total_success', 'total_fail', 'total_canceled']) {
      if (typeof data[key] !== 'number' || !Number.isFinite(data[key]) || data[key] < 0) throw new Error('统计字段无效：' + key);
    }
    // Go encodes its empty recent-attempt slice as null after startup/reset.
    let normalized = data.recent_attempts === null ? { ...data, recent_attempts: [] } : data;
    if (normalized.recent_requests === null) normalized = { ...normalized, recent_requests: [] };
    if (normalized.recent_requests !== undefined && !Array.isArray(normalized.recent_requests)) throw new Error('请求计时字段无效');
    if (!Array.isArray(normalized.providers) || !Array.isArray(normalized.recent_attempts) || !Array.isArray(normalized.curves) || !Array.isArray(normalized.latency_curve)) throw new Error('统计样本字段缺失');
    return normalized;
  }
  return { ATTEMPT_LIMIT, LOG_LIMIT, LOG_VIEW_LIMIT, PAGE_SIZE, number, count, ratio, formatCount, formatRate,
    formatDuration, formatLogDuration, formatUptime, timestamp, priorityColor, activePriorities, chartSeries, modelLatencySeries, chartSelection, rateAxis, quantile, attemptOutcome, providerOutcome, providerLabel,
    providerStreak, providerMatchesView, filterProviders, sortProviders, groupRequests, filterRequests, parseLog, logProviderIndex, logPriorityIndex, filterLogs, LogBuffer, validateSnapshot };
});
