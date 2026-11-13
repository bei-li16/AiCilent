const { test } = require('node:test');
const assert = require('node:assert/strict');
const C = require('../web/dashboard-core.js');

test('current YAML priorities exclude stale provider, breaker and chart history', () => {
  const data = {configured_priorities:[3,1,2,2],providers:[{priority:5}],cb:[{priority:4}],curves:[{priority:0,points:[60]},{priority:1,points:[100]},{priority:5,points:[0]}],latency_curve:[2,4],latency_curves:[{priority:0,points:[2,4]},{priority:1,points:[2]},{priority:2,points:[4]}]};
  assert.deepEqual(C.activePriorities(data),[1,2,3]);
  assert.deepEqual(C.chartSeries(data,'rate').map(c=>c.priority),[0,1,2,3]);
  assert.deepEqual(C.chartSeries(data,'latency').map(c=>c.points),[[2,4],[2],[4],[]]);
  assert.deepEqual(C.activePriorities({...data,configured_priorities:[]}),[]);
  assert.deepEqual(C.chartSeries({...data,latency_curves:undefined},'latency')[0].points,[2,4]);
});

test('chart multi-selection defaults to all and preserves empty or single saved selection', () => {
  assert.deepEqual([...C.chartSelection([0,1,2,3],null)],[0,1,2,3]);
  assert.deepEqual([...C.chartSelection([0,1,2,3],[1,3,5])],[1,3]);
  assert.deepEqual([...C.chartSelection([0,1,2,3],[])],[]);
  assert.deepEqual([...C.chartSelection([0,1,2,3],[2])],[2]);
});

