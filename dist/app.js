'use strict';

const root = document.querySelector('#app');
const confirmDialog = document.querySelector('#confirm-dialog');
const tabs = { agent: '客户案件', human: '人工接手', evolution: '流程改进' };
const states = { intake: '已受理', clarify: '待补充信息', answered: '已说明事实', handoff: '等待人工', human: '人工处理中', plan: '待客户确认', waiting: '追踪已提交', resolved: '本次支持请求已处理' };
const changeStates = { draft: '草稿', replayed: '已回放', approved: '已批准', active: '已发布', rolled_back: '已回滚' };
let database = { Cases: {}, Changes: [], Snapshots: {}, Tasks: {}, Proposals: {}, Active: 'v1', Dataset: {}, JEV: {}, JudgeReport: [] };
let view = Object.hasOwn(tabs, location.hash.slice(1)) ? location.hash.slice(1) : 'agent';
let selectedCase = readSession('ota.selectedCase');
let selectedChange = readSession('ota.selectedChange');
let busy = false;
let connected = false;
let errorText = '';
let draftText = '';
let pendingOrder = 'DEMO-RAIL-001';
let pendingSample = null;
let reuseNotice = false;
let humanText = '';
const noteText = {};
const faultChoice = {};
const stepLabels = { query_order: '核验退票记录', query_refund_trace: '核验支付轨迹', explain_status: '说明当前进度', request_confirmation: '等待你确认', create_tracking: '提交追踪请求', show_receipt: '提供任务回执', ask_result: '确认本次处理结果' };
let confirmation = null;
let toastTimer;

