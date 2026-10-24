const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

// 把 safetyHoldHTML 从 app.js 里单独取出来跑。
// 目的只有一个：阻断必须看得见——看不见的阻断，在演示现场和真实事故里都等于不存在。
function holdScope() {
  const source = fs.readFileSync(path.join(__dirname, 'dist/app.js'), 'utf8');
  const scope = vm.createContext({ esc: value => String(value ?? '') });
  const name = 'safetyHoldHTML';
  const start = source.indexOf('function ' + name + '(');
  assert.notEqual(start, -1, 'app.js 缺少函数 ' + name);
  let depth = 0;
  let end = -1;
  for (let index = source.indexOf('{', start); index < source.length; index++) {
    if (source[index] === '{') depth++;
    if (source[index] === '}') { depth--; if (depth === 0) { end = index + 1; break; } }
  }
  vm.runInContext(source.slice(start, end), scope);
  return scope;
}

test('未解除的阻断会渲染出来，并且给出解除入口', () => {
  const { safetyHoldHTML } = holdScope();
  const html = safetyHoldHTML({
    ID: 'CASE-1', Owner: 'human',
    SafetyHold: { Reason: '疑似安全事件描述，立即转人工', Source: 'rule_safety_gate', At: '2026-10-24T07:00:00Z' }
  });
  assert.match(html, /安全阻断生效中/);
  assert.match(html, /rule_safety_gate/);
  assert.match(html, /release-form/, '坐席应能看到显式解除入口');
});

test('没有坐席身份时不给解除入口', () => {
  const { safetyHoldHTML } = holdScope();
  const html = safetyHoldHTML({
    ID: 'CASE-1', Owner: 'agent',
    SafetyHold: { Reason: '疑似安全事件描述', Source: 'rule_safety_gate' }
  });
  assert.doesNotMatch(html, /release-form/);
});

test('已解除的阻断显示留痕，不再显示解除入口', () => {
  const { safetyHoldHTML } = holdScope();
  const html = safetyHoldHTML({
    ID: 'CASE-1', Owner: 'human',
    SafetyHold: { Reason: '疑似安全事件描述', Source: 'rule_safety_gate', ReleasedBy: 'human', ReleasedAt: '2026-10-24T07:30:00Z', ReleaseNote: '已电话核实无安全风险' }
  });
  assert.match(html, /阻断已解除/);
  assert.match(html, /已电话核实无安全风险/);
  assert.doesNotMatch(html, /release-form/);
});

test('没有阻断就不渲染任何东西', () => {
  const { safetyHoldHTML } = holdScope();
  assert.equal(safetyHoldHTML({ ID: 'CASE-1', Owner: 'agent' }), '');
});