test('attempt ratio excludes cancellations and distinguishes empty samples', () => {
  assert.equal(C.ratio(0, 0), null);
  assert.equal(C.formatRate(C.ratio(0, 0)), '—');
  assert.equal(C.ratio(3, 1), 75);
  assert.equal(C.attemptOutcome({ status_code: 499, outcome: 'failed' }).key, 'failed');
  assert.equal(C.attemptOutcome({ status_code: 499, outcome: 'canceled' }).key, 'canceled');
  assert.equal(C.attemptOutcome({ status_code: 429, outcome: 'failed' }).key, 'limited');
});
test('quantiles use valid successful-latency samples and do not mutate input', () => {
  const values = [4, 1, 3, 2, NaN, -1];
  assert.equal(C.quantile(values, .5), 2.5);
  assert.ok(Math.abs(C.quantile(values, .95) - 3.85) < 1e-10);
  assert.equal(values[0], 4);
  assert.equal(C.quantile([], .5), null);
  assert.equal(C.timestamp('0001-01-01T00:00:00Z'), 0);
  assert.equal(C.timestamp('not-a-date'), 0);
});
test('providers filter by recent state and sort numerically with no-sample rates last', () => {
  const ps = [{name:'account10',priority:10,total:0}, {name:'account2',priority:2,total:5,success:4,fail:1}, {name:'account1',priority:2,total:3,fail:3,last_err_type:'tpm_limit',last_activity:'2026-11-06T00:00:00Z'}];
  assert.deepEqual(C.sortProviders(ps,'priority').map(p=>p.name), ['account1','account2','account10']);
  assert.deepEqual(C.sortProviders(ps,'rate').map(p=>p.name), ['account1','account2','account10']);
  assert.equal(C.filterProviders(ps,{priority:'2',result:'limited'}).length,1);
  assert.equal(C.providerOutcome({total:10,fail:10,last_err_type:''}).label,'有历史记录');
});
test('partial request groups preserve full IDs and do not infer final failure', () => {
  const id='0123456789abcdef0123456789abcdef';
  const rows=[{request_id:id,provider:'a',outcome:'failed',status_code:429,latency_ms:20,timestamp:'2026-11-06T01:00:01Z'}, {request_id:id,provider:'b',success:true,latency_ms:30,timestamp:'2026-11-06T01:00:02Z'}, {request_id:'unknown',status_code:500,latency_ms:10,timestamp:'2026-11-06T01:00:03Z'}];
  const groups=C.groupRequests(rows), known=groups.find(g=>g.id===id);
  assert.equal(known.outcome.key,'success'); assert.equal(known.duration,50); assert.equal(known.providers.length,2);
  assert.equal(known.knownRetries,0); assert.equal(known.knownSwitches,1);
  const retries=C.groupRequests(['a','a','b','b'].map((provider,i)=>({request_id:'retry',provider,timestamp:'2026-11-06T01:00:0'+i+'Z'})))[0];
  assert.equal(retries.knownRetries,2); assert.equal(retries.knownSwitches,1);
  assert.equal(groups[0].outcome.key,'unknown');
  assert.equal(C.filterRequests(groups,{result:'limited'})[0].id,id);
  assert.equal(C.groupRequests(Array.from({length:250},(_,i)=>({request_id:String(i)}))).length,200);
});
test('bounded logs count evictions, truncate long lines and keep untrusted text as text', () => {
  const buffer=new C.LogBuffer(3);
  for(let i=0;i<10;i++) buffer.push('line '+i);
  assert.equal(buffer.rows.length,3); assert.equal(buffer.dropped,7);
  assert.equal(buffer.rows[0].body,'line 7');
  assert.ok(C.parseLog('x'.repeat(20000),1).raw.length<16500);
  const row=C.parseLog('[2026-11-06 10:00:00.000] [full-id] PROVIDER | [P2][1/3] provider-a | FAIL | 20ms | API error (status 429): <script>bad</script>',1);
  assert.equal(row.key,'limited'); assert.equal(row.priority,2); assert.equal(row.provider,'provider-a'); assert.ok(row.body.includes('<script>'));
  buffer.clear(); assert.equal(buffer.rows.length,0); assert.equal(buffer.dropped,0);
});
test('invalid statistics cannot silently replace the last valid snapshot', () => {
  assert.throws(()=>C.validateSnapshot({}),/统计字段/);
  const snapshot={total_client_req:0,total_req:0,total_success:0,total_fail:0,total_canceled:0,providers:[],recent_attempts:[],curves:[],latency_curve:[]};
  assert.equal(C.validateSnapshot(snapshot),snapshot);
  assert.deepEqual(C.validateSnapshot({...snapshot,recent_attempts:null}).recent_attempts,[]);
  assert.throws(()=>C.validateSnapshot({...snapshot,total_success:-1}),/统计字段/);
});
test('failure streak views and sorting preserve unknown historical state', () => {
  const now='2026-11-06T01:00:00Z';
  const providers=[{name:'old',total:20,fail:20,consecutive_fail:0}, {name:'ok',total:20,success:1,consecutive_fail:0,last_activity:now}, {name:'bad',total:20,fail:20,consecutive_fail:5,last_activity:now,last_err_type:'tpm_limit'}];
  assert.equal(C.providerStreak(providers[0]),null);
  assert.equal(C.providerStreak(providers[1]),0);
  assert.equal(C.filterProviders(providers,{},'streak')[0].name,'bad');
  assert.equal(C.filterProviders(providers,{},'unknown')[0].name,'old');
  assert.equal(C.filterProviders(providers,{model:'missing'},'streak').length,0);
  assert.deepEqual(C.sortProviders(providers,'streak').map(p=>p.name),['bad','ok','old']);
});
test('log levels distinguish diagnostics, rate limits, failures, success and cancellation', () => {
  const raw=(event,body)=>`[2026-11-06 10:00:00.000] [full-id] ${event} | ${body}`;
  const rows=[C.parseLog(raw('ROUTER','mode=P1'),1),C.parseLog(raw('PROVIDER','[P1][1/3] a | FAIL | 10ms | API error (status 429)'),2),C.parseLog(raw('PROVIDER','[P1][2/3] a | FAIL | 20ms | API error (status 502)'),3),C.parseLog(raw('SUMMARY','result=SUCCESS'),4),C.parseLog(raw('PROVIDER','[P1][3/3] a | CANCELED | 0s'),5)];
  assert.deepEqual(rows.map(l=>l.level),['debug','warn','error','info','info']);
  assert.equal(C.filterLogs(rows,{level:'warn+'}).length,2);
  assert.equal(C.filterLogs(rows,{level:'error'})[0].status,502);
  assert.equal(C.filterLogs(rows,{level:'info'}).length,2);
  assert.equal(C.parseLog(raw('INBOUND','user says ERROR status 429'),6).level,'info');
  assert.equal(C.parseLog(raw('INBOUND','user says ERROR status 429'),6).key,'neutral');
  assert.equal(C.parseLog(raw('ERROR','explicit error'),7).level,'error');
});

