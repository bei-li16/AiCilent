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
const liveBadge = document.getElementById('liveBadge');
const cbContainer = document.getElementById('cb-container');
const logFilter = document.getElementById('logFilter');
const pauseBtn = document.getElementById('pauseBtn');
const hitChart = document.getElementById('hitChart');
const chartEmpty = document.getElementById('chartEmpty');
const chartLegend = document.getElementById('chartLegend');

// Priority → curve color (matches the table's priority accents). 0 = overall.
const CURVE_COLORS = { 0: '#3fb950', 1: '#f0883e', 2: '#58a6ff', 3: '#8b949e' };
const CURVE_LABEL = { 0: '全部', 1: 'P1', 2: 'P2', 3: 'P3' };
function prioColor(p) { return CURVE_COLORS[p] || '#d2a8ff'; }
function prioLabel(p) { return CURVE_LABEL[p] || ('P' + p); }

let paused = false;
let fetchFails = 0;

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
      totalReqEl.textContent = data.total_req;
      totalClientEl.textContent = data.total_client_req;
      totalSuccessEl.textContent = data.total_success;
      totalFailEl.textContent = data.total_fail;
      totalRateEl.textContent = data.total_req > 0 ? data.total_rate.toFixed(1) + '%' : '—';
      totalRateEl.className = data.total_req > 0 ? 'card-value ' + colorClass(data.total_rate) : 'card-value';

      statusEl.textContent = data.running ? '● 运行中' : '● 已停用';
      statusEl.className = 'status ' + (data.running ? 'running' : 'stopped');
      // Only reflect server state; don't clobber an optimistic toggle mid-flight.
      if (!toggleBusy) toggleEl.checked = data.running;
      uptimeEl.textContent = '运行 ' + data.uptime;

      data.providers.sort((a, b) => a.priority - b.priority || a.name.localeCompare(b.name));
      statsBody.innerHTML = data.providers.map(p => {
        const rate = p.total > 0 ? p.rate.toFixed(1) + '%' : '—';
        const cf = p.consecutive_fail > 0 ? `<span class="cf-bad">${p.consecutive_fail}</span>` : '0';
        const err = p.last_err_type
          ? `<span class="err-type" title="${escapeAttr(p.last_err)}">${p.last_err_type}</span>`
          : '—';
        return `<tr>
          <td class="p${p.priority}">P${p.priority}</td>
          <td>${p.name}</td>
          <td class="muted">${p.model_id}</td>
          <td>${p.total}</td>
          <td class="ok-num">${p.success}</td>
          <td class="fail-num">${p.fail}</td>
          <td class="${p.total > 0 ? colorClass(p.rate) : ''}">${rate}</td>
          <td>${cf}</td>
          <td class="muted">${fmtLatency(p.latency_avg_ms)}</td>
          <td>${err}</td>
        </tr>`;
      }).join('');

      renderCB(data.cb || []);
      renderChart(data.curves || []);
    })
    .catch(() => {
      fetchFails++;
      if (fetchFails >= 2) {
        liveBadge.textContent = '● 统计中断';
        liveBadge.className = 'live-badge offline';
      }
    });
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

// renderChart draws the rolling-50 hit-rate curves as right-aligned SVG
// polylines. Y axis 0–100%, X axis = most recent point at the right edge so
// curves with different lengths align in time.
function renderChart(curves) {
  const W = 1000, H = 200, padTop = 8, padBottom = 8, padLeft = 4, padRight = 4;
  const plotH = H - padTop - padBottom;
  const plotW = W - padLeft - padRight;

  let maxLen = 1;
  let anyData = false;
  for (const c of curves) {
    if (c.points && c.points.length) { anyData = true; maxLen = Math.max(maxLen, c.points.length); }
  }
  if (!anyData) {
    hitChart.innerHTML = '';
    chartEmpty.style.display = '';
    chartLegend.innerHTML = '';
    return;
  }
  chartEmpty.style.display = 'none';

  // Right-align: last point of each series lands at x = plotW (right edge).
  function xy(series) {
    const n = series.length;
    const start = maxLen - n; // index offset so the latest aligns to the right
    const pts = [];
    for (let j = 0; j < n; j++) {
      const xi = padLeft + plotW * (start + j) / (maxLen - 1 || 1);
      const yi = padTop + plotH - (series[j] / 100) * plotH;
      pts.push(xi.toFixed(1) + ',' + yi.toFixed(1));
    }
    return pts.join(' ');
  }

  let svg = '';
  // Gridlines + Y labels at 0/50/100.
  for (const v of [0, 50, 100]) {
    const y = padTop + plotH - (v / 100) * plotH;
    svg += `<line x1="${padLeft}" y1="${y}" x2="${W - padRight}" y2="${y}" stroke="#21262d" stroke-width="1"/>`;
    svg += `<text x="${padLeft + 2}" y="${y - 2}" fill="#8b949e" font-size="9">${v}%</text>`;
  }
  // Curves.
  for (const c of curves) {
    if (!c.points || !c.points.length) continue;
    const col = prioColor(c.priority);
    svg += `<polyline points="${xy(c.points)}" fill="none" stroke="${col}" stroke-width="1.6" stroke-linejoin="round" stroke-linecap="round"/>`;
  }
  hitChart.innerHTML = svg;

  // Legend.
  chartLegend.innerHTML = curves.filter(c => c.points && c.points.length).map(c =>
    `<span class="legend-item"><span class="legend-dot" style="background:${prioColor(c.priority)}"></span>${prioLabel(c.priority)}</span>`
  ).join('');
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
setInterval(fetchStats, 2000);

toggleEl.addEventListener('change', toggleProxy);
