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
  const row=C.parseLog('[2026-11-06 10:00:00.000] [full-id] ATTEMPT | [P2][1/3] provider-a | API error (status 429): <script>bad</script>',1);
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
  const rows=[C.parseLog(raw('ROUTER','mode=P1'),1),C.parseLog(raw('PROVIDER','[P1][1/3] a | FAIL | status 429'),2),C.parseLog(raw('PROVIDER','[P1][2/3] a | FAIL | status 502'),3),C.parseLog(raw('SUMMARY','result=SUCCESS'),4),C.parseLog(raw('PROVIDER','CANCELED'),5)];
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
  const modelFor = C.logProviderIndex(
    [{ name: 'account-a', model_id: 'old-model' }, { name: 'account-b', model_id: 'deepseek-flash' }],
    [{ request_id: 'request-1', provider: 'account-a', model_id: 'glm-5.3' }]
  );

  assert.equal(attempt.provider, 'account-a');
  assert.equal(attempt.label, '限流 429');
  assert.match(attempt.detail, /^尝试 3\/3/);
  assert.equal(next.provider, 'account-b');
  assert.equal(next.providerRole, '切换目标');
  assert.equal(next.label, '切换 · 429');
  assert.equal(next.priority, 3);
  assert.equal(skip.providerRole, '跳过对象');
  assert.equal(skip.label, '跳过 · 429');
  assert.equal(modelFor(next), 'deepseek-flash');
  assert.deepEqual(C.filterLogs([attempt, next, skip], { query: 'glm-5.3', result: 'limited' }, modelFor), [attempt]);
  assert.deepEqual(C.filterLogs([attempt, next, skip], { model: 'deepseek-flash', result: 'limited' }, modelFor), [next, skip]);
});