test('rate-limit logs distinguish the failed attempt from the next provider and resolve each model', () => {
  const prefix = '[2026-11-07 11:32:27.545] [request-1] ';
  const attempt = C.parseLog(prefix + 'PROVIDER | [P2][3/3] account-a | FAIL | 373ms | API error (status 429)', 1);
  const next = C.parseLog(prefix + 'ROUTER | degrade to next provider | account-b [P3] | reason=API error (status 429)', 2);
  const skip = C.parseLog(prefix + 'QUEUE | [P3] | provider account-b skipping (API error (status 429)) | rr=2 size=15', 3);
  const access = C.parseLog(prefix + 'ACCESS | POST /chat/completions | 429 | 2s | format=openai', 4);
  const other = C.parseLog(prefix + 'PROVIDER | [P2][1/3] account-a | FAIL | 12ms | protocol error: text says status 429', 5);
  const rows = [attempt, next, skip, access, other];
  const modelFor = C.logProviderIndex(
    [{ name: 'account-a', model_id: 'old-model' }, { name: 'account-b', model_id: 'deepseek-flash' }],
    [{ request_id: 'request-1', provider: 'account-a', model_id: 'glm-5.3' }]
  );

  assert.equal(attempt.provider, 'account-a');
  assert.equal(attempt.label, '限流 429');
  assert.equal(attempt.attemptNumber, '3/3');
  assert.match(attempt.detail, /^FAIL \| API error/);
  assert.equal(next.provider, 'account-b');
  assert.equal(next.providerRole, '切换目标');
  assert.equal(next.label, '限流转接');
  assert.equal(next.priority, 3);
  assert.equal(skip.providerRole, '跳过对象');
  assert.equal(skip.label, '因 429 跳过');
  assert.deepEqual([next, skip, access].map(l => l.attemptNumber), [null, null, null]);
  assert.equal(modelFor(next), 'deepseek-flash');
  assert.deepEqual(C.filterLogs(rows, { result: 'limited' }, modelFor), [attempt]);
  assert.deepEqual(C.filterLogs(rows, { model: 'deepseek-flash', result: 'route-switch' }, modelFor), [next]);
  assert.deepEqual(C.filterLogs(rows, { result: 'route-skip' }, modelFor), [skip]);
  assert.deepEqual(C.filterLogs(rows, { result: 'client-limited' }, modelFor), [access]);
  assert.deepEqual(C.filterLogs(rows, { result: 'failed' }, modelFor), [other]);
});
test('request timing is independent of attempts and preserves local failures', () => {
  const groups = C.groupRequests([
    { request_id: 'ok', provider: 'p1', model_id: 'glm', priority: 2, latency_ms: 500, status_code: 429 },
    { request_id: 'ok', provider: 'p2', model_id: 'glm', priority: 2, latency_ms: 3000, success: true }
  ], [
    { request_id: 'ok', model: 'P2', ended_at: '2026-11-10T00:00:01Z', status_code: 200, first_response_ms: 1800, total_ms: 4300 },
    { request_id: 'bad', model: 'P6', ended_at: '2026-11-10T00:00:02Z', status_code: 503, first_response_ms: null, total_ms: 12 }
  ]);
  assert.equal(groups[0].outcome.key, 'failed');
  assert.equal(groups[0].timing.first_response_ms, null);
  assert.deepEqual(groups[0].priorities, [6]);
  assert.equal(groups[1].timing.first_response_ms, 1800);
  assert.equal(groups[1].timing.total_ms, 4300);
});

test('log timings preserve the event clock and separate different elapsed time scopes', () => {
  const prefix = '[2026-11-10 12:53:17.398] [req] ';
  for (const [body, label, ms] of [
    ['INBOUND | POST / | msg=total=2s', '请求起点', 0],
    ['PROVIDER | [P2][3/3] acct | FAIL | 1.3810652s | API error (status 429)', '单次尝试', 1381.0652],
    ['TTFB | acct[P2] | upstream header=3.010456s | status=200', '上游响应头', 3010.456],
    ['FIRST_RESPONSE | elapsed=7.02s', '请求首响', 7020],
    ['STREAM | answered=true terminal_written=true | request_elapsed=9s | end_reason=protocol_complete', '请求至流结束', 9000],
    ['SUMMARY | result=SUCCESS | provider=acct | rr=1 | total=1m2.5s', '路由耗时', 62500],
    ['ACCESS | POST /chat/completions | 200 | 45.5µs | format=openai', '请求总耗时', .0455]
  ]) {
    const row = C.parseLog(prefix + body, 1);
    assert.equal(row.time, '12:53:17.398');
    assert.equal(row.timingLabel, label);
    assert.ok(Math.abs(row.timingMs - ms) < 0.000001);
    assert.equal(row.raw, prefix + body);
  }
  assert.equal(C.parseLog(prefix + 'STREAM | answered=true terminal_written=true | end_reason=protocol_complete', 1).timingMs, null);
});
test('success rate axis follows only the plotted values and stays within percentage bounds', () => {
  assert.deepEqual(C.rateAxis([0, 24, 100]), { minimum: 0, maximum: 100 });
  assert.deepEqual(C.rateAxis([12, 14, 21]), { minimum: 10, maximum: 25 });
  assert.ok(C.rateAxis([96, 100]).minimum >= 90);
  for (const n of [0, 50, 100]) {
    const { minimum, maximum } = C.rateAxis([n]);
    assert.ok(minimum >= 0 && minimum <= n && n <= maximum && maximum <= 100 && minimum < maximum);
  }
});

