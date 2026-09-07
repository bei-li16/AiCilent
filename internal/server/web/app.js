const statusEl = document.getElementById('status');
const toggleEl = document.getElementById('toggle');
const totalReqEl = document.getElementById('totalReq');
const totalClientEl = document.getElementById('totalClient');
const totalSuccessEl = document.getElementById('totalSuccess');
const totalFailEl = document.getElementById('totalFail');
const totalRateEl = document.getElementById('totalRate');
const statsBody = document.getElementById('stats-body');
const logContainer = document.getElementById('log-container');
const uptimeEl = document.getElementById('uptime');
const buildInfoEl = document.getElementById('buildInfo');
const liveBadge = document.getElementById('liveBadge');
const cbContainer = document.getElementById('cb-container');
const logFilter = document.getElementById('logFilter');
const pauseBtn = document.getElementById('pauseBtn');
const hitChart = document.getElementById('hitChart');
const chartEmpty = document.getElementById('chartEmpty');
const chartLegend = document.getElementById('chartLegend');
const traceBody = document.getElementById('trace-body');
const RECENT_ATTEMPT_LIMIT = 200;

// Priority → curve color (matches the table's priority accents). 0 = overall.
const CURVE_COLORS = { 0: '#3fb950', 1: '#f0883e', 2: '#58a6ff', 3: '#8b949e' };
const CURVE_LABEL = { 0: '全部', 1: 'P1', 2: 'P2', 3: 'P3' };
function prioColor(p) { return CURVE_COLORS[p] || '#d2a8ff'; }
function prioLabel(p) { return CURVE_LABEL[p] || ('P' + p); }

let paused = false;
let fetchFails = 0;
let statsBaseline = null;
let statsBaselineAt = 0;
let lastStatsData = null;
const statsClearBtn = document.getElementById('statsClearBtn');

function colorClass(value) {
  if (value >= 90) return 'rate-high';
  if (value >= 50) return 'rate-mid';
  return 'rate-low';
}

function fmtLatency(ms) {
  if (!ms || ms <= 0) return '—';
  if (ms < 1000) return ms.toFixed(0) + 'ms';
  return (ms / 1000).toFixed(2) + 's';
}

function fetchStats() {
  fetch('/api/stats')
    .then(r => r.json())
    .then(data => {
      fetchFails = 0;
      lastStatsData = data;
      renderStats(data);
    })
    .catch(() => {
      fetchFails++;
      if (fetchFails >= 2) {
        liveBadge.textContent = '● 统计中断';
        liveBadge.className = 'live-badge offline';
      }
    });
}

function fetchVersion() {
  fetch('/api/version')
    .then(r => r.json())
    .then(data => {
      if (!buildInfoEl) return;
      const version = data.version || 'dev';
      const buildDate = data.build_date && data.build_date !== 'unknown' ? data.build_date : '编译时间未知';
      buildInfoEl.textContent = `${version} · ${buildDate}`;
    })
    .catch(() => {
      if (buildInfoEl) buildInfoEl.textContent = '版本信息不可用';
    });
}

function clearStats() {
  if (!confirm('确认清零所有统计数据？该操作会清空服务端统计，不可恢复。')) return;
  fetch('/api/stats/reset', { method: 'POST' })
    .then(r => {
      if (!r.ok) throw new Error('reset denied');
      statsBaseline = null;
      statsBaselineAt = 0;
      statsClearBtn.textContent = '清零';
      fetchStats();
    })
    .catch(() => alert('清零失败：仅允许本机访问（或开启 control_allow_remote）'));
}

