const statusEl = document.getElementById('status');
const toggleEl = document.getElementById('toggle');
const totalReqEl = document.getElementById('totalReq');
const totalSuccessEl = document.getElementById('totalSuccess');
const totalFailEl = document.getElementById('totalFail');
const totalRateEl = document.getElementById('totalRate');
const statsBody = document.getElementById('stats-body');
const logContainer = document.getElementById('log-container');
const uptimeEl = document.getElementById('uptime');

function colorClass(value) {
  if (value >= 90) return 'rate-high';
  if (value >= 50) return 'rate-mid';
  return 'rate-low';
}

function fetchStats() {
  fetch('/api/stats')
    .then(r => r.json())
    .then(data => {
      totalReqEl.textContent = data.total_req;
      totalSuccessEl.textContent = data.total_success;
      totalFailEl.textContent = data.total_fail;
      totalRateEl.textContent = data.total_req > 0 ? data.total_rate.toFixed(1) + '%' : '—';
      totalRateEl.className = data.total_req > 0 ? colorClass(data.total_rate) : '';

      statusEl.textContent = data.running ? '● 运行中' : '● 已停用';
      statusEl.className = 'status ' + (data.running ? 'running' : 'stopped');
      toggleEl.checked = data.running;
      uptimeEl.textContent = '运行 ' + data.uptime;

      data.providers.sort((a, b) => a.priority - b.priority || a.name.localeCompare(b.name));
      statsBody.innerHTML = data.providers.map(p => `
        <tr>
          <td class="p${p.priority}">P${p.priority}</td>
          <td>${p.name}</td>
          <td style="color:#8b949e">${p.model_id}</td>
          <td>${p.total}</td>
          <td style="color:#3fb950">${p.success}</td>
          <td style="color:#f85149">${p.fail}</td>
          <td class="${p.total > 0 ? colorClass(p.rate) : ''}">${p.total > 0 ? p.rate.toFixed(1) + '%' : '—'}</td>
        </tr>
      `).join('');
    })
    .catch(() => {});
}

function toggleProxy() {
  const running = toggleEl.checked;
  fetch('/api/control', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ running })
  });
}

function clearLog() {
  logContainer.innerHTML = '';
}

function logLineClass(line) {
  if (line.includes('| OK |')) return 'ok';
  if (line.includes('| FAIL |')) return 'fail';
  if (line.includes('CB') || line.includes('QUEUE')) return 'info';
  if (line.includes('SUMMARY') && line.includes('FAIL')) return 'fail';
  if (line.includes('SUMMARY')) return 'muted';
  if (line.includes('REQUEST')) return 'info';
  return '';
}

// SSE log stream
const evtSource = new EventSource('/api/logs');
evtSource.onmessage = function(e) {
  const line = e.data;
  if (!line.trim()) return;
  const div = document.createElement('div');
  div.className = 'log-line ' + logLineClass(line);
  div.textContent = line;
  logContainer.appendChild(div);
  logContainer.scrollTop = logContainer.scrollHeight;
  while (logContainer.children.length > 500) {
    logContainer.removeChild(logContainer.firstChild);
  }
};

evtSource.onerror = function() {};

// Poll stats every 2s
fetchStats();
setInterval(fetchStats, 2000);