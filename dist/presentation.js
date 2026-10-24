'use strict';

function routingHTML(entry) {
  const routes = { static_consultation: '静态咨询', dynamic_consultation: '动态咨询', task_request: '办理请求', clarify_or_human: '澄清 / 风险 / 人工' };
  return '<section class="routing-summary" aria-label="本案实际服务分流"><div class="routing-options">' + Object.entries(routes).map(([route, label]) => '<span class="route-option' + (entry.Route === route ? ' selected' : '') + '">' + (entry.Route === route ? '● ' : '') + label + '</span>').join('') + '</div><p class="tiny muted">当前：' + esc(routes[entry.Route] || '旧案件 / 尚未重新判定') + ' · 政策、事实核验与执行授权始终由代码控制。</p>' + intakeHTML(entry.Intake) + '</section>';
}

function judgeReason(reason) {
  if (reason === 'ready') return '配置就绪';
  const reasons = { mode_off: '开关已关闭', missing_api_key: '服务端未配置密钥', synthetic_egress_not_allowed: '未获合成数据外发许可', invalid_base_url: '模型接口配置无效', invalid_model: '模型名称无效', cooldown: '接口受限或连续失败，冷却30秒', synthetic_only: '自由输入不外发，仅合成样例可调用', rule_priority: '确定性规则或风险规则优先', case_owned_or_closed: '人工持有、已结案、有风险或已有任务', timeout: '模型调用超时', network_error: '模型网络不可用', http_error: '模型接口受限或返回错误', invalid_response: '模型返回格式无效', response_too_large: '模型响应超过限制', model_unavailable: '模型不可用', shadow_only: '只对照，不改变规则判断', model_uncertain: '判定概率不足，未采用模型判断', model_rule_conflict: '模型与现有规则冲突', outside_model_scope: '不属于本期模型覆盖范围', mode_changed: '等待期间开关已变化，丢弃旧判断', case_changed: '等待期间案件已变化，丢弃旧判断', model_refund: '采用退款进度语义判断，不自动申请任务', model_human: '采用明确人工意愿判断' };
  return reasons[reason] || reason || '尚无判定记录';
}

function intakeHTML(decision) {
  if (!decision) return '<p class="tiny muted">最近消息判断：尚无记录，历史结果未追溯改写。</p>';
  return '<p class="tiny intake-source">' + (decision.Fallback ? '已自动回退规则' : decision.Source === 'jev' ? '最近消息采用 JEV 判断' : '最近消息由规则判断') + ' · ' + esc(judgeReason(decision.Reason)) + '<br>规则意图 ' + esc(decision.RuleIntent) + ' → 实际采用 ' + esc(decision.Intent) + '</p>';
}

function judgeSwitchHTML(status, unavailable = false) {
  const mode = status.Mode || 'off';
  const labels = { off: '关闭 · 只用规则', shadow: '影子对照', assist: 'JEV 辅助分流' };
  return '<section class="judge-switch" aria-label="JEV运行模式"><div><strong>分流方式</strong><small>模型出错或不确定时回退规则；风险规则不让位。</small></div><div class="judge-switch-buttons">' + Object.entries(labels).map(([value, label]) => '<button type="button" class="secondary" data-judge-mode="' + value + '" aria-pressed="' + (mode === value) + '" ' + (unavailable || mode === value || (value !== 'off' && !status.Configured) ? 'disabled' : '') + '>' + label + '</button>').join('') + '</div><p class="tiny muted">' + esc(judgeReason(mode === 'off' && !status.Configured ? status.ConfigurationReason || status.Reason : status.Reason)) + (status.CooldownUntil ? ' · 冷却到 ' + esc(status.CooldownUntil) : '') + ' · 本机运行时开关，重启依服务端配置。</p></section>';
}

const presentationDialog = document.querySelector('#presentation-dialog');

function presentationControlsHTML(entry) {
  return '<div class="button-row presentation-controls">' + (entry && rows(entry.Plans).length ? button('客户视角预览', 'client_preview', '', 'secondary') : '') + button('系统执行全景', 'system_map', '', 'quiet') + '</div>';
}

