'use strict';

function llmBannerHTML(status) {
  return '<div class="disclosure"><strong>' + (status?.Enabled ? '模型解释已启用 · ' + esc(status.Model) : '模型解释未启用 · 使用规则答复') + '</strong><span>仅解释合成事实，方案仍需客户确认</span></div>';
}

function llmExplanationHTML(entry, message) {
  const reply = rows(entry.LLMReplies).find(item => item.MessageAt === message.At);
  if (!reply) return '';
  if (reply.Status !== 'ok') return '<div class="tiny muted">本次模型解释不可用，以上为规则答复。<details class="mini-detail"><summary>查看调用记录</summary><p>' + esc(reply.Model) + ' · ' + esc(reply.Error) + ' · ' + esc(reply.LatencyMS) + ' ms</p></details></div>';
  const sources = { decision: '已核验结论', ticket: '退票证据', payment: '支付证据' };
  return '<aside class="llm-explanation"><p class="tiny"><strong>模型补充解释 · ' + esc(reply.Model) + '</strong></p><p>' + esc(reply.Explanation) + '</p><details class="mini-detail"><summary>依据与调用记录</summary><p class="tiny muted">依据：' + rows(reply.Sources).map(source => esc(sources[source] || source)).join('、') + '<br>耗时 ' + esc(reply.LatencyMS) + ' ms · 输入 ' + esc(reply.InputTokens) + ' / 输出 ' + esc(reply.OutputTokens) + ' Token<br>解释供理解参考，以以上核验结论和方案卡为准。</p></details></aside>';
}