function renderStats(data) {
  const base = statsBaseline;
  const apply = base !== null;
  const delta = (current, previous) => Math.max(0, current - previous);

  const dReq = apply ? delta(data.total_req, base.total_req) : data.total_req;
  const dClient = apply ? delta(data.total_client_req, base.total_client_req) : data.total_client_req;
  const dSuccess = apply ? delta(data.total_success, base.total_success) : data.total_success;
  const dFail = apply ? delta(data.total_fail, base.total_fail) : data.total_fail;
  const dRate = dReq > 0 ? (dSuccess / dReq * 100) : 0;

  totalReqEl.textContent = dReq;
  totalClientEl.textContent = dClient;
  totalSuccessEl.textContent = dSuccess;
  totalFailEl.textContent = dFail;
  totalRateEl.textContent = dReq > 0 ? dRate.toFixed(1) + '%' : '—';
  totalRateEl.className = dReq > 0 ? 'card-value ' + colorClass(dRate) : 'card-value';

  statusEl.textContent = data.running ? '● 运行中' : '● 已停用';
  statusEl.className = 'status ' + (data.running ? 'running' : 'stopped');
  if (!toggleBusy) toggleEl.checked = data.running;
  uptimeEl.textContent = '运行 ' + data.uptime;

  // 优先级升序；同优先级内最近活跃的排最前，其次按名称稳定排序。
  const lastAct = p => p.last_activity ? Date.parse(p.last_activity) : 0;
  data.providers.sort((a, b) =>
    a.priority - b.priority || lastAct(b) - lastAct(a) || a.name.localeCompare(b.name));
  statsBody.innerHTML = data.providers.map(p => {
    const bp = (apply && base.providers[p.name]) || { total: 0, success: 0, fail: 0 };
    const dTotal = apply ? delta(p.total, bp.total) : p.total;
    const dSucc = apply ? delta(p.success, bp.success) : p.success;
    const dFl = apply ? delta(p.fail, bp.fail) : p.fail;
    const rate = dTotal > 0 ? (dSucc / dTotal * 100).toFixed(1) + '%' : '—';
    const cf = p.consecutive_fail > 0 ? `<span class="cf-bad">${p.consecutive_fail}</span>` : '0';
    const err = p.last_err_type
      ? `<span class="err-type" title="${escapeAttr(p.last_err)}">${p.last_err_type}</span>`
      : '—';
    return `<tr>
      <td class="p${p.priority}">P${p.priority}</td>
      <td>${escapeText(p.name)}</td>
      <td class="muted">${escapeText(p.model_id)}</td>
      <td>${dTotal}</td>
      <td class="ok-num">${dSucc}</td>
      <td class="fail-num">${dFl}</td>
      <td class="${dTotal > 0 ? colorClass(dSucc / dTotal * 100) : ''}">${rate}</td>
      <td>${cf}</td>
      <td class="muted">${fmtLatency(p.latency_avg_ms)}</td>
      <td>${err}</td>
    </tr>`;
  }).join('');

  renderCB(data.cb || []);
  renderChart(data.curves || [], data.latency_curve || []);
  renderRecentAttempts(data.recent_attempts || []);
}