function judgeBaselineHTML(observation) {
  const transport = observation.HTTPStatus ? '<small>HTTP ' + esc(observation.HTTPStatus) + '</small><br>' : '';
  if (!observation.SampleID) return transport + '<small>历史记录未绑定用例标签，不评分</small><br>';
  const describe = (intent, human) => '退款进度=' + (intent === 'refund_progress' ? '是' : '否') + ' / 明确人工=' + ((typeof human === 'boolean' ? human : intent === 'human') ? '是' : '否');
  return transport + '<small class="replay-label">' + esc(observation.SampleID) + '<br>规则实际判定：' + esc(describe(observation.LocalIntent, observation.LocalHumanRequested)) + '<br>开发时人工预设：' + esc(describe(observation.ExpectedIntent, observation.ExpectedHumanRequested)) + '</small><br>';
}

function judgeMetrics(observations) {
  const metrics = { attempted: observations.length, scored: 0, localCorrect: 0, modelCorrect: 0, latencyMS: 0, inputTokens: 0, outputTokens: 0 };
  for (const observation of observations) {
    metrics.latencyMS += Number(observation.LatencyMS) || 0;
    if (observation.Status !== 'ok') continue;
    metrics.inputTokens += Number(observation.InputTokens) || 0;
    metrics.outputTokens += Number(observation.OutputTokens) || 0;
    if (!observation.SampleID || !observation.LocalIntent || !observation.ExpectedIntent) continue;
    for (const [point, intent] of [['refund_progress', 'refund_progress'], ['human_requested', 'human']]) {
      const choice = observation.Answers?.[point]?.Choice;
      if (!['yes', 'no', 'unclear'].includes(choice)) continue;
      const expectedFlag = point === 'human_requested' && typeof observation.ExpectedHumanRequested === 'boolean' ? observation.ExpectedHumanRequested : observation.ExpectedIntent === intent;
      const localFlag = point === 'human_requested' && typeof observation.LocalHumanRequested === 'boolean' ? observation.LocalHumanRequested : observation.LocalIntent === intent;
      const expected = expectedFlag ? 'yes' : 'no';
      const local = localFlag ? 'yes' : 'no';
      metrics.scored++;
      if (local === expected) metrics.localCorrect++;
      if (choice === expected) metrics.modelCorrect++;
    }
  }
  return metrics;
}

function judgeSummaryHTML(observations) {
  const metrics = judgeMetrics(observations);
  return '<div class="notice"><h3>本次实际对照，不预填提升</h3><p>调用尝试 ' + metrics.attempted + ' 次 / 累计等待 ' + metrics.latencyMS + ' ms<br>成功返回 tokens：输入 ' + metrics.inputTokens + ' / 输出 ' + metrics.outputTokens + '</p><p>同一批已成功且带标签的判定点：' + (metrics.scored ? '规则符合预设 ' + metrics.localCorrect + '/' + metrics.scored + '；JEV 符合预设 ' + metrics.modelCorrect + '/' + metrics.scored : '暂无可评分结果') + '</p><p class="tiny muted">这不是独立业务测试集，标签由开发时预设；失败/缺标签不参与准确性分母，不确定计未符合。未接账单与真实业务轮次，不换算节省金额，也不声称降低服务成本。</p></div>';
}

