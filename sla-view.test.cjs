const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

// 只把承诺时钟的三个渲染函数从 app.js 里取出来跑，不加载整个页面脚本——
// 目的是保证"有承诺/已超期"两种状态都能渲染出来，且措辞不越权。
function promiseScope() {
  const source = fs.readFileSync(require('node:path').join(__dirname, 'dist/app.js'), 'utf8');
  const scope = vm.createContext({
    esc: value => String(value ?? ''),
    badge: text => '<span class="badge">' + String(text) + '</span>',
    rows: value => Array.isArray(value) ? value : [],
    database: { Cases: {}, Tasks: {} }
  });
  scope.tasksFor = entry => Object.values(scope.database.Tasks).filter(task => task.CaseID === entry.ID);
  for (const name of ['durationText', 'promiseTask', 'promiseHTML']) {
    const start = source.indexOf('function ' + name + '(');
    assert.notEqual(start, -1, 'app.js 缺少函数 ' + name);
    let depth = 0;
    let end = -1;
    for (let index = source.indexOf('{', start); index < source.length; index++) {
      if (source[index] === '{') depth++;
      else if (source[index] === '}') {
        depth--;
        if (depth === 0) { end = index + 1; break; }
      }
    }
    assert.notEqual(end, -1, '函数 ' + name + ' 未闭合');
    vm.runInContext(source.slice(start, end), scope);
  }
  return scope;
}
function entryWithTask(dueAt) {
  const scope = promiseScope();
  const entry = { ID: 'CASE-1', Receipt: 'TR-1' };
  scope.database.Cases['CASE-1'] = entry;
  scope.database.Tasks['TR-1'] = { ID: 'TR-1', CaseID: 'CASE-1', DueAt: dueAt };
  return { scope, entry };
}

test('promise clock renders nothing before a tracked task exists', () => {
  const scope = promiseScope();
  assert.equal(scope.promiseHTML(null), '');
  assert.equal(scope.promiseHTML({ ID: 'CASE-1' }), '');
});

test('promise clock shows remaining time without promising arrival', () => {
  const { scope, entry } = entryWithTask(new Date(Date.now() + 72 * 3600 * 1000).toISOString());
  const html = scope.promiseHTML(entry);
  assert.match(html, /距承诺到账还有/);
  assert.doesNotMatch(html, /已超期/);
  assert.doesNotMatch(html, /已到账/);
  assert.match(html, /任务提交不等于资金到账/);
});

test('expired promise is flagged as overdue and still says not arrived', () => {
  const { scope, entry } = entryWithTask(new Date(Date.now() - 3600 * 1000).toISOString());
  const html = scope.promiseHTML(entry);
  assert.match(html, /promise overdue/);
  assert.match(html, /已超期/);
  assert.match(html, /未到账/);
  assert.doesNotMatch(html, /已到账/);
});

test('duration text stays readable at hour and day scale', () => {
  const scope = promiseScope();
  assert.match(scope.durationText(30 * 60 * 1000), /30 分钟/);
  assert.match(scope.durationText(5 * 3600 * 1000), /5 小时/);
  assert.match(scope.durationText(72 * 3600 * 1000), /3 天/);
});