function escapeText(s) {
  return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function renderRecentAttempts(attempts) {
  if (!traceBody) return;
  const recent = attempts.filter(a => !statsBaselineAt || !a.timestamp || Date.parse(a.timestamp) >= statsBaselineAt).slice(-RECENT_ATTEMPT_LIMIT).reverse();
  traceBody.innerHTML = recent.map(a => {
    const ok = a.success;
    const timestamp = a.timestamp ? new Date(a.timestamp).toLocaleTimeString() : '—';
    return `<tr>
      <td class="trace-id" title="${escapeText(a.request_id)}">${escapeText(String(a.request_id).slice(0, 12))}</td>
      <td>${escapeText(a.provider)}</td>
      <td class="muted">${escapeText(a.model_id)}</td>
      <td>P${a.priority}</td>
      <td class="${ok ? 'ok-num' : 'fail-num'}">${ok ? '成功' : '失败'}</td>
      <td>${a.status_code || '—'}</td>
      <td class="muted">${fmtLatency(a.latency_ms)}</td>
      <td class="muted">${escapeText(timestamp)}</td>
    </tr>`;
  }).join('');
}

function renderCB(cb) {
  if (!cb || cb.length === 0) {
    cbContainer.innerHTML = '<span class="muted">暂无熔断记录</span>';
    return;
  }
  cbContainer.innerHTML = cb.map(c => {
    const state = c.open ? 'open' : 'closed';
    const label = c.open
      ? `熔断中 · 失败 ${c.failures} · 剩余 ${c.cooldown_rem_sec}s`
      : `正常 · 累计失败 ${c.failures}`;
    return `<div class="cb-bar ${state}">
      <span class="cb-prio">P${c.priority}</span>
      <span class="cb-label">${label}</span>
    </div>`;
  }).join('');
}

function escapeAttr(s) {
  return String(s).replace(/"/g, '&quot;').replace(/</g, '&lt;');
}

// renderChart draws the rolling-50 hit-rate curves plus a hit-latency curve
// (seconds, right axis). All series are right-aligned: the most recent point
// lands at the right edge so curves with different lengths align in time.
//   - hit-rate: left axis 0–100%
//   - latency:  right axis 0–maxLat seconds (only successful attempts)
function renderChart(curves, latencyCurve) {
  const W = 1000, H = 200, padTop = 8, padBottom = 8, padLeft = 4, padRight = 36;
  const plotH = H - padTop - padBottom;
  const plotW = W - padLeft - padRight;

  let maxLen = 1;
  let anyData = false;
  for (const c of curves) {
    if (c.points && c.points.length) { anyData = true; maxLen = Math.max(maxLen, c.points.length); }
  }
  let maxLat = 0;
  if (latencyCurve && latencyCurve.length) {
    anyData = true;
    maxLen = Math.max(maxLen, latencyCurve.length);
    for (const v of latencyCurve) { if (v > maxLat) maxLat = v; }
  }
  if (!anyData) {
    hitChart.innerHTML = '';
    chartEmpty.style.display = '';
    chartLegend.innerHTML = '';
    return;
  }
  chartEmpty.style.display = 'none';

  // Right-align a series of length n across the full width; map each value
  // to a y coordinate via valY(v).
  function xy(series, valY) {
    const n = series.length;
    const start = maxLen - n; // offset so the latest point aligns to the right edge
    const pts = [];
    for (let j = 0; j < n; j++) {
      const xi = padLeft + plotW * (start + j) / (maxLen - 1 || 1);
      const yi = valY(series[j]);
      pts.push(xi.toFixed(1) + ',' + yi.toFixed(1));
    }
    return pts.join(' ');
  }
  const rateY = v => padTop + plotH - (v / 100) * plotH;
  const latY = v => padTop + plotH - (maxLat > 0 ? (v / maxLat) * plotH : 0);

  let svg = '';
  // Left-axis gridlines + labels (hit-rate %).
  for (const v of [0, 50, 100]) {
    const y = rateY(v);
    svg += `<line x1="${padLeft}" y1="${y}" x2="${W - padRight}" y2="${y}" stroke="#21262d" stroke-width="1"/>`;
    svg += `<text x="${padLeft + 2}" y="${y - 2}" fill="#8b949e" font-size="9">${v}%</text>`;
  }
  // Right-axis labels (latency seconds): 0 / mid / max.
  if (maxLat > 0) {
    const latColor = '#d2a8ff';
    for (const v of [0, maxLat / 2, maxLat]) {
      const y = latY(v);
      svg += `<text x="${W - padRight + 2}" y="${y + 3}" fill="${latColor}" font-size="9" text-anchor="start">${fmtSec(v)}</text>`;
    }
    svg += `<polyline points="${xy(latencyCurve, latY)}" fill="none" stroke="${latColor}" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round" stroke-dasharray="3 2"/>`;
  }
  // Hit-rate curves.
  for (const c of curves) {
    if (!c.points || !c.points.length) continue;
    const col = prioColor(c.priority);
    svg += `<polyline points="${xy(c.points, rateY)}" fill="none" stroke="${col}" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round"/>`;
  }
  hitChart.innerHTML = svg;

  // Legend: hit-rate curves (label + latest success rate) + latency.
  let legend = curves.filter(c => c.points && c.points.length).map(c => {
    const latest = c.points[c.points.length - 1];
    return `<span class="legend-item"><span class="legend-dot" style="background:${prioColor(c.priority)}"></span>${prioLabel(c.priority)} ${latest.toFixed(1)}%</span>`;
  }).join('');
  if (maxLat > 0) {
    legend += `<span class="legend-item"><span class="legend-dot dash" style="background:#d2a8ff"></span>命中耗时(s)</span>`;
  }
  chartLegend.innerHTML = legend;
}

function fmtSec(v) {
  if (v <= 0) return '0s';
  if (v < 10) return v.toFixed(1) + 's';
  return v.toFixed(0) + 's';
}

let toggleBusy = false;
function toggleProxy() {
  if (toggleBusy) return;
  toggleBusy = true;
  const running = toggleEl.checked;
  // Optimistic UI: reflect immediately.
  statusEl.textContent = running ? '● 运行中' : '● 已停用';
  statusEl.className = 'status ' + (running ? 'running' : 'stopped');
  fetch('/api/control', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ running })
  })
    .then(r => r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)))
    .then(() => { /* server confirmed */ })
    .catch(() => {
      // Revert on failure.
      toggleEl.checked = !running;
      statusEl.textContent = (!running ? '● 运行中' : '● 已停用');
      statusEl.className = 'status ' + (!running ? 'running' : 'stopped');
      flashToggle('操作失败（已回退）');
    })
    .finally(() => { toggleBusy = false; });
}

