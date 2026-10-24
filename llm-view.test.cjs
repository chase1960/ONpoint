const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const scope = vm.createContext({ rows: x => Array.isArray(x) ? x : [], esc: x => String(x ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])) });
vm.runInContext(fs.readFileSync(__dirname + '/dist/llm-view.js', 'utf8'), scope);
test('model text is escaped and tied to the correct message', () => {
  const entry = {LLMReplies:[{MessageAt:'a',Status:'ok',Explanation:'<img onerror=alert(1)>',Model:'qwen3.8-flash',Sources:['decision'],LatencyMS:1,InputTokens:2,OutputTokens:3}]};
  assert.equal(scope.llmExplanationHTML(entry,{At:'b'}),'');
  const html=scope.llmExplanationHTML(entry,{At:'a'});
  assert.ok(html.includes('&lt;img')); assert.ok(!html.includes('<img'));
  assert.ok(html.includes('模型补充解释')); assert.ok(html.includes('已核验结论'));
});
test('failed calls show fallback and never display unaccepted prose', () => {
  const html=scope.llmExplanationHTML({LLMReplies:[{MessageAt:'a',Status:'fallback',Explanation:'UNACCEPTED',Error:'http_403'}]},{At:'a'});
  assert.ok(html.includes('规则答复')); assert.ok(!html.includes('UNACCEPTED'));
});