test('log milestones are distinct from outcomes and retry gap is separate from attempt duration', () => {
  const parse = body => C.parseLog('[2026-11-11 09:57:46.197] [req] ' + body, 1);
  const retry = parse('PROVIDER | [P2][2/3] acct | FAIL | 556ms | retry_start_gap=1.001s | API error (status 429)');
  assert.equal(retry.timingMs, 556);
  assert.ok(Math.abs(retry.retryGapMs - 1001) < 1e-6);
  assert.equal(retry.key, 'limited');
  assert.equal(parse('INBOUND | POST /chat/completions | model=P2').milestone.tone, 'request-start');
  assert.equal(parse('FIRST_RESPONSE | elapsed=1s').milestone.tone, 'request-first');
  assert.equal(parse('STREAM | answered=true terminal_written=true | end_reason=protocol_complete').milestone.tone, 'request-end');
  assert.equal(parse('STREAM | answered=true terminal_written=true | request_elapsed=9s | end_reason=protocol_complete').milestone.tone, 'request-end');
  assert.equal(parse('STREAM | answered=true terminal_written=false | end_reason=unexpected EOF').milestone, null);
  assert.equal(parse('PROVIDER | [P2][1/3] acct | OK | 3s').label, '上游成功');
  assert.equal(parse('SUMMARY | result=SUCCESS | provider=acct | total=3s').label, '路由成功');
});

test('attempt, route and stream completion have distinct presentation without extra success counts', () => {
  const parse = body => C.parseLog('[2026-11-12 01:35:28.677] [req] ' + body, 1);
  const attempt = parse('PROVIDER | [P2][2/3] acct | OK | 25.001234s');
  const summary = parse('SUMMARY | model=P1 | result=SUCCESS | provider=acct | rr=1 | total=25.002345s');
  const stream = parse('STREAM | answered=true terminal_written=true | request_elapsed=25.201234s | end_reason=protocol_complete');
  assert.deepEqual([attempt.label, summary.label, stream.milestone.label], ['上游成功', '路由成功', '流式完成']);
  assert.deepEqual([attempt.tone, summary.tone, stream.milestone.tone], ['attempt-success', 'route-success', 'request-end']);
  assert.deepEqual([attempt.key, summary.key, stream.key], ['success', 'success', 'neutral']);
  assert.ok(attempt.description.includes('一次上游'));
  assert.ok(summary.timingDescription.includes('重试、退避'));
  assert.ok(stream.description.includes('不另计一次成功'));
  assert.deepEqual([attempt, summary, stream].map(log => log.time), Array(3).fill('01:35:28.677'));
  assert.deepEqual([attempt, summary, stream].map(log => C.formatLogDuration(log.timingMs)), ['25.001 s', '25.002 s', '25.201 s']);
  assert.equal(parse('STREAM | answered=true terminal_written=false | end_reason=unexpected EOF').milestone, null);
});

test('summary priority follows the actual request/provider rather than the requested group or current config', () => {
  const parse = (request, body) => C.parseLog('[2026-11-12 01:35:28.677] [' + request + '] ' + body, 1);
  const first = parse('req', 'PROVIDER | [P1][1/3] a | FAIL | 1s');
  const last = parse('req', 'PROVIDER | [P3][1/3] b | OK | 2s');
  const summary = parse('req', 'SUMMARY | model=P1 | result=SUCCESS | provider=b | rr=0 | total=3s');
  const unknown = parse('other', 'SUMMARY | model=P1 | result=SUCCESS | provider=b | rr=0 | total=3s');
  const rows = [first, last, summary, unknown];
  const priorityFor = C.logPriorityIndex(rows, [{ request_id: 'req', provider: 'b', priority: 2 }]);
  assert.equal(priorityFor(summary), 3);
  assert.equal(priorityFor(unknown), 0);
  assert.equal(summary.priority, 0, 'do not change the original parsed record');
  assert.deepEqual(C.filterLogs(rows, { priority: '3' }, () => '', priorityFor), [last, summary]);
  assert.equal(C.logPriorityIndex([summary], [{ request_id: 'req', provider: 'b', priority: 3 }])(summary), 3);
  assert.equal(C.logPriorityIndex([summary])(summary), 0);
});