let flashTimer = null;
function flashToggle(msg) {
  liveBadge.textContent = msg;
  liveBadge.className = 'live-badge offline';
  if (flashTimer) clearTimeout(flashTimer);
  flashTimer = setTimeout(() => {
    liveBadge.textContent = evtSource.readyState === 1 ? '● 实时' : '● 重连中';
    liveBadge.className = 'live-badge ' + (evtSource.readyState === 1 ? '' : 'reconnect');
  }, 2000);
}

function clearLog() {
  logContainer.innerHTML = '';
}

function toggleSection(id) {
  document.getElementById(id).classList.toggle('collapsed');
}

function togglePause() {
  paused = !paused;
  pauseBtn.textContent = paused ? '继续' : '暂停';
}

function logLineClass(line) {
  if (line.includes('| OK |')) return 'ok';
  if (line.includes('| FAIL |')) return 'fail';
  if (line.includes('SUMMARY') && line.includes('FAIL')) return 'fail';
  if (line.includes('SUMMARY')) return 'muted';
  if (line.includes('INBOUND')) return 'info';
  if (line.includes('ACCESS')) return 'access';
  if (line.includes('CB') || line.includes('QUEUE') || line.includes('ROUTER')) return 'info';
  if (line.includes('REQUEST')) return 'info';
  return '';
}

function applyFilter() {
  const q = logFilter.value.trim().toLowerCase();
  for (const div of logContainer.children) {
    if (!q) { div.style.display = ''; continue; }
    div.style.display = div.textContent.toLowerCase().includes(q) ? '' : 'none';
  }
}
logFilter.addEventListener('input', applyFilter);

// SSE log stream
const evtSource = new EventSource('/api/logs');
evtSource.onopen = function() {
  liveBadge.textContent = '● 实时';
  liveBadge.className = 'live-badge';
};
evtSource.onmessage = function(e) {
  const line = e.data;
  if (!line.trim()) return;
  if (paused) return;
  const div = document.createElement('div');
  div.className = 'log-line ' + logLineClass(line);
  div.textContent = line;
  // Apply current filter so newly appended lines respect it.
  const q = logFilter.value.trim().toLowerCase();
  if (q && !line.toLowerCase().includes(q)) div.style.display = 'none';
  logContainer.appendChild(div);
  logContainer.scrollTop = logContainer.scrollHeight;
  while (logContainer.children.length > 500) {
    logContainer.removeChild(logContainer.firstChild);
  }
};
evtSource.onerror = function() {
  liveBadge.textContent = '● 重连中';
  liveBadge.className = 'live-badge reconnect';
};

// Poll stats every 2s
fetchStats();
fetchVersion();
setInterval(fetchStats, 2000);

toggleEl.addEventListener('change', toggleProxy);
