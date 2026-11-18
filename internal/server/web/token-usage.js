(function () {
  'use strict';
  const C = window.AiDashboardCore;
  const $ = id => document.getElementById(id);
  const ns = 'http://www.w3.org/2000/svg';
  const tokens = value => (value?.input_tokens || 0) + (value?.output_tokens || 0);
  const count = new Intl.NumberFormat('zh-CN');
  const compact = new Intl.NumberFormat('en', { notation: 'compact', maximumFractionDigits: 1 });
  const iso = date => date.toISOString().slice(0, 10);
  const calendar = day => new Date(day + 'T00:00:00Z');
  const activityWeeks = 52;
  let data, signature, controller, lastPoll = 0, range = 1;
  let trendChart;
  const hiddenModels = new Set();
  const usageTooltips = new WeakMap();
  const svgNode = (tag, attrs, text) => {
    const el = document.createElementNS(ns, tag);
    for (const [key, value] of Object.entries(attrs)) el.setAttribute(key, value);
    if (text !== undefined) el.textContent = text;
    return el;
  };
  const node = (tag, className, text) => {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (text !== undefined) el.textContent = text;
    return el;
  };
  const set = (id, text) => {
    if ($(id).textContent !== String(text)) $(id).textContent = text;
  };
  const stepDate = (day, offset) => new Date(calendar(day).getTime() + offset * 86400000);
  const windowDays = (today, days) => Array.from({ length: days }, (_, i) => iso(stepDate(today, i + 1 - days)));

  function combine(values) {
    const total = { input_tokens: 0, output_tokens: 0, cached_input_tokens: 0, cache_eligible_tokens: 0 };
    for (const value of Object.values(values || {})) {
      for (const key of Object.keys(total)) total[key] += value[key] || 0;
    }
    return total;
  }

  function modelColor(model) {
    if (model === 'glm-5.3') return '#b5a0ff';
    if (model === 'kimi-k3') return '#54d6bb';
    if (model === 'deepseek-flash') return '#f6bd70';
    if (model === '其他模型') return '#8396b2';
    let hash = 0;
    for (const char of model) hash = (Math.imul(hash, 31) + char.codePointAt(0)) >>> 0;
    return 'hsl(' + (hash % 360) + ' 68% 68%)';
  }

  // Pure path builder for the cache-hit overlay (unit-tested in webtest):
  // null buckets are skipped so the dashed line stays continuous; a single
  // known bucket yields only a moveto, which paints nothing, so the caller
  // draws that lone point as a dot instead.
  function cacheHitPath(hits, x, y) {
    let d = '', known = 0, last = null;
    hits.forEach((hit, i) => {
      if (hit === null || hit === undefined || !Number.isFinite(hit)) return;
      d += (d ? 'L' : 'M') + x(i) + ',' + y(hit);
      known += 1;
      last = { cx: x(i), cy: y(hit) };
    });
    return { d, dot: known === 1 ? last : null };
  }

  function drawOverview() {
    const total = data.total;
    const days = windowDays(data.today, 365);
    const dayTokens = days.map(day => tokens(combine(data.days[day])));
    let trailing = dayTokens.length - 1;
    if (!dayTokens[trailing]) trailing--;
    let streak = 0;
    while (trailing >= 0 && dayTokens[trailing] > 0) { streak++; trailing--; }
    const peak = Math.max(0, ...dayTokens);
    const todayBucket = combine(data.days[data.today]);
    const todayRequests = Object.values(data.days[data.today] || {}).reduce((sum, value) => sum + (value.requests || 0), 0);
    const eligible = total.cache_eligible_tokens || 0;
    const cached = total.cached_input_tokens || 0;
    const firstCount = total.first_count || 0;
    const decodeMs = total.decode_ms || 0;
    const decodeTokens = total.decode_tokens || 0;
    set('usageTotal', compact.format(tokens(total)));
    setUsageTooltip($('usageTotal'), [
      { name: '输入', value: count.format(total.input_tokens || 0) + ' Token', swatch: '#4d6bfe' },
      { name: '输出', value: count.format(total.output_tokens || 0) + ' Token', swatch: '#0d8a8a' },
      { name: '缓存命中输入', value: count.format(cached) + ' Token' },
      { name: '命中率计算基数', value: count.format(eligible) + ' Token' }
    ]);
    set('usageToday', compact.format(dayTokens.at(-1)));
    setUsageTooltip($('usageToday'), [
      { name: '今日 Token', value: count.format(tokens(todayBucket)) },
      { name: '今日请求数', value: count.format(todayRequests) + ' 次' }
    ]);
    set('usageRequests', count.format(total.requests || 0));
    setUsageTooltip($('usageRequests'), [{ name: '总请求数', value: count.format(total.requests || 0) + ' 次' }]);
    set('usageCache', eligible ? C.formatRate(100 * cached / eligible) : '—');
    setUsageTooltip($('usageCache'), [
      { name: '命中率', value: eligible ? C.formatRate(100 * cached / eligible) : '—' },
      { name: '缓存命中输入', value: count.format(cached) + ' Token' },
      { name: '计算基数', value: count.format(eligible) + ' Token' },
      { note: '只在上游报告缓存明细的请求中计算：缓存读命中的输入 Token ÷ 该批请求的输入 Token。' }
    ]);
    set('usageFirst', firstCount ? C.formatDuration((total.first_ms || 0) / firstCount) : '—');
    setUsageTooltip($('usageFirst'), [
      { name: '平均耗时', value: firstCount ? C.formatDuration((total.first_ms || 0) / firstCount) : '—' },
      { name: '样本数', value: count.format(firstCount) + ' 次' },
      { note: '流式尝试开始到收到首个文本、思考或工具输出片段的平均时间。' }
    ]);
    set('usageDecode', decodeMs ? (decodeTokens * 1000 / decodeMs).toFixed(1) + ' t/s' : '—');
    setUsageTooltip($('usageDecode'), [
      { name: '解码速度', value: decodeMs ? (decodeTokens * 1000 / decodeMs).toFixed(1) + ' t/s' : '—' },
      { name: '输出 Token', value: count.format(decodeTokens) },
      { name: '计时用时', value: decodeMs ? C.formatDuration(decodeMs) : '—' },
      { note: '按上游报告的输出 Token 与可测流式输出阶段的用时合计计算。' }
    ]);
    set('usageStreak', streak + ' 天');
    setUsageTooltip($('usageStreak'), [
      { name: '连续活跃', value: streak + ' 天' },
      { name: '近一年活跃', value: dayTokens.filter(value => value > 0).length + ' 天' }
    ]);
    set('usagePeak', peak ? compact.format(peak) : '—');
    if (peak) {
      setUsageTooltip($('usagePeak'), [
        { name: '日期', value: days[dayTokens.lastIndexOf(peak)] },
        { name: 'Token', value: count.format(peak) }
      ]);
    } else {
      usageTooltips.delete($('usagePeak'));
      delete $('usagePeak').dataset.tooltip;
    }
    set('usageCoverage', data.started_at ? '自 ' + data.started_at.slice(0, 10) + ' 开始统计 · ' + data.timezone : '等待上游报告用量 · ' + data.timezone);
  }

  function drawActivity() {
    const target = $('usageActivity');
    const weekday = (calendar(data.today).getUTCDay() + 6) % 7;
    const sunday = iso(stepDate(data.today, 6 - weekday));
    const days = windowDays(sunday, activityWeeks * 7);
    const points = days.map(day => day <= data.today ? tokens(combine(data.days[day])) : 0);
    const wrap = node('div', 'usage-heat-wrap');
    const weekdays = node('div', 'usage-weekdays');
    for (const label of ['周一', '周二', '周三', '周四', '周五', '周六', '周日']) weekdays.append(node('span', '', label));
    const grid = node('div', 'usage-heat');
    const labels = node('div', 'usage-months');
    wrap.style.setProperty('--weeks', activityWeeks);
    const peak = Math.max(1, ...points);
    const showFirstMonth = days[0].slice(0, 7) === days[7].slice(0, 7);
    for (let i = 0; i < days.length; i++) {
      const day = days[i], value = points[i];
      const future = day > data.today;
      const observed = data.started_at && day >= data.started_at.slice(0, 10);
      const cell = node('span', 'usage-heat-cell');
      cell.dataset.level = value ? Math.min(4, Math.ceil(Math.sqrt(value / peak) * 4)) : 0;
      cell.dataset.tooltip = day + ' · ' + (future ? '尚未到来' : observed ? count.format(value) + ' Token' : '尚未开始统计');
      if (future) cell.classList.add('future');
      else if (!observed) cell.classList.add('untracked');
      if (i === 0 ? showFirstMonth : i % 7 === 0 && day.slice(0, 7) !== days[i - 7].slice(0, 7)) {
        const month = node('span', '', Number(day.slice(5, 7)) + '月');
        month.style.gridColumn = String(Math.floor(i / 7) + 1);
        labels.append(month);
      }
      grid.append(cell);
    }
    grid.setAttribute('role', 'img');
    grid.setAttribute('aria-label', '最近 52 个自然周的每日 Token 用量，每周从周一到周日');
    wrap.append(weekdays, grid, labels);
    target.replaceChildren(wrap);
  }

  function rankModels(buckets) {
    const totals = new Map();
    for (const bucket of buckets) {
      for (const [model, usage] of Object.entries(bucket || {})) {
        const value = tokens(usage);
        if (value > 0) totals.set(model, (totals.get(model) || 0) + value);
      }
    }
    return [...totals.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
  }

  function swatch(color) {
    const dot = node('span', 'usage-swatch');
    dot.style.background = color;
    dot.setAttribute('aria-hidden', 'true');
    return dot;
  }

  function setUsageTooltip(owner, rows) {
    usageTooltips.set(owner, rows);
    owner.dataset.tooltip = rows.map(row => row.text ?? row.note ??
      [row.name, row.value].filter(value => value !== undefined).join(' ')).join('\n');
  }

  function drawShare() {
    const target = $('usageShare');
    const buckets = range === 1 ? [data.days[data.today]] : windowDays(data.today, range).map(day => data.days[day]);
    const models = rankModels(buckets);
    const rangeLabel = range === 1 ? '今天' : '近 ' + range + ' 天';
    set('usageShareRange', rangeLabel);
    const total = models.reduce((sum, [, value]) => sum + value, 0);
    const chart = svgNode('svg', { viewBox: '0 0 160 160', role: 'img', 'aria-label': rangeLabel + '模型用量占比' });
    if (!total) chart.append(svgNode('circle', { cx: 80, cy: 80, r: 60, fill: 'none', 'stroke-width': 18, class: 'usage-ring-empty' }));
    const groups = models.length > 7 ? [...models.slice(0, 6), ['其他模型', models.slice(6).reduce((sum, [, n]) => sum + n, 0)]] : models;
    let start = 0;
    const legend = node('ul', 'usage-share-legend');
    for (const [model, value] of groups) {
      const length = value / total * 100;
      const color = modelColor(model);
      const gap = groups.length > 1 ? Math.min(.35, length * .25) : 0;
      const ring = svgNode('circle', { cx: 80, cy: 80, r: 60, pathLength: 100, fill: 'none', stroke: color,
        'stroke-width': 18, 'stroke-dasharray': (length - gap) + ' ' + (100 - length + gap), 'stroke-dashoffset': -start - gap / 2,
        transform: 'rotate(-90 80 80)', class: 'usage-ring-segment' });
      setUsageTooltip(ring, [{ text: model, swatch: color }, { text: count.format(value) + ' Token · ' + C.formatRate(length) }]);
      chart.append(ring);
      start += length;
      const item = node('li');
      item.append(swatch(color), node('span', 'usage-legend-name', model), node('strong', '', C.formatRate(length)));
      legend.append(item);
    }
    const frame = node('div', 'usage-ring-frame');
    frame.append(chart);
    target.replaceChildren(frame, total ? legend : node('p', 'usage-empty', rangeLabel + '暂无用量记录'));
  }

  function drawTrend() {
    const controls = $('usageTrendControls');
    const dates = range === 1 ? Array.from({ length: 24 }, (_, i) => data.today + 'T' + String(i).padStart(2, '0')) : windowDays(data.today, range);
    const source = range === 1 ? data.hours : data.days;
    const buckets = dates.map(date => source[date]);
    const models = rankModels(buckets);
    if (!models.length) {
      trendChart = null;
      $('usageTrendPlot').replaceChildren(node('p', 'usage-empty', '所选时段暂无用量记录'));
      controls.hidden = true;
      return;
    }
    const top = models.length > 7 ? models.slice(0, 6) : models;
    const series = top.map(([name]) => ({ name, color: modelColor(name), values: buckets.map(b => tokens(b?.[name])) }));
    if (top.length < models.length) {
      const remaining = models.slice(top.length).map(([name]) => name);
      series.push({ name: '其他模型', color: modelColor('其他模型'), values: buckets.map(b => remaining.reduce((sum, name) => sum + tokens(b?.[name]), 0)) });
    }
    const selected = series.filter(s => !hiddenModels.has(s.name));
    const observedBuckets = range === 1 ? buckets.slice(0, data.hour + 1) : buckets;
    const visible = selected.map(s => ({ ...s, values: s.values.slice(0, observedBuckets.length) }));
    const totals = observedBuckets.map(combine);
    const hits = totals.map(total => total.cache_eligible_tokens ? 100 * total.cached_input_tokens / total.cache_eligible_tokens : null);
    const tooltipRows = observedBuckets.map((bucket, i) => {
      const rows = [
        { text: range === 1 ? dates[i].replace('T', ' ') + ':00' : dates[i] },
        { text: '总消耗 · ' + count.format(tokens(totals[i])) + ' Token', emphasis: true },
        ...rankModels([bucket]).map(([model, value]) => ({ text: model + ' · ' + count.format(value) + ' Token', swatch: modelColor(model) }))
      ];
      if (hits[i] !== null) rows.push({ text: '缓存命中率 · ' + C.formatRate(hits[i]) });
      return rows;
    });
    const scaleLabels = dates.map(date => range === 1 ? date.slice(11) + ':00' : date.slice(5));
    controls.replaceChildren();
    controls.hidden = false;
    for (const s of series) {
      const toggle = node('button', 'usage-model-toggle');
      toggle.type = 'button';
      toggle.setAttribute('aria-pressed', String(!hiddenModels.has(s.name)));
      toggle.append(swatch(s.color), node('span', '', s.name));
      toggle.addEventListener('click', () => {
        if (!hiddenModels.delete(s.name)) hiddenModels.add(s.name);
        drawTrend();
        [...controls.querySelectorAll('.usage-model-toggle')].find(btn => btn.textContent === s.name)?.focus();
      });
      controls.append(toggle);
    }
    if (hits.some(value => value !== null)) controls.append(node('span', 'usage-cache-legend', '缓存命中率'));
    trendChart = visible.length ? { labels: scaleLabels, series: visible, hits, tooltipRows } : null;
    if (trendChart) paintTrend();
    else $('usageTrendPlot').replaceChildren(node('p', 'usage-empty', '选择一个模型查看用量'));
  }

  function paintTrend() {
    if (!trendChart) return;
    const target = $('usageTrendPlot');
    const width = target.clientWidth, height = target.clientHeight;
    if (!width || !height) return;
    const { labels, series, hits, tooltipRows } = trendChart;
    target.replaceChildren(drawPlot(labels, series, { width, height, hits, tooltipRows, smooth: true }));
  }

  function drawPlot(labels, series, { width, height, smooth = false, hits = [], tooltipRows }) {
    const left = 44, right = hits.some(v => v !== null) ? 39 : 13, top = 13, bottom = 25;
    const w = width - left - right, h = height - top - bottom;
    const plot = svgNode('svg', { viewBox: '0 0 ' + width + ' ' + height, role: 'img', 'aria-label': 'Token 用量趋势' });
    const maxValue = Math.max(1, ...series.flatMap(s => s.values));
    const hasHits = hits.some(v => v !== null);
    const delta = w / Math.max(1, labels.length - 1);
    const x = i => left + i * delta;
    const y = (v, max = maxValue) => top + h * (1 - v / max);
    for (const fraction of h >= 300 ? [0, .25, .5, .75, 1] : [0, .5, 1]) {
      const yy = y(fraction, 1);
      plot.append(svgNode('line', { x1: left, x2: width - right, y1: yy, y2: yy, class: 'usage-gridline' }));
      plot.append(svgNode('text', { x: left - 6, y: yy + 3, 'text-anchor': 'end', class: 'usage-axis-label' }, compact.format(maxValue * fraction)));
      if (hasHits) plot.append(svgNode('text', { x: width - right + 3, y: yy + 3, class: 'usage-axis-label' }, fraction * 100 + '%'));
    }
    const tickCount = Math.min(10, Math.max(2, Math.floor(w / 48)));
    const stride = Math.max(1, Math.ceil((labels.length - 1) / (tickCount - 1)));
    labels.forEach((label, i) => {
      if (i % stride !== 0 && i !== labels.length - 1) return;
      if (i > 0 && i < labels.length - 1 && x(labels.length - 1) - x(i) < 48) return;
      const anchor = i === 0 ? 'start' : i === labels.length - 1 ? 'end' : 'middle';
      plot.append(svgNode('text', { x: x(i), y: height - 5, 'text-anchor': anchor, class: 'usage-axis-label' }, label));
    });
    for (const s of series) {
      const path = s.values.map((value, i) => {
        const xx = x(i), yy = y(value);
        if (!i) return `M${xx},${yy}`;
        if (!smooth) return `L${xx},${yy}`;
        return `C${xx - delta * 2 / 3},${y(s.values[i - 1])} ${xx - delta / 3},${yy} ${xx},${yy}`;
      }).join(' ');
      plot.append(svgNode('path', { d: path, fill: 'none', stroke: s.color, 'stroke-width': 2.2, 'stroke-linejoin': 'round', 'stroke-linecap': 'round' }));
      if (s.values.length === 1) plot.append(svgNode('circle', { cx: x(0), cy: y(s.values[0]), r: 3, fill: s.color }));
    }
    if (hasHits) {
      // Null buckets (no cache-eligible input) are skipped, so the dashed
      // overlay stays one continuous line instead of shattering into
      // floating segments. A lone known bucket paints as a dot.
      const { d, dot } = cacheHitPath(hits, x, v => y(v, 100));
      plot.append(svgNode('path', { d, class: 'usage-cache-path' }));
      if (dot) plot.append(svgNode('circle', { cx: dot.cx, cy: dot.cy, r: 2.5, class: 'usage-cache-point' }));
    }
    const sampleCount = Math.max(...series.map(s => s.values.length));
    for (let i = 0; i < sampleCount; i++) {
      const start = Math.max(left, x(i) - delta / 2);
      const end = Math.min(width - right, x(i) + delta / 2);
      const overlay = svgNode('rect', { x: start, y: top, width: end - start, height: h, fill: 'transparent' });
      if (tooltipRows) {
        overlay.dataset.usageSample = i;
        setUsageTooltip(overlay, tooltipRows[i]);
      } else overlay.dataset.tooltip = labels[i] + '\n' + series[0].name + ' · ' + count.format(series[0].values[i]) + ' Token';
      plot.append(overlay);
    }
    if (tooltipRows) {
      const focus = svgNode('g', { class: 'usage-trend-focus', 'aria-hidden': 'true', 'pointer-events': 'none' });
      plot.append(focus);
      plot.addEventListener('pointerover', event => {
        const sample = event.target.dataset.usageSample;
        if (sample === undefined) {
          focus.replaceChildren();
          return;
        }
        const i = Number(sample), xx = x(i);
        focus.replaceChildren(svgNode('line', { x1: xx, x2: xx, y1: top, y2: top + h, class: 'usage-focus-line' }));
        for (const line of series) {
          if (line.values[i] !== undefined) {
            focus.append(svgNode('circle', { cx: xx, cy: y(line.values[i]), r: 4, fill: line.color, class: 'usage-focus-point' }));
          }
        }
        if (hits[i] != null) focus.append(svgNode('circle', { cx: xx, cy: y(hits[i], 100), r: 3.5, class: 'usage-cache-point usage-focus-point' }));
      });
      plot.addEventListener('pointerleave', () => focus.replaceChildren());
    }
    return plot;
  }

  function bindSegments(id, key, select) {
    $(id).addEventListener('click', event => {
      const button = event.target.closest('button[data-' + key + ']');
      if (!button) return;
      for (const choice of $(id).children) choice.setAttribute('aria-pressed', String(choice === button));
      select(button.dataset[key]);
    });
  }

  bindSegments('usageRanges', 'range', value => { range = Number(value); if (data) { drawTrend(); drawShare(); } });
  new ResizeObserver(paintTrend).observe($('usageTrendPlot'));

  async function refresh(force = false) {
    if (!force && (controller || document.hidden || Date.now() - lastPoll < 10000)) return;
    controller?.abort();
    const pending = new AbortController();
    controller = pending;
    lastPoll = Date.now();
    const timer = setTimeout(() => pending.abort(), 8000);
    try {
      const response = await fetch('/api/usage', { cache: 'no-store', signal: pending.signal });
      if (!response.ok) throw new Error('HTTP ' + response.status);
      const updated = await response.json();
      if (pending !== controller) return;
      if (!updated.today || !updated.total || !updated.days || !updated.hours) throw new Error('响应格式无效');
      const fingerprint = JSON.stringify(updated);
      if (fingerprint === signature) return;
      data = updated;
      drawOverview(); drawActivity(); drawShare(); drawTrend();
      signature = fingerprint;
    } catch (error) {
      if (pending === controller) {
        signature = null;
        set('usageCoverage', '用量暂不可用' + (data ? ' · 显示上次数据' : ''));
      }
    } finally {
      clearTimeout(timer);
      if (pending === controller) controller = null;
    }
  }

  window.AiTokenUsage = { refresh, tooltipFor: owner => usageTooltips.get(owner) };
})();
