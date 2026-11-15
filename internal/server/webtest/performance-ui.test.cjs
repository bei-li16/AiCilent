const { test } = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');

test('dashboard handles log bursts, stable polls, priorities and responsive views', async () => {
  const { chromium } = require(process.env.AI_PROXY_PLAYWRIGHT || 'playwright');
  const output = process.env.AI_PROXY_UI_OUTPUT || path.resolve('artifacts/dashboard-performance');
  fs.mkdirSync(output, { recursive: true });
  const provider = priority => ({ name: 'mock-P' + priority, model_id: 'model-' + priority, priority,
    total: 10, success: 7, fail: 3, client_abort: 0, rate: 70, consecutive_fail: 0,
    latency_count: 10, latency_avg_ms: 30, last_latency_ms: 20, last_activity: new Date().toISOString() });
  const priorities = [1, 2, 3, 7];
  const data = { total_client_req: 20, total_client_completed: 20, total_client_success: 14,
    total_req: 40, total_success: 28, total_fail: 12, total_canceled: 0, total_rate: 70, running: true,
    uptime: '1h0m0s', trend_request_seq: 120, configured_priorities: priorities, providers: priorities.map(provider),
    recent_attempts: [], recent_requests: [], curves: [{ priority: 0, points: [50, 60, 70], requests: [1, 2, 3] }],
    latency_curve: [1, 3, 20, 9, 50], latency_curves: [
      { priority: 0, points: [1, 3, 20, 9, 50], requests: [10, 30, 80, 120, 60] },
      { priority: 1, points: [1, 3, 9], requests: [10, 30, 120] },
      { priority: 2, points: [20, 50], requests: [80, 60] }
    ],
    cb: priorities.map(priority => ({ priority, open: false, failures: 0, cooldown_sec: 60, cooldown_rem_sec: 0, skip_remaining: 10 })) };
  let reads = 0;
  const sockets = new Set();
  const server = http.createServer((request, response) => {
    const pathname = new URL(request.url, 'http://localhost').pathname;
    if (pathname === '/api/stats') { reads++; response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify(data)); return; }
    if (pathname === '/api/version') { response.end(JSON.stringify({ version: 'v-test', commit: 'fixture', build_date: '2026-11-15T00:00:00Z' })); return; }
    if (pathname === '/api/logs') {
      response.writeHead(200, { 'Content-Type': 'text/event-stream' });
      response.write('event: gap\ndata: 7\n\n');
      for (let i = 0; i < 2100; i++) {
        const p = priorities[i % priorities.length];
        response.write(`data: [2026-11-15 12:00:00.000] [${i.toString(16).padStart(32, '0')}] PROVIDER | [P${p}][1/4] mock-P${p} | OK | 1ms\n\n`);
      }
      return;
    }
    const file = pathname === '/' ? 'index.html' : pathname.slice(1);
    if (!['index.html', 'app.js', 'dashboard-core.js', 'style.css'].includes(file)) { response.writeHead(404); response.end(); return; }
    let body = fs.readFileSync(path.join(__dirname, '..', 'web', file), 'utf8');
    if (file === 'index.html') body = body.replaceAll('__DASHBOARD_VERSION__', 'v-test').replaceAll('__DASHBOARD_ASSET__', 'fixture');
    response.setHeader('Content-Type', file.endsWith('.js') ? 'application/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html');
    response.end(body);
  });
  server.on('connection', socket => { sockets.add(socket); socket.on('close', () => sockets.delete(socket)); });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
    const errors = []; page.on('pageerror', error => errors.push(error.message));
    await page.goto('http://127.0.0.1:' + server.address().port);
    await page.waitForFunction(() => document.getElementById('logBufferInfo').textContent.includes('1000/1000'));
    assert.match(await page.locator('#logBufferInfo').innerText(), /7/);
    assert.equal(await page.locator('#logEntries > *').count(), 200);
    await page.waitForTimeout(650);
    const legend = priority => page.locator('#latencyLegend button[data-priority="' + priority + '"]');
    const assertMean = async (priority, value, count) => {
      assert.match(await legend(priority).innerText(), new RegExp('平均 ' + value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
      assert.match(await legend(priority).getAttribute('aria-label'), new RegExp('平均成功耗时 .*，' + count + ' 次成功尝试'));
    };
    await assertMean(0, '16.6 s', 5);
    await assertMean(1, '4.33 s', 3);
    await assertMean(2, '35.0 s', 2);
    await assertMean(3, '—', 0);
    assert.match(await page.locator('#rateLegend button[data-priority="0"]').innerText(), /70\.0%/);
    assert.equal(await page.locator('#latencyP50').innerText(), '9.00 s');
    await legend(1).click();
    await assertMean(0, '16.6 s', 5);
    await assertMean(1, '4.33 s', 3);
    assert.equal(await page.locator('#latencyP50').innerText(), '9.00 s');
    await legend(0).click();
    await assertMean(0, '16.6 s', 5);
    await assertMean(2, '35.0 s', 2);
    assert.equal(await page.locator('#latencyP50').innerText(), '35.0 s');
    await page.locator('#latencyAll').click();
    await page.locator('#trendRange button[data-window="50"]').click();
    await assertMean(0, '14.5 s', 2);
    await assertMean(1, '9.00 s', 1);
    await assertMean(2, '20.0 s', 1);
    await page.locator('#trendRange button[data-window="100"]').click();
    await assertMean(0, '20.5 s', 4);
    await assertMean(1, '6.00 s', 2);
    await assertMean(2, '35.0 s', 2);
    assert.equal(await page.locator('#latencyP50').innerText(), '14.5 s');
    await page.locator('#trendRange button[data-window="0"]').click();
    await assertMean(0, '16.6 s', 5);
    await page.evaluate(() => {
      window.chartMutations = 0;
      new MutationObserver(list => { window.chartMutations += list.length; }).observe(document.getElementById('rateChart'), { subtree: true, childList: true, attributes: true });
    });
    await page.waitForTimeout(4500);
    assert.ok(reads >= 3);
    assert.equal(await page.evaluate(() => window.chartMutations), 0);
    await page.screenshot({ path: path.join(output, 'desktop.png'), fullPage: true });
    await page.locator('#pauseFollow').click();
    assert.match(await page.locator('#logFollowState').innerText(), /阅读/);
    await page.locator('#tabProviders').click();
    assert.ok((await page.locator('#providerBody').innerText()).includes('P7'));
    await page.locator('#tabRequests').click();
    await page.locator('#tabLogs').click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: path.join(output, 'mobile.png'), fullPage: true });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
    assert.ok(overflow <= 2, 'body overflows by ' + overflow);
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify({ reads, logCache: 1000, visibleLogs: 200, errors, overflow }, null, 2));
  } finally {
    await browser.close();
    for (const socket of sockets) socket.destroy();
    await new Promise(resolve => server.close(resolve));
  }
});
