const { test } = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');

test('dashboard handles log bursts, stable polls, priorities and responsive views', async () => {
  const { chromium } = require(process.env.AI_PROXY_PLAYWRIGHT || 'playwright');
  const output = process.env.AI_PROXY_UI_OUTPUT || path.resolve('artifacts/dashboard-performance');
  fs.mkdirSync(output, { recursive: true });
  const provider = priority => ({ name: 'mock-P' + priority, model_id: 'model-' + (priority === 1 ? 10 : priority), priority,
    total: 10, success: 7, fail: 3, client_abort: 0, rate: 70, consecutive_fail: 0,
    latency_count: 10, latency_avg_ms: 30, last_latency_ms: 20,
    last_activity: priority === 7 ? '0001-01-01T00:00:00Z' : '2026-11-17T10:00:0' + priority + 'Z',
    last_err_type: priority === 3 ? 'upstream_error' : '' });
  const priorities = [1, 2, 3, 7];
  const data = { total_client_req: 20, total_client_completed: 20, total_client_success: 14,
    total_req: 40, total_success: 28, total_fail: 12, total_canceled: 0, total_rate: 70, running: true,
    uptime: '1h0m0s', trend_request_seq: 120, configured_priorities: priorities, providers: priorities.map(provider),
    recent_attempts: [], recent_requests: [], curves: [{ priority: 0, points: Array(100).fill(0), requests: Array(100).fill(120),
      successes: [...Array(10).fill(true), ...Array(40).fill(false), ...Array(30).fill(true), ...Array(20).fill(false)] }],
    latency_curve: [1, 3, 20, 9, 50], latency_curves: [
      { priority: 0, points: [1, 3, 20, 9, 50], requests: [10, 30, 80, 120, 60] },
      { priority: 1, points: [1, 3, 9], requests: [10, 30, 120] },
      { priority: 2, points: [20, 50], requests: [80, 60] }
    ],
    cb: priorities.map(priority => ({ priority, open: false, failures: 0, cooldown_sec: 60, cooldown_rem_sec: 0, skip_remaining: 10 })) };
  let reads = 0;
  let build = 'fixture', logGeneration = 'initial';
  let usageResets = 0;
  const logStreams = new Set();
  const sockets = new Set();
  const server = http.createServer((request, response) => {
    const pathname = new URL(request.url, 'http://localhost').pathname;
    if (pathname === '/api/stats') {
      reads++;
      response.writeHead(200, { 'Content-Type': 'application/json', 'X-Dashboard-Build': build });
      response.end(JSON.stringify(data));
      return;
    }
    if (pathname === '/api/version') { response.end(JSON.stringify({ version: 'v-test', commit: 'fixture', build_date: '2026-11-15T00:00:00Z' })); return; }
    if (pathname === '/api/usage/reset' && request.method === 'POST') {
      usageResets++;
      response.end(JSON.stringify({ reset: true }));
      return;
    }
    if (pathname === '/api/usage') {
      const sample = { input_tokens: 900, output_tokens: 100, requests: 2, cached_input_tokens: 450,
        cache_eligible_tokens: 900, first_ms: 1000, first_count: 2, decode_tokens: 100, decode_ms: 2000 };
      const models = { 'glm-5.3': { input_tokens: 720, output_tokens: 80 }, 'kimi-k3': { input_tokens: 180, output_tokens: 20 } };
      const hour = { 'glm-5.3': { input_tokens: 630, output_tokens: 70, cache_eligible_tokens: 630, cached_input_tokens: 315 },
        'kimi-k3': { input_tokens: 90, output_tokens: 10 } };
      response.end(JSON.stringify({ today: '2026-11-17', hour: 2, timezone: 'UTC+08:00',
        started_at: usageResets ? null : '2026-11-16T00:00:00+08:00', total: usageResets ? {} : sample,
        days: usageResets ? {} : { '2026-11-17': models }, hours: usageResets ? {} : { '2026-11-17T01': hour } }));
      return;
    }
    if (pathname === '/api/logs') {
      response.writeHead(200, { 'Content-Type': 'text/event-stream' });
      logStreams.add(response);
      response.on('close', () => logStreams.delete(response));
      response.write('retry: 100\nevent: log-generation\ndata: ' + logGeneration + '\n\n');
      if (logGeneration !== 'initial') {
        response.write('data: new generation\n\n');
        return;
      }
      response.write('event: gap\ndata: 7\n\n');
      for (let i = 0; i < 2100; i++) {
        const p = priorities[i % priorities.length];
        response.write(`data: [2026-11-15 12:00:00.000] [${i.toString(16).padStart(32, '0')}] PROVIDER | [P${p}][1/4] mock-P${p} | OK | 1ms\n\n`);
      }
      return;
    }
    const file = pathname === '/' ? 'index.html' : pathname.slice(1);
    if (!['index.html', 'app.js', 'dashboard-core.js', 'token-usage.js', 'style.css'].includes(file)) { response.writeHead(404); response.end(); return; }
    let body = fs.readFileSync(path.join(__dirname, '..', 'web', file), 'utf8');
    if (file === 'index.html') body = body.replaceAll('__DASHBOARD_VERSION__', 'v-test').replaceAll('__DASHBOARD_ASSET__', build);
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
    assert.equal(await page.locator('#workspaceTitle').innerText(), '工作区');
    assert.equal(await page.locator('[role="tab"][aria-selected="true"]').getAttribute('id'), 'tabUsage');
    assert.equal(await page.locator('#filterForm').isVisible(), false);
    await page.waitForFunction(() => document.getElementById('usageTotal').textContent === '1K');
    assert.equal(await page.locator('#usageCache').innerText(), '50.0%');
    const heatCells = page.locator('.usage-heat-cell[data-level]');
    assert.equal(await heatCells.count(), 52 * 7);
    assert.match(await heatCells.first().getAttribute('data-tooltip'), /^2025-11-24/);
    assert.match(await heatCells.last().getAttribute('data-tooltip'), /^2026-11-22 · 尚未到来$/);
    assert.equal(await page.locator('.usage-heat-cell.future').count(), 5);
    assert.equal(await page.locator('.usage-heat').evaluate(el => getComputedStyle(el).gridTemplateColumns.split(' ').length), 52);
    assert.equal(await page.locator('#paneUsage').evaluate(el => getComputedStyle(el).backgroundColor), 'rgb(17, 28, 44)');
    assert.equal(await page.locator('.usage-activity').evaluate(el => getComputedStyle(el).backgroundColor), 'rgb(26, 42, 64)');
    assert.equal(await page.locator('.usage-heat-cell.untracked').first().evaluate(el => getComputedStyle(el).backgroundColor), 'rgb(48, 48, 48)');
    assert.equal(await page.locator('.usage-heat-cell.future').first().evaluate(el => getComputedStyle(el).backgroundColor), 'rgb(39, 39, 39)');
    assert.match(await page.locator('#usageShare').innerText(), /glm-5\.3/);
    const tip = page.locator('#hoverTooltip');
    const arcs = page.locator('.usage-ring-segment');
    const ringPoint = await arcs.first().evaluate(el => {
      const { x, y } = new DOMPoint(80, 140).matrixTransform(el.getScreenCTM());
      return { x, y };
    });
    await page.mouse.move(ringPoint.x, ringPoint.y);
    assert.match(await tip.innerText(), /glm-5\.3\n800 Token · 80\.0%/);
    assert.equal(await tip.locator('.tooltip-swatch').evaluate(el => getComputedStyle(el).backgroundColor), 'rgb(181, 160, 255)');
    await page.waitForFunction(() => {
      const [first, second] = document.querySelectorAll('.usage-ring-segment');
      return getComputedStyle(first).strokeWidth === '22px' && Number(getComputedStyle(second).opacity) === .45;
    });
    const assertTrendTooltip = async (date, total, modelValue, otherValue) => {
      await page.locator(`#usageTrendPlot rect[data-tooltip^="${date}"]`).hover();
      const tooltip = await tip.innerText();
      assert.ok(tooltip.includes(date));
      assert.equal(await tip.locator('strong').innerText(), `总消耗 · ${total} Token`);
      assert.ok(tooltip.includes(`glm-5.3 · ${modelValue} Token`));
      assert.ok(tooltip.includes(`kimi-k3 · ${otherValue} Token`));
      assert.deepEqual(await tip.locator('.tooltip-swatch').evaluateAll(els => els.map(el => getComputedStyle(el).backgroundColor)),
        ['rgb(181, 160, 255)', 'rgb(84, 214, 187)']);
    };
    const focus = page.locator('#usageTrendPlot .usage-trend-focus');
    await assertTrendTooltip('2026-11-17 01:00', '800', 700, 100);
    assert.equal(await focus.locator('line').count(), 1);
    assert.deepEqual(await focus.locator('circle').evaluateAll(els => els.map(el => el.getAttribute('fill'))), ['#b5a0ff', '#54d6bb', null]);
    const oldFocusX = await focus.locator('line').getAttribute('x1');
    await page.locator('#usageTrendPlot rect[data-usage-sample="2"]').hover();
    assert.notEqual(await focus.locator('line').getAttribute('x1'), oldFocusX);
    assert.equal(await focus.locator('.usage-cache-point').count(), 0);
    await assertTrendTooltip('2026-11-17 01:00', '800', 700, 100);
    await page.locator('#paneUsage').screenshot({ path: path.join(output, 'usage-hover.png') });
    await page.mouse.move(0, 0);
    assert.equal(await focus.locator('line').count(), 0);
    await page.screenshot({ path: path.join(output, 'usage-desktop.png'), fullPage: true });
    const desktopRingWidth = await page.locator('#usageShare svg').evaluate(el => el.getBoundingClientRect().width);
    await page.setViewportSize({ width: 2560, height: 1440 });
    await page.waitForFunction(() => {
      const plot = document.getElementById('usageTrendPlot');
      const svg = plot.querySelector('svg');
      return svg?.viewBox.baseVal.width === plot.clientWidth && svg.viewBox.baseVal.height === plot.clientHeight;
    });
    const chartBottomGap = await page.locator('#paneUsage').evaluate(pane =>
      pane.getBoundingClientRect().bottom - parseFloat(getComputedStyle(pane).paddingBottom) - pane.querySelector('.usage-charts').getBoundingClientRect().bottom);
    assert.ok(Math.abs(chartBottomGap) <= 2, `unexpected empty space under charts: ${chartBottomGap}px`);
    assert.ok(await page.locator('#usageShare svg').evaluate(el => el.getBoundingClientRect().width) > desktopRingWidth);
    assert.equal(await page.locator('#usageShare svg text').count(), 0);
    assert.match(await page.locator('#usageTrendPlot path[stroke="#b5a0ff"]').getAttribute('d'), /C/);
    assert.equal(await page.locator('.usage-heading h3').first().evaluate(el => getComputedStyle(el).fontSize), '15px');
    await page.locator('#paneUsage').screenshot({ path: path.join(output, 'usage-wide.png') });
    await page.setViewportSize({ width: 1440, height: 1000 });
    const legendKeys = page.locator('.usage-heat-legend i');
    assert.equal(await legendKeys.count(), 5);
    assert.equal(await legendKeys.nth(0).evaluate(el => getComputedStyle(el).backgroundColor), 'rgb(56, 56, 56)');
    assert.equal(await legendKeys.nth(4).evaluate(el => getComputedStyle(el).backgroundColor), 'rgb(133, 155, 250)');
    await page.locator('#usageTotal').hover();
    assert.match(await tip.innerText(), /输入[\s\S]*900 Token/);
    assert.match(await tip.innerText(), /输出[\s\S]*100 Token/);
    assert.match(await tip.innerText(), /命中率计算基数[\s\S]*900 Token/);
    const toggles = page.locator('#usageTrend .usage-model-toggle');
    await toggles.filter({ hasText: 'kimi-k3' }).click();
    for (const days of [7, 30, 90]) {
      await page.locator(`#usageRanges button[data-range="${days}"]`).click();
      await assertTrendTooltip('2026-11-17', '1,000', 800, 200);
      assert.equal(await focus.locator('circle').count(), 1);
    }
    await toggles.filter({ hasText: 'glm-5.3' }).click();
    assert.match(await page.locator('#usageTrend').innerText(), /选择一个模型/);
    await toggles.filter({ hasText: 'glm-5.3' }).click();
    assert.equal(await page.locator('#usageTrend svg').count(), 1);
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth <= 2));
    assert.equal(await page.locator('.usage-charts').evaluate(el => getComputedStyle(el).gridTemplateColumns.split(' ').length), 1);
    await page.screenshot({ path: path.join(output, 'usage-mobile.png'), fullPage: true });
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.locator('#resetTokenUsage').click();
    assert.equal(await page.locator('#confirmTitle').innerText(), '清空 Token 统计？');
    await page.locator('#cancelAction').click();
    assert.equal(usageResets, 0);
    await page.locator('#resetTokenUsage').click();
    await page.locator('#confirmAction').click();
    await page.waitForFunction(() => document.getElementById('usageTotal').textContent === '0');
    assert.equal(usageResets, 1);
    assert.equal(await page.locator('#totalClient').innerText(), '20');
    await page.locator('#tabLogs').click();
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
    const rateLegend = page.locator('#rateLegend button[data-priority="0"]');
    const rateWindow = page.locator('#rateWindow');
    assert.equal(await rateWindow.inputValue(), '50');
    assert.equal(await rateWindow.getAttribute('min'), '20');
    assert.equal(await rateWindow.getAttribute('max'), '100');
    assert.equal(await rateWindow.getAttribute('step'), '10');
    assert.match(await rateLegend.innerText(), /60\.0%/);
    await rateWindow.fill('100');
    assert.match(await rateLegend.innerText(), /40\.0%/);
    await rateWindow.fill('20');
    assert.match(await rateLegend.innerText(), /0\.0%/);
    await rateWindow.press('ArrowUp');
    assert.equal(await rateWindow.inputValue(), '30');
    assert.match(await rateLegend.innerText(), /33\.3%/);
    await rateWindow.press('ArrowDown');
    assert.equal(await rateWindow.inputValue(), '20');
    for (const invalid of ['55', '10', '110', '']) {
      await rateWindow.fill(invalid);
      assert.equal(await rateWindow.getAttribute('aria-invalid'), 'true');
      assert.match(await rateLegend.innerText(), /0\.0%/);
      await rateWindow.press('Tab');
      assert.equal(await rateWindow.inputValue(), '20');
    }
    await rateWindow.fill('50');
    assert.match(await rateLegend.innerText(), /60\.0%/);
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
    const listedProviders = () => page.locator('#providerBody .provider-name').allTextContents();
    const recent = page.locator('#providerTable button[data-sort="recent"]');
    assert.deepEqual(await listedProviders(), ['mock-P2', 'mock-P3', 'mock-P7', 'mock-P1']);
    assert.equal(await page.locator('#providerTable th[aria-sort]').getAttribute('aria-sort'), 'ascending');
    await recent.click();
    assert.deepEqual(await listedProviders(), ['mock-P3', 'mock-P2', 'mock-P1', 'mock-P7']);
    const latestRow = page.locator('#providerBody tr').first();
    assert.equal(await latestRow.locator('time').getAttribute('datetime'), '2026-11-17T10:00:03Z');
    assert.match(await latestRow.innerText(), /最近失败/);
    assert.equal(await page.locator('#providerBody tr').last().locator('td').nth(2).innerText(), '—');
    await recent.press('Enter');
    assert.deepEqual(await listedProviders(), ['mock-P1', 'mock-P2', 'mock-P3', 'mock-P7']);
    assert.equal(await page.locator('#providerTable th[aria-sort]').getAttribute('aria-sort'), 'ascending');
    assert.equal(await page.locator('#providerSort').inputValue(), 'recent');
    await page.locator('#tabRequests').click();
    await page.locator('#tabLogs').click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: path.join(output, 'mobile.png'), fullPage: true });
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
    assert.ok(overflow <= 2, 'body overflows by ' + overflow);

    // A new stats epoch refreshes the frozen request list in other tabs.
    data.reset_epoch = 0;
    data.recent_attempts = [{ request_id: 'old-request', provider: 'mock-P5', model_id: 'old-model', priority: 5, success: true }];
    await page.waitForFunction(() => document.querySelector('#priorityFilter option[value="5"]'));
    await page.locator('#tabRequests').click();
    assert.match(await page.locator('#requestBody').innerText(), /old-request/);
    await page.locator('#priorityFilter').selectOption('5');
    data.reset_epoch++;
    data.recent_attempts = [];
    await page.waitForFunction(() => !document.querySelector('#priorityFilter option[value="5"]'));
    assert.equal(await page.locator('#requestBody tr').count(), 0);

    // Reconnect on the same build and discard the previous server log buffer.
    logGeneration = 'cleared';
    for (const stream of logStreams) stream.end();
    await page.locator('#tabLogs').click();
    await page.waitForFunction(() => document.getElementById('logTabCount').textContent === '1');
    await page.locator('#jumpLatest').click();
    await page.waitForFunction(() => document.getElementById('logEntries').textContent.includes('new generation'));

    // An already open tab loads changed assets even when the version is the same.
    build = 'next-build';
    await page.waitForFunction(() => document.querySelector('meta[name="dashboard-assets"]').content === 'next-build' &&
      !location.search.includes('_dashboard_build') && document.getElementById('buildInfo').textContent.includes('v-test'));
    await page.locator('#rateWindow').fill('100');
    await page.reload();
    await page.waitForFunction(() => document.querySelector('#rateLegend button')?.textContent.includes('40.0%'));
    assert.equal(await page.locator('#rateWindow').inputValue(), '100');
    await page.evaluate(() => localStorage.setItem('ai-dashboard.rate-window', '55'));
    await page.reload();
    await page.waitForFunction(() => document.querySelector('#rateLegend button')?.textContent.includes('60.0%'));
    assert.equal(await page.locator('#rateWindow').inputValue(), '50');
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(output, 'result.json'), JSON.stringify({ reads, logCache: 1000, visibleLogs: 200, errors, overflow }, null, 2));
  } finally {
    await browser.close();
    for (const socket of sockets) socket.destroy();
    await new Promise(resolve => server.close(resolve));
  }
});