function readSession(key) { try { return sessionStorage.getItem(key) || ''; } catch { return ''; } }
function saveSession(key, value) { try { sessionStorage.setItem(key, value); } catch {} }
function esc(value) { return String(value ?? '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[character])); }
function textValue(value) { return value === undefined || value === null || value === '' ? '未记录' : typeof value === 'object' ? JSON.stringify(value, null, 2) : String(value); }
function rows(value) { return Array.isArray(value) ? value : []; }
function dateText(value, full = false) { return !value || Number.isNaN(new Date(value).getTime()) ? '时间未记录' : new Date(value).toLocaleString('zh-CN', full ? { hour12: false } : { hour: '2-digit', minute: '2-digit', hour12: false }); }
function record() { return database.Cases[selectedCase] || null; }
function allCases() { return Object.values(database.Cases).sort((first, second) => String(second.Updated || '').localeCompare(String(first.Updated || ''))); }
function changeRecord() { return rows(database.Changes).find(change => change.ID === selectedChange) || rows(database.Changes)[0] || null; }
function orderFor(entry) { return rows(database.Dataset.Orders).find(order => order.ID === entry?.OrderID); }
function version(entry) { return typeof entry?.Snapshot === 'object' ? entry.Snapshot?.ID || entry.Snapshot?.Version || '已固定快照' : entry?.Snapshot || '未记录'; }
function tasksFor(entry) { return Object.values(database.Tasks).filter(task => task.CaseID === entry.ID); }
function badge(text, tone = '') { return '<span class="badge ' + tone + '">' + esc(text) + '</span>'; }
function button(label, action, extra = '', style = 'secondary', disabled = false) { return '<button type="button" class="' + style + '" data-action="' + action + '" ' + extra + (busy || !connected || disabled ? ' disabled' : '') + '>' + esc(label) + '</button>'; }
function list(items, className = 'steps', ordered = true) {
  const tag = ordered ? 'ol' : 'ul';
  return rows(items).length ? '<' + tag + ' class="' + className + '">' + items.map(item => '<li>' + esc(stepLabels[item] || textValue(item)) + '</li>').join('') + '</' + tag + '>' : '<p class="empty-mini">未记录</p>';
}
function notify(text) {
  const toast = document.querySelector('#toast');
  toast.textContent = text;
  toast.classList.add('visible');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toast.classList.remove('visible'), 4500);
}
function setCase(id) { selectedCase = id; saveSession('ota.selectedCase', id); humanText = ''; }
function navigate(target) {
  if (!Object.hasOwn(tabs, target)) return;
  view = target;
  history.replaceState(null, '', '#' + target);
  render();
}
function newDraft(reuse = false) {
  setCase(''); pendingOrder = 'DEMO-RAIL-001'; pendingSample = null; draftText = ''; reuseNotice = reuse;
  navigate('agent');
  root.querySelector('#message-text')?.focus();
}
function headerHTML() {
  return appHeaderHTML() + judgeSwitchHTML(database.JEV || {}, busy || !connected) + llmBannerHTML(database.LLM);
}
function appHeaderHTML() {
  return '<header class="topbar"><div class="brand"><span class="brand-mark" aria-hidden="true">↗</span><div><strong>ONpoint</strong><small>OTA 售后履约 Agent · 每一步，有依据</small></div></div><nav class="tabs" aria-label="工作区">' + Object.entries(tabs).map(([key, label]) => '<button type="button" class="tab" data-nav="' + key + '" ' + (view === key ? 'aria-current="page" ' : '') + (busy ? 'disabled' : '') + '>' + label + '<small>' + key + '</small></button>').join('') + '</nav><div class="connection"><i class="dot ' + (connected ? '' : 'off') + '"></i>' + (busy ? '正在同步…' : connected ? '本地服务已连接' : '本地服务未连接') + '</div></header><div class="disclosure"><strong>规则安全门 · 合成数据 · 无真实资金动作</strong><span class="mono">生效流程 ' + esc(database.Active) + ' / 执行授权由代码控制</span></div>';
}
function versionHTML(entry) { return routingHTML(entry) + '<div class="version-pair"><span>案件固定版本 <b>' + esc(version(entry)) + '</b></span><span>当前生效 <b>' + esc(database.Active) + '</b></span>' + (version(entry) !== database.Active ? '<span>此案件继续使用原快照</span>' : '') + '</div>'; }
function caseItem(entry, activeID = selectedCase) {
  const lastUser = rows(entry.Messages).filter(message => message.Role === 'user').at(-1);
  return '<button type="button" class="case-item ' + (entry.ID === activeID ? 'active' : '') + '" data-case="' + esc(entry.ID) + '" ' + (entry.ID === activeID ? 'aria-current="true" ' : '') + (busy ? 'disabled' : '') + '><span class="case-item-top">' + badge(states[entry.State] || entry.State, entry.State === 'resolved' ? 'teal' : ['handoff', 'human'].includes(entry.State) ? 'coral' : '') + '<time>' + esc(dateText(entry.Updated)) + '</time></span><p>' + esc(lastUser?.Text || '等待客户描述问题') + '</p><span class="mono">' + esc(entry.OrderID || '不关联订单') + ' · ' + esc(version(entry)) + '</span></button>';
}
function railHTML() {
  return '<aside class="panel case-rail" aria-label="案件列表"><div class="panel-pad"><div class="section-head"><h2 class="rail-title">客户案件</h2><small>' + allCases().length + ' 件</small></div>' + button('＋ 新建案件', 'new', '', 'secondary wide') + '</div><div class="case-list">' + (allCases().length ? allCases().map(entry => caseItem(entry)).join('') : '<div class="rail-empty">还没有客户案件。<br>发送第一条消息，<br>从查询事实开始。</div>') + '</div><div class="rail-foot">同一份案件，贯穿服务全程。<br>发送时才创建案件；<br>已有案件保留固定版本。</div></aside>';
}
function sampleCard(sample, index) { return '<button type="button" class="sample-card" data-sample-index="' + index + '" ' + (busy || !connected ? 'disabled' : '') + '><span class="sample-label">' + esc(sample.Group || '合成场景') + ' · ' + esc(sample.ID) + '</span><strong>' + esc(sample.Text) + '</strong><small>' + esc(sample.OrderID || '不关联订单') + '</small><span class="sample-arrow" aria-hidden="true">↗</span></button>'; }
function welcomeHTML() {
  const samples = rows(database.Dataset.Samples);
  return '<section class="hero"><div class="hero-symbol" aria-hidden="true">↗</div><p class="eyebrow">AFTER-SALES, WITH A CLEAR NEXT STEP</p><h1>退票成功，<br><em>钱到哪一步了？</em></h1><p class="hero-copy">先核对订单与支付记录，再判断下一步。<br>需要追踪时，方案由你确认，任务由工具回执。</p><div class="hero-tags">' + badge('事实可查', 'teal') + badge('动作需授权', 'outline') + badge('回执可追溯', 'outline') + '</div><div class="intro-chain" aria-label="四阶段执行路径"><span>查询事实</span><i>→</i><span>规则核验</span><i>→</i><span>用户确认</span><i>→</i><span>工具回执</span></div></section><section class="sample-section" aria-labelledby="samples-title"><div class="sample-intro"><h2 id="samples-title" class="small">从一句客户诉求开始</h2><small class="muted">合成样例 · 点击填入，再发送</small></div><div class="sample-grid">' + (samples.length ? samples.slice(0, 3).map(sampleCard).join('') : '<p class="empty-mini">' + (connected ? '没有可用样例，可直接描述你的诉求。' : '连接本地服务后显示合成场景。') + '</p>') + '</div>' + (samples.length > 3 ? '<details class="more-samples"><summary>查看全部 ' + samples.length + ' 个合成样例 · 含边界与拒绝场景</summary><div class="sample-grid">' + samples.slice(3).map((sample, index) => sampleCard(sample, index + 3)).join('') + '</div></details>' : '') + '</section>';
}
function orderOptions() {
  const orders = rows(database.Dataset.Orders);
  return '<option value="" ' + (pendingOrder === '' ? 'selected' : '') + '>不关联订单</option>' + (!orders.some(order => order.ID === 'DEMO-RAIL-001') ? '<option value="DEMO-RAIL-001" ' + (pendingOrder === 'DEMO-RAIL-001' ? 'selected' : '') + '>DEMO-RAIL-001 · 默认演示订单</option>' : '') + orders.map(order => '<option value="' + esc(order.ID) + '" ' + (pendingOrder === order.ID ? 'selected' : '') + '>' + esc(order.ID + ' · ' + (order.Route || order.Business || '演示订单')) + '</option>').join('');
}
function composerHTML(entry) {
  const locked = entry?.State === 'resolved';
return '<footer class="composer-wrap">' + (!entry && reuseNotice ? '<div class="sample-context">下一次发送将新建案件，固定当前生效流程 ' + esc(database.Active) + '。发送后可核对复用依据与实际步骤。</div>' : '') + (!entry && pendingSample ? '<div class="sample-context">合成场景：' + esc(pendingSample.Group) + ' / ' + esc(pendingSample.ID) + '<br>下一新案件关联：' + esc(pendingOrder || '不关联订单') + ' · 来源：' + esc(textValue(database.Dataset.Source)) + '</div>' : '') + '<form id="message-form">' + (!entry ? '<div class="composer-meta"><label for="order-select"><span>新案件关联订单</span><select id="order-select" ' + (busy || !connected ? 'disabled' : '') + '>' + orderOptions() + '</select></label>' + badge('发送时才创建案件', 'outline') + '</div>' : '') + '<label for="message-text"><span>' + (locked ? '案件已关闭，可新建案件继续验证' : '客户消息') + '</span></label><div class="composer-input"><textarea id="message-text" placeholder="例如：退票成功了，但银行卡还没收到钱，请帮我查一下。" maxlength="2000" ' + (busy || !connected || locked ? 'disabled' : '') + ' required>' + esc(draftText) + '</textarea></div><div class="composer-bottom"><p>Enter 发送 · Shift + Enter 换行<br>不承诺到账，不修改资金状态。</p><button type="submit" class="primary" ' + (busy || !connected || locked ? 'disabled' : '') + '>' + (busy ? '处理中…' : '发送消息 ↗') + '</button></div></form></footer>';
}
function messagesHTML(entry) {
  const names = { user: '客户', assistant: '履约 Agent · 本地规则', agent: '人工坐席', human: '人工坐席', system: '系统记录' };
  return rows(entry.Messages).map(message => '<article class="message ' + (message.Role === 'user' ? 'user' : ['agent', 'human'].includes(message.Role) ? 'human' : '') + '"><div class="message-meta"><span>' + esc(names[message.Role] || message.Role || '服务记录') + '</span><time datetime="' + esc(message.At) + '">' + esc(dateText(message.At)) + '</time></div><div class="message-bubble">' + esc(message.Text) + '</div>' + llmExplanationHTML(entry, message) + '</article>').join('');
}
function planArtifact(entry, plan) { return plan?.ArtifactID || (entry?.ProposalID && database.Proposals[entry.ProposalID]?.Plan?.ID === plan?.ID ? entry.ProposalID : plan?.ID) || '尚无方案'; }
// 事实的来源标注：圆点颜色区分「谁说的」——客户陈述、工具核验、已发布流程、安全闸。
// 未核验的一律标出来，不把推测伪装成证据。
function factSource(key, entry) {
  const tools = rows(entry.Evidence);
  const ran = name => tools.some(item => item.Tool === name && item.Success);
  if (key === '你的诉求') return { label: '客户陈述', dot: 'cf-dot-user', verified: false };
  if (key === '复用流程') return { label: '已发布流程', dot: 'cf-dot-flow', verified: true };
  if (key === '安全闸') return { label: '安全闸命中', dot: 'cf-dot-risk', verified: true };
  if (key === '订单' || key === '退票状态') return { label: '订单查询', dot: 'cf-dot-tool', verified: ran('query_order') };
  if (key === '支付退款' || key === '金额') return { label: '支付轨迹', dot: 'cf-dot-tool', verified: ran('query_refund_trace') };
  return { label: '案件记录', dot: 'cf-dot-user', verified: false };
}

// 卡片相位：待确认 → 已确认 → 已执行，另有一去不回的作废态。
function planPhase(entry, plan) {
  if (entry.Receipt) return { phase: 'executed', label: '已执行 · 回执已生成' };
  const proposal = entry.ProposalID ? database.Proposals[entry.ProposalID] : null;
  if (proposal && (proposal.Status === 'cancelled' || proposal.Status === 'superseded')) return { phase: 'void', label: '已作废 · 不可执行' };
  if (entry.Selected === plan.ID) return { phase: 'confirmed', label: '已确认 · 待执行' };
  if (entry.State === 'plan' && plan.Token) return { phase: 'pending', label: '待你确认 · 未执行' };
  return { phase: 'readonly', label: '只读方案记录' };
}

// 生命周期轨道：三格一字排开，当前相位亮起；作废时整条灰掉并盖章。
function lifeRailHTML(phase) {
  const steps = [['pending', '待确认'], ['confirmed', '已确认'], ['executed', '已执行']];
  const order = { pending: 0, confirmed: 1, executed: 2, readonly: -1, void: -1 };
  const current = order[phase] ?? -1;
  return '<ol class="cf-life' + (phase === 'void' ? ' is-void' : '') + '" aria-label="方案卡生命周期"><li>' +
    steps.map(([key, label], index) => '<span class="cf-life-step' +
      (phase === 'void' ? '' : index === current ? ' is-current' : index < current ? ' is-done' : '') + '"><i aria-hidden="true"></i>' + esc(label) + '</span>').join('<em aria-hidden="true">→</em>') +
    '</li></ol>' + (phase === 'void' ? '<p class="cf-void-stamp">已作废</p>' : '');
}

function plansHTML(entry, readOnly = false) {
  const facts = [['你的诉求', rows(entry.Messages).find(message => message.Role === 'user')?.Text || '查询退款进度'], ...['订单', '退票状态', '支付退款', '金额', '复用流程', '安全闸'].filter(key => entry.Facts?.[key]).map(key => [key, entry.Facts[key]])];
  return rows(entry.Plans).map(plan => {
    const canConfirm = entry.State === 'plan' && !readOnly && Boolean(plan.Token) && !entry.Receipt;
    const phase = planPhase(entry, plan);
    const source = factSource('订单', entry);
    const fields = facts.map(([key, value]) => {
      const meta = factSource(key, entry);
      return '<div class="cf-field"><span class="cf-field-label">' + esc(key) + '</span><span class="cf-field-value">' + esc(textValue(value)) +
        '<span class="cf-source"><i class="' + meta.dot + '" aria-hidden="true"></i>' + esc(meta.label) +
        (meta.verified ? '' : ' · 未核验') + '</span></span></div>';
    }).join('');
    return '<article class="plan-card cf-card is-' + phase.phase + '">' +
      '<header class="cf-card-head"><span class="mono">' + esc(planArtifact(entry, plan)) + ' · ' + esc(entry.ID) + '</span>' +
      badge(phase.label, phase.phase === 'void' ? 'coral' : 'teal') + '</header>' +
      '<div class="cf-card-body">' +
      '<p class="cf-hud">先核对一下，你的问题与事实</p>' +
      '<div class="cf-fields">' + fields + '</div>' +
      '<p class="cf-note">来源圆点：' + esc(source.label) + '等证据由服务端工具核验；标注「未核验」的不是已证实事实。</p>' +
      '<p class="cf-hud">可以为你做的下一步</p>' +
      '<div class="cf-option' + (phase.phase === 'pending' ? ' is-pressed' : '') + '">' +
      '<div class="cf-option-top"><div><p class="cf-option-title">' + esc(plan.Title) + '</p>' +
      '<p class="cf-option-sub">流程 ' + esc(plan.ProcedureID || '未记录') + ' · 固定版本 ' + esc(version(entry)) + '</p></div>' +
      '<dl class="cf-option-meta"><dt>步骤</dt><dd>' + rows(plan.Steps).length + ' 步</dd><dt>性质</dt><dd>只读追踪</dd></dl></div>' +
      '<p class="cf-option-desc">' + esc(plan.Description) + '</p>' +
      '<details class="cf-basis"><summary>依据 · ' + esc(plan.Clause || '未记录') + '</summary>' +
      '<blockquote>' + esc(plan.Clause || '未记录') + '<footer>依据原文随卡片保存；没有依据的方案不会生成。</footer></blockquote></details>' +
      '</div>' +
      list(plan.Steps, 'cf-steps') +
      '<div class="plan-columns"><div><h4>可以帮到你</h4>' + list(plan.Benefits, 'plan-notes', false) + '</div><div><h4>你需要知道</h4>' + list(plan.Notes, 'plan-notes', false) + '</div></div>' +
      '<p class="tiny muted">仅执行方案列出的追踪步骤，不发起退款、不保证到账。</p>' +
      lifeRailHTML(phase.phase) +
      (entry.Receipt ? '<div class="cf-receipt"><span class="cf-receipt-label">追踪任务回执</span><span class="mono">' + esc(entry.Receipt) + '</span><span class="cf-receipt-note">这是追踪任务回执，不是退款到账回执。</span></div>' : '') +
      '<div class="button-row">' + (readOnly ? button('去客户案件，由客户确认', 'open_customer', 'data-id="' + esc(entry.ID) + '"', 'secondary', entry.State !== 'plan' || Boolean(entry.Receipt)) : button(canConfirm ? '这个方案可以，查看并确认' : entry.Selected === plan.ID ? '已确认此方案' : '方案记录 · 不可执行', 'show_confirm', 'data-plan-id="' + esc(plan.ID) + '"', 'primary', !canConfirm)) + '</div>' +
      '</div></article>';
  }).join('');
}
function artifactHTML(entry, change = null) {
  if (!entry && !change) return '';
  const planIDs = entry ? [...new Set(rows(entry.Plans).map(plan => planArtifact(entry, plan)))] : [];
  const taskIDs = entry ? tasksFor(entry).map(task => task.ID) : [];
  return '<div class="artifact-chain" aria-label="方案与履约产物来源链"><span>方案 ' + esc(change?.SourcePlan || planIDs.join(' / ') || entry?.ProposalID || '未形成') + '</span><i>→</i>' + (entry ? '<button type="button" data-action="open_customer" data-id="' + esc(entry.ID) + '" ' + (busy ? 'disabled' : '') + '>' + esc(entry.ID) + '</button>' : '<span>' + esc(change?.Source || '未记录案件') + '</span>') + '<i>→</i><span>' + (entry?.Selected ? '客户已确认' : '等待客户确认') + '</span><i>→</i><span>' + esc(change?.SourceTask || entry?.Receipt || taskIDs.join(' / ') || '尚无追踪回执') + '</span>' + (change ? '<i>→</i><span>' + esc(change.ID) + '</span><i>→</i><span>' + esc(change.Procedure?.ID || '未生成流程') + ' / ' + esc(typeof change.Snapshot === 'object' ? change.Snapshot?.ID || '已记录快照' : change.Snapshot || '未生成快照') + '</span>' : '') + '</div>';
}
// 承诺时钟：把"什么时候到"显示成一个会走的时间，而不是一句安慰。
// 超期只说"未到账、已主动同步"，绝不替支付状态宣布到账。
function durationText(ms) {
  const minutes = Math.max(1, Math.round(Math.abs(ms) / 60000));
  if (minutes < 60) return minutes + ' 分钟';
  const hours = Math.round(minutes / 60);
  if (hours < 48) return hours + ' 小时';
  return Math.round(hours / 24) + ' 天';
}
function promiseTask(entry) {
  if (!entry) return null;
  const tasks = tasksFor(entry);
  return tasks.find(task => task.DueAt) || (entry.Receipt ? database.Tasks?.[entry.Receipt] : null) || null;
}
function promiseHTML(entry) {
  const task = promiseTask(entry);
  if (!task || !task.DueAt) return '';
  const due = new Date(task.DueAt).getTime();
  if (Number.isNaN(due)) return '';
  const remaining = due - Date.now();
  const overdue = remaining <= 0;
  const when = new Date(task.DueAt).toLocaleString('zh-CN', { hour12: false });
  return '<section class="promise' + (overdue ? ' overdue' : '') + '" aria-label="承诺时钟"><div class="promise-head"><p class="eyebrow">PROMISE CLOCK / 承诺时钟</p>' + badge(overdue ? '已过承诺时间' : '承诺进行中', overdue ? 'coral' : 'teal') + '</div><h3>' + esc(when) + '</h3><p class="small">' + (overdue ? '已超期 ' + durationText(remaining) + '，系统已主动同步；当前仍为处理中，未到账。' : '距承诺到账还有 ' + durationText(remaining) + '。到点若未到账，系统会主动来找你，不用你追问。') + '</p><p class="tiny muted">' + esc(task.ID || '任务未记录') + ' · 任务提交不等于资金到账</p></section>';
}
function receiptHTML(entry) {
  const tasks = tasksFor(entry);
  if (!entry.Receipt && !tasks.length) return '';
  return '<section class="receipt"><p class="eyebrow">TRACKING TASK RECEIPT</p><h3>你的追踪任务已记录</h3>' + (entry.Receipt ? '<div class="receipt-id">' + esc(entry.Receipt) + '</div>' : '') + '<p class="small">这是追踪任务回执，不是退款回执，也不表示资金到账。</p>' + tasks.map(task => '<p class="tiny mono">' + esc(task.ID) + ' · ' + esc(task.State || '未记录') + '<br>来源方案 ' + esc(task.ProposalID || '未记录') + ' · ' + esc(dateText(task.At, true)) + '</p>').join('') + '</section>';
}
function caseActionsHTML(entry) {
  if (entry.State === 'plan') return '<div class="notice"><p>证据超过 5 分钟或需要重新核对时，先重新查询生成新方案；旧确认会作废，不会自动执行。</p><div class="button-row">' + button('重新核验并更新方案', 'refresh_plan') + button('需要人工协助', 'handoff', '', 'quiet') + '</div></div>';
  if (entry.State === 'resolved') return '<div class="notice"><h3>本次支持请求已处理，记录完整保留</h3><p>案件解决表示本次诉求处理完成，资金事实仍以查询记录为准。</p><div class="button-row">' + (entry.HumanHandled ? button('从本案起草候选流程', 'draft', '', 'primary') : '') + button('新建案件，验证流程复用', 'reuse') + '</div></div>';
  if (['handoff', 'human'].includes(entry.State)) return '<div class="notice coral"><h3>' + (entry.State === 'handoff' ? '需要人工接手核验' : '人工正在为你核验') + '</h3><p>人工核验后形成方案，仍需由你明确确认。</p><div class="button-row">' + button('打开人工接手', 'open_human') + '</div></div>';
  if (entry.State === 'waiting') return '<div class="button-row">' + button('进度已说明，追踪已提交', 'resolved', '', 'primary') + button('仍需处理', 'unresolved') + '</div>';
  if (entry.State === 'answered') {
    const canTrack = entry.Intent === 'refund_progress' && !entry.Risk && entry.Owner !== 'human' && entry.QueryOrder?.Business === 'rail' && entry.QueryOrder?.Ticket === 'SUCCESS' && entry.QueryOrder?.Payment === 'PROCESSING';
    return '<div class="notice"><p>只读查询已完成，没有创建任务或改动业务状态。你可以在此结束咨询；申请追踪仅请求形成方案，之后仍需明确确认。</p><div class="button-row">' + button('查询说明已足够', 'consultation_done', '', 'primary') + (canTrack ? button('需要继续跟进，申请追踪', 'request_tracking') : '') + button('仍需人工核验', 'handoff') + '</div></div>';
  }
  return '<div class="button-row">' + button('需要人工协助', 'handoff', '', 'quiet') + '</div>';
}
function caseHTML(entry) {
return '<section class="panel"><header class="case-header"><div class="section-head"><p class="eyebrow">CUSTOMER CASE / ' + esc(entry.ID) + '</p>' + badge(states[entry.State] || entry.State, ['handoff', 'human'].includes(entry.State) ? 'coral' : 'teal') + '</div><h1>' + esc(orderFor(entry)?.Route || '一次售后诉求，一条完整证据链') + '</h1><p class="tiny muted">' + esc(entry.OrderID || '不关联订单') + ' · ' + esc(entry.Intent || '待识别诉求') + '</p>' + versionHTML(entry) + presentationControlsHTML(entry) + '</header><div class="messages" aria-label="客户与服务对话">' + (messagesHTML(entry) || '<p class="empty-mini">请发送客户的实际诉求。</p>') + '</div><div class="case-content">' + plansHTML(entry) + promiseHTML(entry) + receiptHTML(entry) + '<div class="human-instruction">' + caseActionsHTML(entry) + '</div></div>' + composerHTML(entry) + '</section>';
}
function traceHTML(entry) {
  const evidence = rows(entry?.Evidence);
  const queried = evidence.some(item => /query|lookup|查询|事实/i.test(item.Tool || '') && item.Success === true);
  const policy = evidence.some(item => /policy|rule|规则|核验/i.test(item.Tool || '') && item.Success === true);
  if (entry && ['static_consultation', 'dynamic_consultation'].includes(entry.Route) && !entry.TrackingRequested && ['answered', 'resolved'].includes(entry.State)) {
    return '<section class="panel panel-pad"><h2 class="trace-title">本次是只读咨询</h2><p class="trace-sub">已依据' + (entry.Route === 'static_consultation' ? '演示政策版本' : '订单与支付工具回执') + '说明结果。</p><ol class="phase-list"><li class="phase done"><span class="phase-num">✓</span><div><strong>读取依据<span class="phase-status">' + (queried || policy ? '已记录' : '尚无有效证据') + '</span></strong><p>证据来源、查询时间或政策版本随案保留。</p></div></li><li class="phase"><span class="phase-num">—</span><div><strong>办理授权<span class="phase-status">本次不需要</span></strong><p>没有提交任务，不应把用户继续办理视为默认选择。</p></div></li><li class="phase"><span class="phase-num">—</span><div><strong>业务动作<span class="phase-status">未执行</span></strong><p>若主动申请追踪，将另行形成方案并等待明确确认。</p></div></li></ol></section>';
  }
  const phases = [
    ['查询事实', queried, '订单、退票与支付记录', '以实际查询结果为准'],
    ['规则核验', policy || rows(entry?.Plans).length > 0, '核验适用条件和动作权限', '核对规则记录与方案条款'],
    ['用户确认', Boolean(entry?.Selected), '客户明确授权方案', '形成方案后，由客户确认'],
    ['工具回执', Boolean(entry?.Receipt || (entry && tasksFor(entry).length)), '执行允许步骤并记录回执', '追踪任务不表示资金到账']
  ];
  const current = entry ? phases.findIndex(phase => !phase[1]) : -1;
  return '<section class="panel panel-pad"><h2 class="trace-title">每一步，都有来处</h2><p class="trace-sub">' + (entry ? '当前案件的事实 → 授权 → 行动' : '从客户问题到可核验的下一步') + '</p><ol class="phase-list">' + phases.map((phase, index) => '<li class="phase ' + (phase[1] ? 'done' : current === index ? 'current' : '') + '"><span class="phase-num" aria-hidden="true">' + (phase[1] ? '✓' : String(index + 1).padStart(2, '0')) + '</span><div><strong>' + phase[0] + '<span class="phase-status">' + (phase[1] ? '已记录' : entry ? '待完成' : '') + '</span></strong><p>' + (entry ? phase[3] : phase[2]) + '</p></div></li>').join('') + '</ol></section>';
}
function factsHTML(entry) {
  const facts = Object.entries(entry.Facts || {});
  const order = orderFor(entry);
  const payment = facts.filter(([key]) => /退款|支付|到账|payment|refund/i.test(key));
  const refundText = payment.length ? payment.map(([key, value]) => key + '：' + textValue(value)).join('；') : order?.Payment ? textValue(order.Payment) + '（合成订单字段）' : '尚无支付退款查询记录';
  return '<section class="panel panel-pad"><h2 class="trace-title">案件与资金，分别看</h2><div class="status-split"><div class="status-line"><span>案件状态</span><strong>' + esc(states[entry.State] || entry.State) + '</strong></div><div class="status-line"><span>支付 / 退款事实</span><strong>' + esc(refundText) + '</strong></div><div class="status-line"><span>退票记录</span><strong>' + esc(textValue(entry.Facts?.['退票状态'] || entry.Facts?.Ticket || order?.Ticket)) + '</strong></div></div><p class="status-caption">退票成功 ≠ 资金到账。案件关闭、流程发布和追踪回执均不改变退款事实。</p><details class="mini-detail"><summary>展开订单与全部核验事实</summary><dl class="fact-grid"><dt>订单</dt><dd>' + esc(entry.OrderID || '未关联') + '</dd>' + (order ? ['Business', 'Customer', 'Route', 'Channel', 'Amount'].map((key, index) => '<dt>' + ['业务', '客户', '行程', '渠道', '金额'][index] + '</dt><dd>' + esc(textValue(order[key])) + '</dd>').join('') : '') + facts.map(([key, value]) => '<dt>' + esc(key) + '</dt><dd>' + esc(textValue(value)) + '</dd>').join('') + '</dl></details></section>';
}
function eventsHTML(events) { return '<ol class="event-list">' + rows(events).map(event => '<li><time>' + esc(dateText(event.At, true)) + ' · ' + esc(event.Kind || '事件') + '</time>' + esc(event.Text || textValue(event)) + '</li>').join('') + '</ol>'; }
function evidenceHTML(entry) {
  const evidence = rows(entry.Evidence);
  return '<section class="panel panel-pad"><div class="section-head"><h2 class="trace-title">事实与动作证据</h2><small>' + evidence.length + ' 条</small></div><p class="tiny muted">合成数据来源：' + esc(textValue(database.Dataset.Source)) + '</p><div class="evidence-list">' + (evidence.length ? evidence.map(item => '<details class="evidence-item"><summary>' + esc(item.Tool || '工具记录') + ' ' + badge(item.Success === true ? '成功' : '未成功', item.Success === true ? 'teal' : 'coral') + '<time datetime="' + esc(item.At) + '">' + esc(dateText(item.At, true)) + '</time></summary><div class="evidence-body"><h4>输入</h4><pre>' + esc(textValue(item.Input)) + '</pre><h4>输出</h4><pre>' + esc(textValue(item.Output)) + '</pre></div><div class="evidence-meta">证据 ' + esc(item.ID) + '<br>来源 ' + esc(textValue(typeof item.Output === 'object' && item.Output ? item.Output.Source || database.Dataset.Source : database.Dataset.Source)) + '</div></details>').join('') : '<p class="empty-mini">尚无工具调用证据，不推断执行成功。</p>') + '</div><details class="mini-detail"><summary>演示故障注入</summary><p class="source-copy">只影响本地合成工具。</p><label for="fault-select"><span>故障类型</span><select id="fault-select" ' + (busy ? 'disabled' : '') + '>' + [['none', '正常运行'], ['query', '查询失败'], ['create', '追踪任务创建失败']].map(([key, label]) => '<option value="' + key + '" ' + ((faultChoice[entry.ID] || entry.Fault || 'none') === key ? 'selected' : '') + '>' + label + '</option>').join('') + '</select></label><div class="button-row">' + button('应用故障设置', 'fault') + '</div><p class="tiny muted">服务记录：' + esc(entry.Fault || 'none') + '</p></details></section>';
}
function decisionHTML(entry) {
  const proposals = Object.values(database.Proposals).filter(proposal => proposal.CaseID === entry.ID);
  return '<details class="panel details-card"><summary>判定/执行链路</summary><p class="source-copy">意图：' + esc(entry.Intent || '未判定') + '<br>规则决策：' + esc(entry.Decision || '未记录') + '<br>风险：' + esc(textValue(entry.Risk)) + '</p><h4>本案产物来源链</h4>' + artifactHTML(entry) + '<h4>实际执行步骤</h4>' + list(entry.ActualSteps) + '<h4>当前事件链</h4>' + (rows(entry.Events).length ? eventsHTML(entry.Events) : '<p class="empty-mini">尚无事件记录</p>') + '<h4>持久化方案</h4>' + (proposals.length ? proposals.map(proposal => '<p class="source-copy mono">' + esc(proposal.ID) + ' · ' + esc(proposal.Status) + '<br>流程 ' + esc(proposal.ProcedureID || '未记录') + ' / ' + esc(typeof proposal.Snapshot === 'object' ? proposal.Snapshot?.ID || '固定快照' : proposal.Snapshot || '未记录') + '<br>确认 ' + esc(proposal.ConfirmedAt ? dateText(proposal.ConfirmedAt, true) : '未确认') + ' · 回执 ' + esc(proposal.Receipt || '无') + '</p>').join('') : '<p class="empty-mini">尚无持久化方案记录</p>') + '<details class="mini-detail"><summary>前后端结构示意 · 非执行结果</summary><div class="architecture"><span>客户消息 / 样例</span><i>→</i><span>本地 API</span><i>→</i><span>查询工具 + 规则核验</span><i>→</i><span>持久化方案</span><i>→</i><span>客户确认 + 方案令牌校验</span><i>→</i><span>追踪工具 / 任务回执</span></div><p class="tiny muted">人工走同一查询与规则引擎。只有确认请求可执行方案；JEV 为可选影子对照，无执行权限。上方证据与事件才是本案实际结果。</p></details>' + (rows(entry.Judgments).length ? '<details class="mini-detail"><summary>本案可选 JEV 影子判定</summary>' + judgmentsHTML(entry.Judgments) + '</details>' : '') + '</details>';
}
function sourceHTML() {
  const dataset = database.Dataset;
  const policy = dataset.Policy || {};
  return '<details class="panel details-card"><summary>场景来源与权限边界</summary><p class="source-copy">全部为合成演示场景。来源：' + esc(textValue(dataset.Source)) + '<br>数据版本 ' + esc(textValue(dataset.Version)) + ' · 规则 ' + esc(policy.ID || '未记录') + ' / ' + esc(policy.Version || '未记录') + '</p><div class="table-wrap"><table class="source-table"><caption>场景分类 · 数据源记录</caption><thead><tr><th scope="col">分类 / 情景</th><th scope="col">分级</th><th scope="col">允许动作</th><th scope="col">执行边界</th></tr></thead><tbody>' + rows(dataset.Categories).slice(0, 5).map(category => '<tr><td>' + esc(category.Scenario) + '<br><span class="mono">' + esc(category.ID) + '</span></td><td>' + esc(category.Level) + '</td><td>' + esc(category.Action) + '</td><td>' + esc(category.Boundary) + '</td></tr>').join('') + '</tbody></table></div>' + (!rows(dataset.Categories).length ? '<p class="empty-mini">等待服务提供场景分类。</p>' : '') + '<details class="mini-detail"><summary>规则允许项、禁止项与成功标准</summary><h4>允许步骤</h4>' + list(policy.AllowedSteps, 'steps', false) + '<h4>禁止事项</h4>' + list(policy.Forbidden, 'stop-list', false) + '<h4>成功标准</h4><p class="source-copy">' + esc(textValue(policy.Success)) + '</p></details></details>';
}
function agentHTML() {
  const entry = record();
  return '<main id="main" tabindex="-1" class="workspace">' + railHTML() + (entry ? caseHTML(entry) : '<section class="panel">' + welcomeHTML() + composerHTML(null) + '</section>') + '<aside class="side-stack" aria-label="证据与执行链">' + traceHTML(entry) + (entry ? factsHTML(entry) + evidenceHTML(entry) + decisionHTML(entry) : '<section class="panel panel-pad"><p class="eyebrow">A BOUNDED AGENT</p><h2 class="trace-title" style="margin-top:10px">把能做的事，做到有回执。</h2><p class="source-copy">规则说明事实，人工核验例外，由你决定是否创建追踪任务。每次动作都留在同一份案件中。</p><div class="version-pair"><span>新案件将固定 <b>' + esc(database.Active) + '</b></span></div></section>') + sourceHTML() + '</aside></main>';
}

function humanHTML() {
  const entry = record();
  return '<main id="main" class="module"><header class="module-heading"><div><p class="eyebrow">SAME CASE, NO REPEATED QUESTIONS</p><h1>人工接着处理，不从头问起。</h1><p class="muted small">已有事实、客户方案与授权链完整移交。坐席同样不能绕过政策和客户确认。</p></div></header><div class="human-layout"><aside class="panel human-queue"><div class="section-head"><h2>案件接手队列</h2></div><div class="case-list">' + (allCases().map(item => caseItem(item)).join('') || '<p class="empty-mini">先在客户案件发送第一条退款进度请求。</p>') + '</div></aside>' + (!entry ? '<section class="panel empty-state"><h2>选择案件，继续处理</h2><p>不是复制一段聊天给人工，而是带着订单、证据、尝试记录接着办理。</p>' + button('从客户诉求开始', 'new', '', 'primary') + '</section>' : '<section class="panel human-detail"><div class="human-top"><div><p class="eyebrow">' + esc(entry.ID) + '</p><h2>' + esc(entry.OrderID || '尚未提供订单') + '</h2></div>' + badge(states[entry.State], 'teal') + '</div>' + versionHTML(entry) + '<p class="source-copy">交接原因：' + esc(entry.Decision || '无') + '<br>客户原始诉求：' + esc(rows(entry.Messages).find(item => item.Role === 'user')?.Text || '未记录') + '</p>' + artifactHTML(entry) + '<div class="human-transcript">' + messagesHTML(entry) + '</div><div class="button-row">' + (entry.State === 'handoff' ? button('接手同一案件', 'accept', '', 'primary') : '') + (entry.State === 'human' && !entry.Receipt ? button('核验并形成追踪方案', 'human_prepare', '', 'primary') : '') + button('查看客户方案与证据', 'open_customer', 'data-id="' + esc(entry.ID) + '"') + '</div><p class="source-copy">先查询退款轨迹、核验适用条件，形成客户可见的方案；不会通过坐席按钮直接创建任务。</p>' + plansHTML(entry, true) + receiptHTML(entry) + (entry.State === 'human' ? '<form id="human-form" class="human-form"><label for="human-text"><span>人工补充说明（文字不等于工具执行）</span><textarea id="human-text" maxlength="2000" required ' + (busy ? 'disabled' : '') + '>' + esc(humanText) + '</textarea></label><div class="button-row"><button class="secondary" type="submit" ' + (busy ? 'disabled' : '') + '>回复到同一案件</button></div></form>' : '') + (entry.State === 'resolved' && entry.HumanHandled ? '<div class="notice"><h3>已取得用户结果确认</h3><p>这次支持请求完成，支付状态仍未改写。</p><div class="button-row">' + button('从本案起草候选流程', 'draft', '', 'primary') + '</div></div>' : '') + '<details class="mini-detail"><summary>查看实际操作与事件</summary>' + list(entry.ActualSteps) + eventsHTML(entry.Events) + '</details></section>') + '</div></main>';
}

function judgmentsHTML(observations) {
  return judgmentTableHTML(observations) + rows(observations).map(observation => observation.RequestPayload ? '<details class="mini-detail"><summary>实际请求 · ' + esc(observation.QuestionVersion || '历史版本') + ' · ' + esc(observation.SampleID || '合成样例') + '</summary><pre>' + esc(textValue(observation.RequestPayload)) + '</pre><p class="tiny">不含密钥或请求头；原始答案保留，不以预设标签替换。</p></details>' : '').join('');
}
// 八个判定点与各自的采用门槛。门槛不是拍脑袋定的：
// 转人工不可逆 → 0.85 最高；安全漏判代价极高 → 0.50 最低且判不出按有风险处理。
const JEV_ORDER = ['refund_progress', 'human_requested', 'plan_response', 'result_confirmed', 'safety_signal', 'emotion_level', 'arrival_status', 'returning_issue'];
const JEV_POINTS = {
  refund_progress: { label: '意图强度', threshold: 0.80 },
  human_requested: { label: '转人工', threshold: 0.85 },
  plan_response: { label: '方案回应', threshold: 0.80 },
  result_confirmed: { label: '结案确认', threshold: 0.80 },
  safety_signal: { label: '危险信号', threshold: 0.50 },
  emotion_level: { label: '情绪等级', threshold: 0.70 },
  arrival_status: { label: '到账状态', threshold: 0.80 },
  returning_issue: { label: '老问题', threshold: 0.70 }
};

function confidenceRow(id, answer) {
  const point = JEV_POINTS[id] || { label: id, threshold: 0.80 };
  const value = Number(answer && answer.Confidence) || 0;
  const percent = Math.round(value * 100);
  const gate = Math.round(point.threshold * 100);
  const adopted = value >= point.threshold;
  return '<div class="cf-meter' + (adopted ? ' is-adopted' : '') + '">' +
    '<span class="cf-meter-label">' + esc(point.label) + '</span>' +
    '<span class="cf-meter-track"><i style="width:' + percent + '%"></i><b style="left:' + gate + '%"></b></span>' +
    '<span class="cf-meter-value">' + value.toFixed(2) + '</span>' +
    '<span class="cf-meter-state">' + (adopted ? '采用' : '未用') + '</span>' +
    '</div>';
}

function judgmentMetersHTML(observation) {
  const answers = (observation && observation.Answers) || {};
  const present = JEV_ORDER.filter(id => answers[id]);
  if (!present.length) return '<p class="tiny muted">本次判定未返回题面结果</p>';
  let adopted = 0;
  const meters = present.map(id => {
    const value = Number(answers[id].Confidence) || 0;
    if (value >= JEV_POINTS[id].threshold) adopted++;
    return confidenceRow(id, answers[id]);
  }).join('');
  return meters + '<p class="tiny muted">竖线是采用门槛，过线才当真。' +
    adopted + ' / ' + present.length + ' 条过线，未过线的只写进账本备查，不代替用户动作。</p>';
}

function judgmentTableHTML(observations) {
  const names = { yes: '是', no: '否', unclear: '不确定' };
return '<div class="table-wrap"><table class="jev-table"><thead><tr><th>合成提问 / 状态</th><th>退款进度 / 人工意愿</th><th>实际调用资源</th></tr></thead><tbody>' + rows(observations).map(observation => '<tr><td>' + judgeBaselineHTML(observation) + esc(observation.Text) + '<br>' + badge(observation.Status, observation.Status === 'ok' ? 'teal' : 'coral') + '<small>' + esc(observation.Error || '') + '</small></td><td>' + judgmentMetersHTML(observation) + '</td><td>' + esc(observation.LatencyMS) + ' ms<br>tokens ' + esc(observation.InputTokens) + ' / ' + esc(observation.OutputTokens) + '<br>' + esc(observation.Model) + '</td></tr>').join('') + '</tbody></table></div>';
}

function jevHTML() {
  const status = database.JEV || {};
  return '<details class="panel details-card"><summary>JEV 判定与对照 · ' + esc({off:'关闭',shadow:'影子对照',assist:'辅助分流'}[status.Mode] || '关闭') + '</summary><p class="source-copy">八个判定点放在一次请求里并发提交，等待时间只算一次。只加闸：判定只能让系统更保守，永不放行——概率不够就不采用，最坏是让用户多点一下，而不是产生一个越权动作。安全题判不出来按有风险处理。</p><p class="tiny muted">模式 ' + esc(status.Mode || 'off') + ' / 模型 ' + esc(status.Model || '未配置') + '<br>配置状态：' + esc(judgeReason(status.Reason)) + '</p><div class="button-row">' + button('对合成提问运行在线对照', 'judge_compare', '', 'secondary', !status.Ready) + '</div>' + (rows(database.JudgeReport).length ? judgeSummaryHTML(database.JudgeReport) + judgmentsHTML(database.JudgeReport) : '<p class="source-copy">尚无在线模型结果。启用需服务端密钥和合成数据外发许可；本地模拟接口测试不等于真实 JEV 评测。</p>') + '<p class="tiny muted">判定概率与 confidence 分开记录，不以 confidence 代替业务正确率；无独立样本和账单，不宣称已降本。</p></details>';
}

function replayHTML(change) {
  if (!rows(change.Replay).length) return '<p class="empty-mini">尚未回放。系统将隔离运行当前基线和候选资产，不预写改善结论。</p>';
  return '<div class="table-wrap"><table class="replay-table"><thead><tr><th>合成用例</th><th>首次 / 当前基线</th><th>候选流程</th><th>校验</th></tr></thead><tbody>' + change.Replay.map(row => '<tr><td>' + esc(row.Name) + '</td><td>' + esc(states[row.Before] || row.Before) + '<small class="replay-label">期望 ' + esc(states[row.ExpectedBefore] || row.ExpectedBefore) + '</small><details><summary>实际步骤</summary>' + list(row.BeforeSteps) + '</details></td><td>' + esc(states[row.After] || row.After) + '<small class="replay-label">期望 ' + esc(states[row.ExpectedAfter] || row.ExpectedAfter) + '</small><details><summary>实际步骤</summary>' + list(row.AfterSteps) + '</details></td><td>' + badge(row.Passed ? '通过' : '未通过', row.Passed ? 'teal' : 'coral') + '</td></tr>').join('') + '</tbody></table></div>';
}

function evolutionHTML() {
  const change = changeRecord();
  const source = change ? database.Cases[change.Source] : null;
  const procedure = change?.Procedure || {};
  const readonly = ['active', 'rolled_back'].includes(change?.Status);
  return '<main id="main" class="module"><header class="module-heading"><div><p class="eyebrow">FROM ONE HANDLED CASE TO A REUSABLE PROCEDURE</p><h1>教会下一次，如何把事办下去。</h1><p class="muted small">积累可审核的处理方法，而不是一条“好答案”。单例只证明本地闭环，不代表统计效果。</p></div><div class="button-row">' + button('新建案件验证实际复用', 'reuse', '', 'primary') + '</div></header><div class="evolution-layout"><aside class="stack"><section class="panel change-rail"><div class="section-head"><h2>流程资产</h2>' + badge(database.Active, 'teal') + '</div><div class="change-list">' + rows(database.Changes).map(item => '<button class="change-item ' + (item.ID === change?.ID ? 'active' : '') + '" data-change="' + esc(item.ID) + '"><strong>' + esc(item.Title) + '</strong><small>' + esc(changeStates[item.Status]) + '</small><span class="mono">' + esc(item.ID) + '</span></button>').join('') + '</div><p class="source-copy">草稿 → 回放 → 人批 → 激活 → 可回滚<br>回滚只改变新案件版本，不删除已创建的任务。</p></section>' + jevHTML() + '</aside>' + (!change ? '<section class="panel empty-state"><h2>先完成一次真实的沙箱处理</h2><p>客户提问 → 同案人工核验 → 客户确认方案 → 工具创建任务 → 用户确认结果。随后才能起草候选，不从聊天自动“学会”。</p>' + button('开始第一个客户案件', 'new', '', 'primary') + '</section>' : '<section class="panel change-detail"><header class="change-title"><div><p class="eyebrow">' + esc(change.ID) + '</p><h2>' + esc(change.Title) + '</h2></div>' + badge(changeStates[change.Status], 'teal') + '</header><p class="change-meta">基线 ' + esc(change.Base) + ' / 发布版本 ' + esc(change.Snapshot || '未发布') + '</p>' + artifactHTML(source, change) + comparisonHTML(change) + '<div class="support">来源支持数 ' + esc(procedure.Support) + ' · 仅合成人工案例，不代表泛化或生产验证</div><div class="procedure-card"><h3>有边界的可执行流程</h3><div class="condition-grid">' + [['业务', procedure.Business], ['意图', procedure.Intent], ['退票状态', procedure.Ticket], ['支付状态', procedure.Payment], ['渠道', procedure.Channel], ['来源案件', procedure.Source]].map(([label, value]) => '<div class="condition"><small>' + label + '</small><strong>' + esc(value) + '</strong></div>').join('') + '</div><h4>白名单步骤</h4>' + list(procedure.Steps) + '<h4>禁用条件</h4>' + list(procedure.StopConditions, 'stop-list', false) + '<h4>成功标准</h4><p class="source-copy">' + esc(procedure.Success) + '</p></div><form id="note-form" class="note-form"><label for="change-note"><span>人工审核说明（修改后旧回放和批准失效）</span><textarea id="change-note" maxlength="4000" ' + (readonly || busy ? 'disabled' : '') + '>' + esc(noteText[change.ID] ?? change.Note) + '</textarea></label>' + (!readonly ? '<div class="button-row"><button type="submit" class="secondary" ' + (busy ? 'disabled' : '') + '>保存说明，旧回放失效</button></div>' : '') + '</form><div class="stage-actions button-row">' + (!readonly ? button('运行基线 / 候选回放', 'change_replay') + button('人工批准', 'change_approve', '', 'secondary', change.Status !== 'replayed') + button('激活到本地沙箱', 'change_publish', '', 'primary', change.Status !== 'approved') : change.Status === 'active' ? button('回滚当前流程版本', 'rollback', '', 'danger', database.Active !== change.Snapshot) : badge('已回滚，历史记录保留', 'outline')) + '</div><section class="replay-section"><h3>同一引擎，实际前后对比</h3><p class="tiny muted">适用案例必须从人工转为可确认方案；范围外权限不能变化。回放不写入客户业务库。</p>' + replayHTML(change) + '</section><details class="hashes mini-detail"><summary>查看资产绑定与审计</summary><p class="mono">候选 ' + esc(change.Hash) + '<br>回放 ' + esc(change.ReplayHash || '无') + '<br>绑定 ' + esc(change.Binding || '无') + '</p>' + eventsHTML(change.Events) + '</details></section>') + '</div></main>';
}

function render() {
  root.setAttribute('aria-busy', String(busy));
  root.innerHTML = (busy ? '<div class="loading-bar" aria-hidden="true"></div>' : '') + headerHTML() + (errorText ? '<div class="error-bar" role="alert"><span>' + esc(errorText) + '</span>' + button('重新读取', 'refresh') + '</div>' : '') + (view === 'human' ? humanHTML() : view === 'evolution' ? evolutionHTML() : agentHTML());
}

async function requestAPI(input = null) {
  const response = await fetch('/api/state', input ? { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) } : { cache: 'no-store' });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || '服务暂时不可用');
  database = { ...database, ...result };
  connected = true;
  return result;
}
async function perform(input, after) {
  if (busy) return;
  busy = true; errorText = ''; render();
  try { await requestAPI(input); if (after) after(); }
  catch (error) { errorText = error.message; notify(error.message); }
  finally { busy = false; render(); }
}
async function sendMessage() {
  const text = draftText.trim();
  if (!text || busy) return;
  busy = true; errorText = ''; render();
  try {
    if (!record()) {
      const previous = new Set(Object.keys(database.Cases));
      await requestAPI({ Action: 'new', OrderID: pendingOrder });
      setCase(Object.keys(database.Cases).find(id => !previous.has(id)) || '');
    }
    await requestAPI({ Action: 'message', CaseID: selectedCase, Text: text });
    draftText = ''; pendingSample = null;
  } catch (error) { errorText = error.message; notify(error.message); }
  finally { busy = false; render(); }
}

