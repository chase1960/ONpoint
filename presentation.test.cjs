const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

function context() {
  const scope = vm.createContext({
    document: { querySelector: () => ({ addEventListener() {} }) },
    rows: value => Array.isArray(value) ? value : [],
    esc: value => String(value ?? '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[character])),
    database: { Cases: {}, Tasks: {} }
  });
  scope.allCases = () => Object.values(scope.database.Cases);
  vm.runInContext(fs.readFileSync(require('node:path').join(__dirname, 'dist/presentation.js'), 'utf8'), scope);
  return scope;
}

test('JEV metrics do not turn failed or unlabeled observations into scored results', () => {
  const scope = context();
  const result = scope.judgeMetrics([{ Status: 'error', LatencyMS: 2500 }, { Status: 'ok', InputTokens: 20, OutputTokens: 3, LatencyMS: 45 }]);
  assert.equal(result.scored, 0);
  assert.equal(result.attempted, 2);
  assert.equal(result.latencyMS, 2545);
  assert.equal(result.inputTokens, 20);
});

test('JEV uncertainty and incorrect answers are not counted as improvement', () => {
  const scope = context();
  const result = scope.judgeMetrics([{ SampleID: 'target', LocalIntent: 'refund_progress', ExpectedIntent: 'refund_progress', Status: 'ok', Answers: { refund_progress: { Choice: 'unclear' }, human_requested: { Choice: 'yes' } } }]);
  assert.equal(result.scored, 2);
  assert.equal(result.localCorrect, 2);
  assert.equal(result.modelCorrect, 0);
  assert.match(scope.judgeSummaryHTML([]), /暂无可评分结果/);
});

test('human preference is scored independently from refund business intent', () => {
  const scope = context();
  const result = scope.judgeMetrics([{ SampleID: 'human_progress', LocalIntent: 'human', ExpectedIntent: 'refund_progress', LocalHumanRequested: true, ExpectedHumanRequested: true, Status: 'ok', Answers: { refund_progress: { Choice: 'yes' }, human_requested: { Choice: 'yes' } } }]);
  assert.equal(result.scored, 2);
  assert.equal(result.localCorrect, 1);
  assert.equal(result.modelCorrect, 2);
});

test('routing view shows backend route and never claims model routing is active', () => {
  const scope = context();
  const html = scope.routingHTML({ Route: 'dynamic_consultation' });
  assert.match(html, /● 动态咨询/);
  assert.match(html, /执行授权始终由代码控制/);
  assert.match(scope.routingHTML({}), /尚未重新判定/);
});

test('model modes cannot be enabled in UI without server configuration', () => {
  const scope = context();
  const html = scope.judgeSwitchHTML({ Mode: 'off', Configured: false, ConfigurationReason: 'missing_api_key' });
  assert.equal((html.match(/ disabled/g) || []).length, 3);
  assert.match(html, /服务端未配置密钥/);
  const configured = scope.judgeSwitchHTML({ Mode: 'off', Configured: true, Reason: 'mode_off' });
  assert.equal((configured.match(/ disabled/g) || []).length, 1);
  assert.match(configured, /data-judge-mode="assist"/);
});

test('fallback and accepted model judgments are shown as different provenance', () => {
  const scope = context();
  assert.match(scope.intakeHTML({ Mode: 'assist', Source: 'rules', Fallback: true, Reason: 'timeout', RuleIntent: 'refund_progress', Intent: 'refund_progress' }), /已自动回退规则/);
  assert.match(scope.intakeHTML({ Mode: 'assist', Source: 'jev', Reason: 'model_refund', RuleIntent: 'unknown', Intent: 'refund_progress' }), /最近消息采用 JEV 判断/);
  assert.match(scope.intakeHTML({ Mode: 'shadow', Source: 'rules', Reason: 'shadow_only', RuleIntent: 'unknown', Intent: 'unknown' }), /只对照，不改变规则判断/);
});

test('read-only trace does not imply authorization or tool execution is still required', () => {
  const scope = context();
  const source = fs.readFileSync(require('node:path').join(__dirname, 'dist/app.js'), 'utf8');
  vm.runInContext(source.slice(source.indexOf('function traceHTML('), source.indexOf('function factsHTML(')), scope);
  const html = scope.traceHTML({ Route: 'dynamic_consultation', State: 'answered', TrackingRequested: false, Evidence: [{ Tool: 'query_order', Success: true }] });
  assert.match(html, /本次是只读咨询/);
  assert.match(html, /本次不需要/);
  assert.doesNotMatch(html, /待完成/);
});

test('consultation offers an optional tracking request, not immediate execution', () => {
  const scope = context();
  scope.button = (label, action) => '<button data-action="' + action + '">' + label + '</button>';
  const source = fs.readFileSync(require('node:path').join(__dirname, 'dist/app.js'), 'utf8');
  vm.runInContext(source.slice(source.indexOf('function caseActionsHTML('), source.indexOf('function caseHTML(')), scope);
  const entry = { Route: 'dynamic_consultation', State: 'answered', Owner: 'agent', Intent: 'refund_progress', QueryOrder: { Business: 'rail', Ticket: 'SUCCESS', Payment: 'PROCESSING' } };
  const html = scope.caseActionsHTML(entry);
  assert.match(html, /consultation_done/);
  assert.match(html, /request_tracking/);
  assert.doesNotMatch(html, /data-action="confirm"/);
  entry.QueryOrder.Payment = 'PAID';
  assert.doesNotMatch(scope.caseActionsHTML(entry), /request_tracking/);
});

test('before-after display never fabricates an unobserved reuse case', () => {
  const scope = context();
  scope.database.Cases.source = { ID: 'source', Snapshot: 'v1', Events: [], Messages: [{ Role: 'user', Text: '<script>不可信输入</script>' }] };
  const html = scope.comparisonHTML({ Source: 'source', Procedure: { ID: 'proc' } });
  assert.match(html, /尚未找到实际复用案件/);
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
});

test('before-after display derives reuse and receipt from actual matching records', () => {
  const scope = context();
  scope.database.Cases.source = { ID: 'source', Snapshot: 'v1', Messages: [{ Role: 'user', Text: '首次提问' }], Events: [{ Kind: 'human_accepted' }], Receipt: 'task-source' };
  scope.database.Cases.next = { ID: 'next', Snapshot: 'v2', Messages: [{ Role: 'user', Text: '换一种问法' }], Plans: [{ ProcedureID: 'proc' }], Events: [], Receipt: 'task-next' };
  scope.database.Tasks['task-source'] = { ID: 'task-source' };
  scope.database.Tasks['task-next'] = { ID: 'task-next' };
  const html = scope.comparisonHTML({ Source: 'source', Procedure: { ID: 'proc' } });
  assert.match(html, /换一种问法/);
  assert.match(html, /task-next/);
  assert.match(html, /本案无接手事件/);
  delete scope.database.Tasks['task-next'];
  assert.match(scope.comparisonHTML({ Source: 'source', Procedure: { ID: 'proc' } }), /尚未取得任务回执/);
});