test('log duration precision preserves milliseconds without changing compact chart durations', () => {
  assert.equal(C.formatLogDuration(0), '0 ms');
  assert.equal(C.formatLogDuration(.0455), '45.500 µs');
  assert.equal(C.formatLogDuration(930.123456), '930.123 ms');
  assert.equal(C.formatLogDuration(62500), '62.500 s');
  for (const value of [null, undefined, NaN, -1]) assert.equal(C.formatLogDuration(value), '—');
  assert.equal(C.formatDuration(25001.234), '25.0 s');
});

test('trend windows count client requests and use the same range for model latency', () => {
  const attempts = { priority: 0, requests: [20, 21, 70, 71, 71, 120, 0], points: [1, 2, 3, 4, 5, 6, 7] };
  const latency = { priority: 0, requests: [21, 120, 71, 20, 0], points: [2, 6, 5, 1, 7] };
  const model = { model: 'glm', requests: [21, 71, 20], points: [2, 5, 1] };
  const data = { trend_request_seq: 120, curves: [attempts], latency_curves: [latency], model_latency_curves: [model] };
  assert.deepEqual(C.chartSeries(data, 'rate')[0].points, attempts.points);
  assert.deepEqual(C.chartSeries(data, 'rate', 50)[0].points, [4, 5, 6]);
  assert.deepEqual(C.chartSeries(data, 'rate', 100)[0].points, [2, 3, 4, 5, 6]);
  assert.deepEqual(C.chartSeries(data, 'latency', 50)[0].points, [6, 5]);
  assert.deepEqual(C.modelLatencySeries(data, 100), [{ model: 'glm', points: [2, 5] }]);
});

test('latency statistics combine selected priorities without counting the overall series twice', () => {
  const curve = (priority, points) => ({ priority, points, color: C.priorityColor(priority) });
  const p1 = curve(1, [1, 3]);
  const p2 = curve(2, [9]);
  const overall = curve(0, [1, 3, 9]);

  assert.deepEqual(C.latencySummary([p1]), { count: 2, p50: 2, p95: 2.9, color: p1.color });
  assert.deepEqual(C.latencySummary([p1, p2]), C.latencySummary([overall]));
  assert.deepEqual(C.latencySummary([overall, p1, p2]), C.latencySummary([overall]));
  assert.deepEqual(C.latencySummary([curve(3, [])]), { count: 0, p50: null, p95: null, color: C.priorityColor(3) });
});

test('all ranges accumulate success rates across every retained eligible attempt in the range', () => {
  const requests = [10, 80, 81, 120, 30, 120];
  const successes = [true, false, true, false, true, true];
  const points = [100, 50, 200 / 3, 50, 60, 200 / 3];
  const data = { trend_request_seq: 120, configured_priorities: [1], curves: [
    { priority: 0, requests, points, successes },
    { priority: 1, requests: [10, 80, 120], points: [100, 50, 100 / 3], successes: [true, false, false] }
  ] };
  assert.deepEqual(C.chartSeries(data, 'rate')[0].points, points);
  assert.deepEqual(C.chartSeries(data, 'rate', 50).map(c => c.points), [[0, 50, 100 / 3, 50], [0, 0]]);
  assert.deepEqual(C.chartSeries(data, 'rate', 100)[0].points, [0, 50, 100 / 3, 50, 60]);

  const long = { trend_request_seq: 120, curves: [{ priority: 0, requests: Array(51).fill(120),
    successes: [true, ...Array(50).fill(false)], points: Array(51).fill(0) }] };
  for (const range of [0, 50, 100]) {
    assert.deepEqual(C.chartSeries(long, 'rate', range)[0].points.slice(-2), [2, 100 / 51]);
  }
  const capped = { curves: [{ priority: 0, points: [],
    successes: [true, ...Array(240).fill(false)] }] };
  assert.deepEqual(C.chartSeries(capped, 'rate')[0].points, Array(240).fill(0));
});