function showConfirmation(planID) {
  const entry = record();
  const plan = rows(entry?.Plans).find(item => item.ID === planID);
  if (!entry || entry.State !== 'plan' || !plan) return;
  confirmation = { CaseID: entry.ID, PlanID: plan.ID, PlanToken: plan.Token };
  confirmDialog.innerHTML = '<div class="dialog-body"><p class="eyebrow">YOUR CONFIRMATION, BEFORE ANY ACTION</p><h2 id="confirm-title">确认提交追踪请求？</h2><p class="source-copy">' + esc(plan.Title) + '<br>订单 ' + esc(entry.OrderID) + '<br>方案卡 ' + esc(planArtifact(entry, plan)) + '</p><div class="notice"><p>只创建沙箱追踪任务，不办理退票、退款或承诺到账。执行前会重新查询，状态不符即停止。</p></div><p id="confirm-error" role="alert" class="dialog-error"></p><div class="dialog-actions"><button class="secondary" id="cancel-confirm">先不提交</button><button class="primary" id="submit-confirm">确认，为本订单创建追踪任务</button></div></div>';
  confirmDialog.showModal();
  const refresh = document.createElement('button');
  refresh.type = 'button';
  refresh.className = 'secondary';
  refresh.id = 'refresh-confirm';
  refresh.textContent = '重新核验，生成新方案';
  confirmDialog.querySelector('.dialog-actions').prepend(refresh);
}
confirmDialog.addEventListener('click', async event => {
  if (event.target.closest('#refresh-confirm') && !busy && confirmation) {
    const caseID = confirmation.CaseID;
    confirmation = null;
    confirmDialog.close();
    await perform({ Action: 'refresh_plan', CaseID: caseID });
    return;
  }
  if (event.target.closest('#cancel-confirm')) { confirmation = null; confirmDialog.close(); }
  if (!event.target.closest('#submit-confirm') || busy || !confirmation) return;
  confirmDialog.querySelector('#submit-confirm').disabled = true;
  const input = { Action: 'confirm', ...confirmation, Confirmed: true };
  await perform(input, () => { confirmDialog.close(); confirmation = null; });
  const submit = confirmDialog.querySelector('#submit-confirm');
  if (submit) submit.disabled = false;
  if (confirmDialog.open && errorText) confirmDialog.querySelector('#confirm-error').textContent = errorText;
});