function customerPreviewHTML(entry) {
  const plan = rows(entry.Plans)[0];
  const task = database.Tasks[entry.Receipt];
  const canConfirm = entry.State === 'plan' && !entry.Receipt;
  return '<div class="preview-layout"><section class="customer-device" aria-label="客户实际交互预览"><header><span class="brand-mark" aria-hidden="true">↗</span><div><strong>售后服务</strong><small>铁路退款进度</small></div>' + badge('合成演示', 'outline') + '</header><div class="customer-content"><div class="customer-question">' + esc(rows(entry.Messages).find(message => message.Role === 'user')?.Text || '查询退款进度') + '</div><p class="customer-answer">我们先核对查到的事实，再由你决定是否办理下一步。</p><article class="customer-plan"><div class="customer-plan-head">' + badge(entry.Receipt ? '已提交追踪' : canConfirm ? '待你确认' : '历史方案', 'teal') + '<h3>退款还在处理中，下一步怎么做？</h3></div><div class="customer-plan-body"><h4>我理解的问题</h4><p>你想知道退款到了哪一步，并希望继续跟进。</p><dl><dt>关联订单</dt><dd>' + esc(entry.OrderID) + '</dd><dt>退票记录</dt><dd>' + esc(entry.Facts?.['退票状态'] || '尚未核验') + '</dd><dt>支付进度</dt><dd>' + esc(entry.Facts?.['支付退款'] || '尚未核验') + '</dd></dl><h4>可以为你做</h4><h3>提交一份退款进度追踪请求</h3><p>' + esc(plan.Description) + '</p><div class="client-pros-cons"><section><h4>对你有什么帮助</h4>' + list(plan.Benefits, 'plan-notes', false) + '</section><section><h4>请先了解</h4>' + list(plan.Notes, 'plan-notes', false) + '</section></div><p class="tiny muted">依据：演示政策 ' + esc(plan.Clause) + '。追踪任务不会改变退款状态。</p>' + (task ? '<div class="client-task"><strong>追踪请求已提交</strong><span class="mono">' + esc(task.ID) + '</span><p>本次咨询与追踪请求可以继续处理；退款是否到账以支付回执为准。</p></div>' : '') + '<div class="button-row">' + (canConfirm ? '<button class="primary wide" data-presentation-confirm="' + esc(plan.ID) + '">这个方案可以，查看并确认</button><button class="quiet wide" data-presentation-handoff>暂不办理，请人工接着处理</button>' : '<span class="tiny muted">' + (entry.Receipt ? '已执行的历史方案，不会再次创建任务。' : '当前方案不可执行，请返回案件核验。') + '</span>') + '</div></div></article></div></section><aside class="preview-explanation"><p class="eyebrow">SAME PLAN, SAME CASE</p><h2>客户只需知道：<br>查到了什么，接下来能办什么。</h2><p class="source-copy">预览与主界面使用同一份后端方案，不是另外编写的示例。点确认后仍进入原确认弹窗，并校验订单、证据与方案令牌。</p><h3>这张卡如何成为可复用经验？</h3>' + artifactHTML(entry, rows(database.Changes).find(change => change.Source === entry.ID) || null) + '<ol class="steps"><li>方案卡持久化为独立 PLAN 记录，不只是聊天文字。</li><li>客户明确确认后，工具任务引用这张方案卡和授权。</li><li>人工处理成功、用户确认结果后，才能起草候选。</li><li>候选经过回放、人批和发布，下一案才可采用。</li></ol><div class="notice"><strong>边界保持不变</strong><p>这是本地合成交互预览，不是多用户身份系统；无真实资金动作，不承诺到账。</p></div></aside></div>';
}

function comparisonHTML(change) {
  const source = database.Cases[change.Source];
  const reuse = allCases().find(entry => entry.ID !== change.Source && rows(entry.Plans).some(plan => plan.ProcedureID === change.Procedure.ID));
  const firstQuestion = entry => rows(entry?.Messages).find(message => message.Role === 'user')?.Text || '未记录';
  const sourceTask = source ? database.Tasks[source.Receipt] : null;
  const reuseTask = reuse ? database.Tasks[reuse.Receipt] : null;
  return '<details class="actual-comparison"><summary>首次与复用后的实际对话 · 非预写故事</summary><div class="comparison-grid"><article><span class="eyebrow">第一次 / 人工来源</span><blockquote>' + esc(firstQuestion(source)) + '</blockquote><p>固定版本 ' + esc(source?.Snapshot || '未记录') + '<br>人工接手：' + (rows(source?.Events).some(event => event.Kind === 'human_accepted') ? '有接手事件' : '无接手证据') + '</p><p class="mono">' + esc(sourceTask?.ID || '无任务回执') + '</p></article><article><span class="eyebrow">发布后的新案件 / 实际复用</span>' + (reuse ? '<blockquote>' + esc(firstQuestion(reuse)) + '</blockquote><p>固定版本 ' + esc(reuse.Snapshot) + '<br>采用流程 ' + esc(change.Procedure.ID) + '<br>人工接手：' + (rows(reuse.Events).some(event => event.Kind === 'human_accepted') ? '有接手事件' : '本案无接手事件') + '</p><p class="mono">' + esc(reuseTask?.ID || '尚未取得任务回执，不能算执行成功') + '</p>' : '<p class="source-copy">尚未找到实际复用案件。发布后请新建会话验证，不预填改善结果。</p>') + '</article></div><p class="tiny muted">仅展示本机案例的路径变化，不推算总体解决率、人工时长或节省金额。回滚后历史复用记录仍可查看。</p></details>';
}

