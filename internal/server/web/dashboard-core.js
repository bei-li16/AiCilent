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
  function chartSeries(data, kind) {
    const source = kind === 'rate' ? data.curves : Array.isArray(data.latency_curves) ? data.latency_curves : [{ priority: 0, points: data.latency_curve }];
    const byPriority = new Map((source || []).filter(c => c && Array.isArray(c.points)).map(c => [Number(c.priority), c.points]));
    return [0, ...activePriorities(data)].map(priority => ({ priority, label: priority === 0 ? '汇总' : 'P' + priority,
      color: priorityColor(priority), points: (byPriority.get(priority) || []).filter(v => Number.isFinite(v) && v >= 0).slice(-240).map(v => kind === 'rate' ? Math.min(100, v) : v) }));
  }
  // null means the first visit: show every curve. An explicit [] means the
  // user hid every curve and must survive polling/reloads unchanged.
  function chartSelection(available, saved) {
    return new Set(Array.isArray(saved) ? saved.map(Number).filter(p => available.includes(p)) : available);
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
  function groupRequests(attempts) {
    const groups = new Map();
    (attempts || []).slice(-ATTEMPT_LIMIT).forEach((a, i) => {
      const id = a.request_id || 'missing-id-' + i;
      if (!groups.has(id)) groups.set(id, { id, missingId: !a.request_id, attempts: [], latest: 0, duration: 0, providers: [], models: [], priorities: [] });
      const g = groups.get(id);
      g.attempts.push(a); g.latest = Math.max(g.latest, timestamp(a.timestamp)); g.duration += number(a.latency_ms);
      if (!g.providers.includes(a.provider)) g.providers.push(a.provider);
      if (!g.models.includes(a.model_id)) g.models.push(a.model_id);
      if (!g.priorities.includes(a.priority)) g.priorities.push(a.priority);
    });
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
        { key: 'canceled', label: '观察到取消', tone: 'canceled' } : { key: 'unknown', label: '最终结果未知', tone: 'neutral' };
      return g;
    }).sort((a, b) => b.latest - a.latest || a.id.localeCompare(b.id));
  }
  function filterRequests(groups, filter) {
    const q = String(filter.query || '').trim().toLowerCase();
    return groups.filter(g => (!q || [g.id, ...g.providers, ...g.models].join(' ').toLowerCase().includes(q)) &&
      (!filter.priority || g.priorities.some(p => Number(p) === Number(filter.priority))) &&
      (!filter.model || g.models.includes(filter.model)) &&
      (!filter.result || g.attempts.some(a => attemptOutcome(a).key === filter.result)));
  }
  function parseLog(raw, id) {
    raw = String(raw || '');
    if (raw.length > 16384) raw = raw.slice(0, 16384) + '\n[当前行超过 16 KiB，显示已截断]';
    const match = raw.match(/^\[([^\]]+)\]\s*\[([^\]]+)\]\s*(\w+)\s*\|\s*(.*)$/s);
    const body = match ? match[4] : raw;
    const event = match ? match[3].toUpperCase() : 'TEXT';
    const resultBody = ['INBOUND', 'TEXT'].includes(event) ? '' : body;
    const attempt = resultBody.match(/^\[P(\d+)\]\[(\d+\/\d+)\]\s+(.+?)\s*\|\s*(.*)$/s);
    const next = event === 'ROUTER' && body.match(/^degrade to next provider\s*\|\s*(.+?)\s+\[P(\d+)\]\s*\|\s*reason=(.*)$/s);
    const skipped = event === 'QUEUE' && body.match(/^\[P(\d+)\]\s*\|\s*provider (.+?) skipping \((.*)\)\s*\|\s*rr=/s);
    const ttfb = event === 'TTFB' && body.match(/^(.+?)\s*\[P(\d+)\]\s*\|/);
    const retry = event === 'ROUTER' && body.match(/^non-retryable status=\d+\s*\|\s*skip retry\s*\|\s*(.+?)\s+\[P(\d+)\]$/);
    const summary = event === 'SUMMARY' && body.match(/\|\s*provider=([^|]*?)\s*\|\s*rr=/);
    const provider = (attempt?.[3] || next?.[1] || skipped?.[2] || ttfb?.[1] || retry?.[1] || summary?.[1] || '').trim();
    const priority = Number(attempt?.[1] || next?.[2] || skipped?.[1] || ttfb?.[2] || retry?.[2] ||
      (resultBody.match(/\[P(\d+)\]/) || [])[1] || 0);
    const providerRole = next ? '切换目标' : skipped ? '跳过对象' : '';
    const detail = attempt ? '尝试 ' + attempt[2] + ' · ' + attempt[4] :
      next ? '切换原因：' + next[3] : skipped ? '跳过原因：' + skipped[3] : body;
    const status = Number((resultBody.match(/status\s*[= ]\s*(\d{3})/) || [])[1] || (resultBody.match(/\|\s+(\d{3})\s+\|/) || [])[1] || 0);
    let key = 'neutral', label = '信息';
    if (/\bCANCELED\b/.test(resultBody)) { key = 'canceled'; label = '取消'; }
    else if (status === 429) { key = 'limited'; label = next ? '切换 · 429' : skipped ? '跳过 · 429' : '限流 429'; }
    else if (/\|\s*FAIL\s*\||result=FAIL|stream interrupted/i.test(resultBody) || status >= 400) { key = 'failed'; label = status ? '失败 ' + status : '失败'; }
    else if (/\|\s*OK\s*\||result=SUCCESS/.test(resultBody)) { key = 'success'; label = '成功'; }
    // The backend emits event categories rather than a severity field. Keep
    // user-supplied request/body text from creating false ERROR/WARN levels.
    const explicit = ['DEBUG', 'INFO', 'WARN', 'WARNING', 'ERROR', 'FATAL'].includes(event) ? event :
      (!match ? (raw.match(/^\s*(?:\d{4}[-/]\d{2}[-/]\d{2}\s+\d{2}:\d{2}:\d{2}\s+)?\[?(DEBUG|INFO|WARN(?:ING)?|ERROR|FATAL)\]?\b/) || [])[1] : '');
    let level = 'info';
    if (explicit) level = /ERROR|FATAL/.test(explicit) ? 'error' : /WARN/.test(explicit) ? 'warn' : explicit.toLowerCase();
    else if (!['INBOUND', 'TEXT'].includes(event) && key === 'failed') level = 'error';
    else if (!['INBOUND', 'TEXT'].includes(event) && key === 'limited') level = 'warn';
    else if (event === 'CB' && /\b(?:OPEN|opened|tripped)\b/.test(body)) level = 'warn';
    else if (['ROUTER', 'QUEUE', 'TTFB', 'STREAM'].includes(event) && key !== 'canceled') level = 'debug';
    return { id, raw, body, time: match ? match[1].slice(11) : '—', requestId: match && match[2] !== '-' ? match[2] : '',
      event, provider, providerRole, priority, status, key, label, level, detail };
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
  function filterLogs(logs, filter, modelFor = () => '') {
    const query = String(filter.query || '').trim().toLowerCase();
    return logs.filter(log => {
      const model = modelFor(log);
      return (!query || log.raw.toLowerCase().includes(query) || model.toLowerCase().includes(query)) &&
        (!filter.priority || log.priority === Number(filter.priority)) && (!filter.result || log.key === filter.result) &&
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
    const normalized = data.recent_attempts === null ? { ...data, recent_attempts: [] } : data;
    if (!Array.isArray(normalized.providers) || !Array.isArray(normalized.recent_attempts) || !Array.isArray(normalized.curves) || !Array.isArray(normalized.latency_curve)) throw new Error('统计样本字段缺失');
    return normalized;
  }
  return { ATTEMPT_LIMIT, LOG_LIMIT, LOG_VIEW_LIMIT, PAGE_SIZE, number, count, ratio, formatCount, formatRate,
    formatDuration, formatUptime, timestamp, priorityColor, activePriorities, chartSeries, chartSelection, quantile, attemptOutcome, providerOutcome, providerLabel,
    providerStreak, providerMatchesView, filterProviders, sortProviders, groupRequests, filterRequests, parseLog, logProviderIndex, filterLogs, LogBuffer, validateSnapshot };
});