root.addEventListener('input', event => {
  if (event.target.id === 'message-text') draftText = event.target.value;
  if (event.target.id === 'human-text') humanText = event.target.value;
  if (event.target.id === 'change-note' && changeRecord()) noteText[changeRecord().ID] = event.target.value;
});
root.addEventListener('change', event => {
  if (event.target.id === 'order-select') { pendingOrder = event.target.value; pendingSample = null; }
  if (event.target.id === 'fault-select' && record()) faultChoice[selectedCase] = event.target.value;
});
root.addEventListener('keydown', event => {
  if (event.target.id === 'message-text' && event.key === 'Enter' && !event.shiftKey && !event.isComposing) { event.preventDefault(); sendMessage(); }
});
root.addEventListener('submit', async event => {
  event.preventDefault();
  if (event.target.id === 'message-form') return sendMessage();
  if (event.target.id === 'human-form') return perform({ Action: 'human_reply', CaseID: selectedCase, Text: humanText }, () => { humanText = ''; });
  if (event.target.id === 'note-form') return perform({ Action: 'change_edit', ChangeID: changeRecord().ID, Text: noteText[changeRecord().ID] ?? changeRecord().Note });
});
root.addEventListener('click', async event => {
  const target = event.target.closest('button');
  if (!target || target.disabled || busy) return;
  if (target.dataset.judgeMode) return perform({ Action: 'judge_mode', Text: target.dataset.judgeMode });
  if (target.dataset.nav) return navigate(target.dataset.nav);
  if (target.dataset.case) { setCase(target.dataset.case); draftText = ''; render(); return; }
  if (target.dataset.change) { selectedChange = target.dataset.change; saveSession('ota.selectedChange', selectedChange); render(); return; }
  if (target.dataset.sampleIndex !== undefined) {
    pendingSample = database.Dataset.Samples[Number(target.dataset.sampleIndex)];
    draftText = pendingSample.Text; pendingOrder = pendingSample.OrderID; render(); root.querySelector('#message-text')?.focus(); return;
  }
  const action = target.dataset.action;
  if (action === 'client_preview') return openPresentation('client');
  if (action === 'system_map') return openPresentation('system');
  if (action === 'new' || action === 'reuse') return newDraft(action === 'reuse');
  if (action === 'refresh') return perform(null);
  if (action === 'open_human') return navigate('human');
  if (action === 'open_customer') { if (target.dataset.id) setCase(target.dataset.id); draftText = ''; return navigate('agent'); }
  if (action === 'show_confirm') return showConfirmation(target.dataset.planId);
  if (action === 'judge_compare') return perform({ Action: action });
  if (action?.startsWith('change_') || action === 'rollback') return perform({ Action: action, ChangeID: changeRecord().ID });
  if (['handoff', 'accept', 'human_prepare', 'request_tracking', 'refresh_plan', 'consultation_done', 'resolved', 'unresolved', 'draft', 'fault'].includes(action)) return perform({ Action: action, CaseID: selectedCase, ...(action === 'fault' ? { Text: faultChoice[selectedCase] || record().Fault || 'none' } : {}) }, () => {
    if (action === 'draft') { selectedChange = database.Changes.at(-1)?.ID || ''; view = 'evolution'; history.replaceState(null, '', '#evolution'); }
    if (action === 'human_prepare' && record()?.State === 'plan') { view = 'agent'; history.replaceState(null, '', '#agent'); }
  });
});
window.addEventListener('hashchange', () => { view = Object.hasOwn(tabs, location.hash.slice(1)) ? location.hash.slice(1) : 'agent'; render(); });
perform(null);

// 承诺时钟的静默同步：不打断操作，只让倒计时继续走，并在到期时把系统的主动同步带回来。
// 拉取的是同一个只读状态接口，不产生任何业务动作。
async function syncPromiseClock() {
  if (busy || !connected || !selectedCase) return;
  const before = (database.Cases?.[selectedCase]?.Messages || []).length;
  try {
    const result = await requestAPI(null);
    const fresh = result?.Cases?.[selectedCase];
    if (!fresh) return;
    const added = (fresh.Messages || []).slice(before);
    if (added.some(item => item.Role !== 'user')) {
      notify('承诺时钟：系统主动同步了一条进度更新');
      render();
      return;
    }
    if (promiseTask(fresh)?.DueAt) render();
  } catch (error) {
    connected = false;
  }
}
setInterval(() => { void syncPromiseClock(); }, 20000);