function systemMapHTML(entry) {
  const nodes = [['客户界面', '提问、方案卡、确认与反馈'], ['Go 服务 / 规则编排', '分诊、同案交接、流程匹配与政策门'], ['白名单工具', '查询订单、退款轨迹、创建沙箱追踪'], ['SQLite', '案件、方案、授权引用、任务与快照']];
  return '<p class="source-copy">上方是结构示意，不是本案成功结果；下方只读取已经发生的操作证据。</p><div class="system-nodes">' + nodes.map(([title, description], index) => '<article><span class="eyebrow">0' + (index + 1) + '</span><h3>' + title + '</h3><p>' + description + '</p></article>').join('') + '</div><section class="system-feedback"><h3>经验回流，不是自动修改政策</h3><p>人工实际处理 → 用户结果确认 → 有来源的候选 → 隔离回放 → 人工批准 → 发布快照 → 新案件匹配 → 用户确认后执行</p><p class="tiny muted">JEV 仅作可选影子判定：输入理解不等于业务授权。回滚影响新案件，不撤销已创建的业务任务。</p></section>' + (entry ? '<div class="system-real"><div><h3>本案实际步骤</h3>' + list(entry.ActualSteps) + '<h3>本案实际事件</h3>' + eventsHTML(entry.Events) + '</div><aside><h3>产物关联</h3>' + artifactHTML(entry) + '<h3>工具输入与输出</h3>' + (rows(entry.Evidence).map(evidence => '<details class="evidence-item"><summary>' + esc(evidence.Tool) + ' · ' + (evidence.Success ? '成功' : '失败') + '</summary><pre>' + esc(evidence.Input) + '</pre><pre>' + esc(evidence.Output) + '</pre></details>').join('') || '<p class="source-copy">尚无工具证据。</p>') + '</aside></div>' : '<div class="notice"><p>当前未选择案件；发送请求后，才会产生可检查的真实沙箱执行记录。</p></div>');
}

function openPresentation(mode) {
  const entry = record();
  if (mode === 'client' && !rows(entry?.Plans).length) return;
  presentationDialog.innerHTML = '<div class="presentation-heading"><div><p class="eyebrow">' + (mode === 'client' ? 'CUSTOMER VIEW / SHARED BACKEND ARTIFACT' : 'ARCHITECTURE / LIVE EXECUTION EVIDENCE') + '</p><h2 id="presentation-title">' + (mode === 'client' ? '客户看到的方案与下一步' : '从客户提问，到可复用处理流程') + '</h2></div><button class="secondary" data-close-presentation>关闭预览</button></div><div class="presentation-body">' + (mode === 'client' ? customerPreviewHTML(entry) : systemMapHTML(entry)) + '</div>';
  presentationDialog.showModal();
}

presentationDialog.addEventListener('click', event => {
  const target = event.target.closest('button');
  if (!target || target.disabled || busy) return;
  if (target.hasAttribute('data-close-presentation')) presentationDialog.close();
  if (target.dataset.presentationConfirm) { presentationDialog.close(); showConfirmation(target.dataset.presentationConfirm); }
  if (target.hasAttribute('data-presentation-handoff')) { presentationDialog.close(); perform({Action:'handoff',CaseID:selectedCase}); }
  if (target.dataset.action === 'open_customer') { presentationDialog.close(); if (target.dataset.id) setCase(target.dataset.id); navigate('agent'); }
});
