'use strict';

// Narrative copy is kept here so the next content pass does not need to touch layout.
const CHAPTERS = [
  {id:'background',en:'01 / THE PROBLEM',title:'售后的难点，是不确定。',lead:'从用户的三个追问出发，解释 ONpoint 为什么选择售后履约。'},
  {id:'experience',en:'02 / THE EXPERIENCE',title:'三类售后问题，看见三种确定性。',lead:'查询有事实，办理有授权，异常有人接。选择一个常见问题，亲自核验处理过程。'},
  {id:'architecture',en:'03 / THE SYSTEM',title:'从产品全景，到系统实现。',lead:'用两张全景图理解设计，再沿实际消息链路核对当前实现。'},
  {id:'decision',en:'04 / THE DECISION',title:'从一句建议，到一张可核验的方案。',lead:'把事实、依据、动作与授权放在同一个决策对象里。'},
  {id:'evolution',en:'05 / THE EVOLUTION',title:'这次办过的，下次接着用。',lead:'从人工处理轨迹中沉淀流程，经回放与审核，再进入下一次服务。'}
];
const NODE_CONTENT = {
  intake:{number:'01',title:'分诊理解',desc:'先区分用户在问什么、想不想继续办理，以及是否需要人工。',input:'用户消息、案件当前状态。',output:'业务意图、办理意愿、人工或风险分流。',boundary:'JEV 为可选增强；本次案例使用规则路径，不展示虚构模型概率。',files:['engine.go','judge_router.go','jev.go']},
  rules:{number:'02',title:'规则与证据',desc:'从订单和支付工具中取事实，再检查业务允许做什么。',input:'合成订单号、版本化政策、工具查询结果。',output:'可核验事实、适用条件与不允许办理的原因。',boundary:'用户自述保留为线索，不会自动变成支付事实。',files:['rules.go','fixtures/rules.json','engine.go']},
  plan:{number:'03',title:'方案与确认',desc:'把下一步签发为方案卡，向用户说清楚动作与限制。',input:'事实快照、已发布流程、规则核验结果。',output:'绑定订单、方案和证据的确认令牌。',boundary:'聊天中的“好的”不代替按钮确认；无有效令牌不执行。',files:['types.go / Plan','engine.go / offer','engine.go / confirm']},
  execute:{number:'04',title:'受控执行',desc:'真正执行之前，再查一次事实；重复确认返回同一份任务。',input:'用户确认、当前方案令牌、重新核验的订单状态。',output:'沙箱追踪任务与 TR 回执，或失败/人工交接记录。',boundary:'提交追踪不等于发起退款，也不改写支付状态。',files:['engine.go / confirm','types.go / Task']},
  store:{number:'05',title:'状态与回执',desc:'把用户消息、事实证据、方案与任务保存在同一案件中。',input:'工具结果、用户动作与每一步事件。',output:'案件、证据、方案、任务和版本快照。',boundary:'前端按回放帧呈现同样的 Store 结构，当前没有实时模型调用。',files:['storage.go','types.go / Store']},
  evolution:{number:'06',title:'回放与复用',desc:'从成功的人工处理里形成候选，检查下一次能否安全复用。',input:'完整人工轨迹、任务回执与用户结果确认。',output:'回放记录、人工批准、发布快照及回滚入口。',boundary:'当前只支持固定七步模板；发布不会替用户确认，也不会撤销历史任务。',files:['evolution.go','types.go / Change','types.go / Snapshot']}
};
const ANATOMY = {
  facts:'事实来自工具核验。用户表达、订单状态与支付状态分别标注。',
  scope:'方案只包含当前规则允许的动作，并把“不做什么”放在同一视线内。',
  consent:'确认绑定当前订单、方案和证据。执行之前，后端还会重新核验。',
  proof:'回执证明追踪任务已提交；资金是否到账，仍以支付回执为准。'
};
const $ = (selector,root=document)=>root.querySelector(selector);
const $$ = (selector,root=document)=>[...root.querySelectorAll(selector)];
const esc = value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const data = window.ONPOINT_REPLAY;
let route='home',scenario='inquiry',frameKey='start',perspective='customer',evoIndex=0,toastTimer,flowTimer,evoTimer;
const frames = data?.frames || {};
const stateLabels={intake:'等待输入',answered:'事实已核验',plan:'等待用户确认',waiting:'追踪已提交',resolved:'本次支持已完成',handoff:'等待人工',human:'人工处理中',clarify:'等待补充信息'};
const stepLabels={query_order:'核验退票记录',query_refund_trace:'核验支付轨迹',explain_status:'说明当前进度',request_confirmation:'等待明确确认',create_tracking:'创建追踪任务',show_receipt:'提供任务回执',ask_result:'确认处理结果'};
const toolLabels={query_order:'订单查询',query_refund_trace:'支付轨迹',read_policy:'政策读取',create_tracking:'追踪任务'};
const connection={mode:'replay',store:null,caseID:null,busy:false,error:''};
const EXAMPLES={
 inquiry:{group:'inquiry',name:'退款进度查询',question:'票退了，钱咋还没回来？帮我查查',order:'DEMO-RAIL-001',start:'start',result:'query',pain:'只收到“请耐心等待”，不知道钱在哪一步。',advantage:'订单与支付分别核验，回答带来源；只查询不自动建任务。'},
 paid:{group:'inquiry',name:'核对已到账回执',question:'我想核对一下退款进度',order:'DEMO-RAIL-003',start:'paid_start',result:'paid',pain:'用户感知与支付回执可能不同，不能凭一句话推断。',advantage:'读取支付回执如实说明，已到账就不再创建处理中追踪。'},
 policy:{group:'inquiry',name:'退款政策与时效',question:'退票退款规则是什么，能保证什么时候到账吗？',order:'DEMO-RAIL-001',start:'policy_start',result:'policy',pain:'政策说明容易被理解成到账承诺。',advantage:'静态政策与动态订单事实分开，不编造银行到账时间。'},
 reuse:{group:'tracking',name:'申请退款追踪',question:'票退了，钱咋还没回来？帮我查查',order:'DEMO-RAIL-001',start:'start',result:'query',pain:'解释完进度，用户还要自己找人继续办理。',advantage:'查询后可申请追踪；出方案、等确认、执行前重查，最后给任务回执。'},
 human:{group:'tracking',name:'人工接手继续办',question:'票退了，钱咋还没回来？帮我查查',liveQuestion:'转人工客服帮我查退款进度',order:'DEMO-RAIL-001',start:'human_start',result:'human_query',pain:'转人工后重讲一遍、重新找证据。',advantage:'坐席沿同一案件接着核验，方案仍由客户确认，处理轨迹可回查。'},
 risk:{group:'handoff',name:'投诉与责任争议',question:'我要投诉退款并追究责任',order:'DEMO-RAIL-001',start:'risk_start',result:'risk',pain:'把投诉误当普通退款查询，可能继续给出不合适方案。',advantage:'识别投诉与责任诉求，保留上下文交人工，不自动赔付或认定责任。'},
 unknown:{group:'handoff',name:'订单或支付状态未知',question:'帮我看一下退票退款进度',order:'DEMO-RAIL-004',start:'unknown_start',result:'unknown',pain:'工具查不清时，模板回答仍可能给出确定结论。',advantage:'查不到就如实说明并交人工核验，不编造进度、不硬执行。'},
 invoice:{group:'handoff',name:'未接入业务：发票办理',question:'火车票怎么开发票？能直接帮我开吗',order:'DEMO-RAIL-001',start:'invoice_start',result:'invoice',pain:'识别出了业务，就误以为已经具备办理权限。',advantage:'明确区分“听懂了”和“能办理”，未开放接口时带上下文转人工。'}
};

function notify(text){$('#toast').textContent=text;$('#toast').classList.add('visible');clearTimeout(toastTimer);toastTimer=setTimeout(()=>$('#toast').classList.remove('visible'),3600);}
function frame(key){if(key===undefined&&connection.mode==='live'&&connection.store?.Cases[connection.caseID])return {caseID:connection.caseID,store:connection.store,record:connection.store.Cases[connection.caseID]};const f=frames[key||frameKey];return f?{...f,record:f.store.Cases[f.caseID]}:null;}
function stepTo(key){if(!frames[key]){notify('回放数据未加载，请重新打开完整文件夹中的 index.html。');return;}frameKey=key;renderExperience();}

function navigate(){
  const wanted=location.hash.slice(1)||'home';
  route=CHAPTERS.some(c=>c.id===wanted)?wanted:'home';
  $('#home').hidden=route!=='home';$('#detail').hidden=route==='home';
  if(route==='home'){document.title='ONpoint · 让售后有确定的下一步';return;}
  const index=CHAPTERS.findIndex(c=>c.id===route),ch=CHAPTERS[index];
  $('#page-eyebrow').textContent=ch.en;$('#page-title').textContent=ch.title;$('#page-lead').textContent=ch.lead;
  $('#chapter-number').innerHTML=String(index+1).padStart(2,'0')+'<span>/05</span>';
  $$('[data-panel]').forEach(el=>el.hidden=el.dataset.panel!==route);
  $$('[data-chapter]').forEach(el=>{const active=el.dataset.chapter===route;el.classList.toggle('active',active);if(active)el.setAttribute('aria-current','page');else el.removeAttribute('aria-current');});
  $('#next-chapter').textContent=index===4?'返回通道 ↗':'下一章 →';
  document.title=ch.title+' · ONpoint';
  window.scrollTo({top:0,behavior:'instant'});
  if(route==='experience')renderExperience();
  if(route==='decision')renderShowcase();
  if(route==='evolution')renderEvolution();
}
addEventListener('hashchange',navigate);
$('#next-chapter').addEventListener('click',()=>{const i=CHAPTERS.findIndex(c=>c.id===route);location.hash=i===4?'home':CHAPTERS[i+1].id;});
$$('[data-open-guide]').forEach(button=>button.addEventListener('click',()=>$('#guide-modal').showModal()));
document.addEventListener('keydown',event=>{
  if(event.target.closest('input,textarea,select')||$('dialog[open]'))return;
  if(event.key>='1'&&event.key<='5'){event.preventDefault();location.hash=CHAPTERS[Number(event.key)-1].id;}
  if(event.key==='Escape'){location.hash='home';}
  if(route!=='home'&&(event.key==='ArrowLeft'||event.key==='ArrowRight')){const i=CHAPTERS.findIndex(c=>c.id===route),next=i+(event.key==='ArrowRight'?1:-1);if(next>=0&&next<5)location.hash=CHAPTERS[next].id;}
});

function actionButton(label,action,primary=true){return `<button class="${primary?'primary':'secondary'}" data-demo-action="${action}">${esc(label)} <span>→</span></button>`;}
function scenarioStart(){return EXAMPLES[scenario].start;}
async function chooseExample(id){if(connection.busy)return;scenario=id;perspective='customer';if(connection.mode==='live')await liveOperation(newLiveCase);else stepTo(scenarioStart());}
$$('[data-category]').forEach(button=>button.addEventListener('click',()=>{const id=Object.keys(EXAMPLES).find(key=>EXAMPLES[key].group===button.dataset.category);void chooseExample(id);}));
$('#case-examples').addEventListener('click',event=>{const button=event.target.closest('[data-example]');if(button)void chooseExample(button.dataset.example);});
$$('[data-perspective]').forEach(button=>button.addEventListener('click',()=>{perspective=button.dataset.perspective;renderExperience();}));
$$('[data-reset]').forEach(button=>button.addEventListener('click',()=>{void chooseExample(scenario);}));

function renderExperience(){
  const f=frame();if(!f){$('#messages').innerHTML='<p>回放数据加载失败。请保留 replay-data.js 与页面在同一目录。</p>';return;}
  const r=f.record,s=f.store,task=s.Tasks[r.Receipt];
  const example=EXAMPLES[scenario],group=example.group;
  $$('[data-category]').forEach(el=>el.classList.toggle('active',el.dataset.category===group));
  $('#case-examples').innerHTML='<p class="small-label">代表案例 · 点击切换</p>'+Object.entries(EXAMPLES).filter(([,v])=>v.group===group).map(([key,v])=>`<button data-example="${key}" class="${key===scenario?'active':''}" ${connection.busy?'disabled':''}>${esc(v.name)} <span>↗</span></button>`).join('');
  const live=connection.mode==='live';
  $('#data-mode').innerHTML=`<span class="status-dot"></span> ${live?'后端实时返回 · 合成订单':'后端录制 · 合成订单'}`;
  $('#backend-toggle').textContent=connection.busy?'正在请求…':live?'切回稳定回放':'连接 ONpoint 后端';
  $('#backend-toggle').disabled=connection.busy;
  $('.detail-footer span').innerHTML=`ONpoint <i>／</i> 合成案例 · ${live?'后端实时沙箱':'本地回放'} · 无真实资金动作`;
  $('#case-brief').innerHTML=`<div><span>${group==='inquiry'?'查清楚 · 查询与咨询':group==='tracking'?'办到位 · 追踪与办理':'接得住 · 异常与人工'}</span><strong>“${esc(live&&example.liveQuestion?example.liveQuestion:example.question)}”</strong></div><p>${esc(example.pain)}</p><small>${live?'实时读取':'录制于'} ONpoint · ${s.Dataset.Orders.length} 个合成订单${live?'':` · ${esc(data.commit.slice(0,7))}`}</small>`;
  const proof=r.Receipt?'已返回任务回执，资金状态未被改写。':r.State==='plan'?'方案已就绪，尚未确认，因此没有执行。':r.State==='handoff'||r.State==='human'?'已进入人工路径，没有越权创建任务。':r.Evidence.length?`已返回 ${r.Evidence.length} 条工具证据，可展开核验来源。`:'点击发送案例，观察后端实际处理结果。';
  $('#case-value').innerHTML=`<section class="case-benefit"><span>这个案例，优势在哪里</span><p>${esc(example.advantage)}</p><strong>${esc(proof)}</strong></section>`;
  $$('[data-perspective]').forEach(el=>el.classList.toggle('active',el.dataset.perspective===perspective));
  $('#case-caption').textContent=`${r.OrderID} · 固定流程 ${r.Snapshot}`;
  $('#case-state').textContent=stateLabels[r.State]||r.State;
  const progress=r.Receipt?4:r.State==='plan'?2:r.QueryOrder?1:0;
  $('#experience-progress').innerHTML=['查询事实','核验方案','用户确认','任务回执'].map((t,i)=>`<span class="${i<progress?'done':''}"><i>${i<progress?'✓':i+1}</i>${t}</span>`).join('');
  $('#messages').hidden=perspective!=='customer';$('#inline-plan').hidden=perspective!=='customer';
  $('#messages').innerHTML=r.Messages.length?r.Messages.map(m=>`<article class="message ${m.Role==='user'?'user':['human','agent'].includes(m.Role)?'human':''}"><small>${m.Role==='user'?'客户':['human','agent'].includes(m.Role)?'人工坐席':'ONpoint · 后端结果'}</small><div>${esc(m.Text)}</div></article>`).join(''):`<div class="chat-empty"><div class="empty-symbol">↗</div><h3>${esc(example.name)}</h3><p>发送这条代表问句，<br>观察事实、分流与可执行的下一步。</p></div>`;
  const plan=r.Plans?.[0];
  $('#inline-plan').innerHTML=plan&&!r.Receipt?`<div class="mini-plan"><p>PLAN / 已核验 · 待确认</p><h3>申请追踪退款进度</h3><small>${esc(plan.Description)}</small><div class="mini-facts"><span>退票：成功</span><span>支付：处理中</span><span>流程：${esc(r.Snapshot)}</span></div><small>确认后创建沙箱任务，不发起退款，不保证到账。</small></div>`:'';
  if(perspective==='human'){
    $('#perspective-content').innerHTML=`<div class="human-summary"><p class="eyebrow">SAME CASE / 同案交接</p><h3>${r.Owner==='human'?'人工正在接着这份案件处理':'交接时，带着证据一起走'}</h3><p><strong>客户诉求：</strong>${esc(r.Messages.find(m=>m.Role==='user')?.Text||'等待客户输入')}</p><p><strong>已核验事实：</strong>${esc(r.Facts['退票状态']||'尚未查询')}；${esc(r.Facts['支付退款']||'尚未查询')}</p><p><strong>已尝试：</strong>${esc((r.ActualSteps||[]).map(x=>stepLabels[x]||x).join(' → ')||'尚未执行')}</p><p><strong>交接原因：</strong>${esc(r.Facts['交接原因']||'当前使用已发布流程，无需人工接管')}</p><p><strong>权限边界：</strong>坐席仍要核验事实、提出方案，由客户确认执行。</p></div>`;
  }else if(perspective==='evidence'){
    $('#perspective-content').innerHTML=r.Events.map(e=>`<div class="view-record"><small>${esc(e.Kind)}</small>${esc(e.Text)}</div>`).join('');
  }else $('#perspective-content').innerHTML='';
  const facts=[['订单状态',r.Facts['退票状态']||'尚未查询','来源：订单查询'],['支付事实',r.Facts['支付退款']||'尚未查询','来源：支付轨迹'],['当前处理',stateLabels[r.State]||r.State,'案件状态，与资金状态分别记录']];
  $('#facts').innerHTML=facts.map(([label,value,source])=>`<div class="fact-row"><span>${esc(label)}</span><strong class="${label==='当前处理'?'positive':''}">${esc(value)}</strong><small>${esc(source)}</small></div>`).join('');
  $('#receipt').innerHTML=task?`<div class="receipt-card"><p>TOOL RECEIPT / 沙箱任务回执</p><strong>${esc(task.ID)}</strong><small>任务状态：${esc(task.State)}<br>支付退款：仍按实际回执更新</small></div>`:'';
  $('#evidence-count').textContent=r.Evidence.length+' 条';
  $('#evidence-list').innerHTML=r.Evidence.map(e=>`<div class="evidence-record"><b>${esc(toolLabels[e.Tool]||e.Tool)} · ${e.Success?'成功':'失败'}</b><p>${esc(e.ID)}</p><span>${esc(e.Output)}</span></div>`).join('')||'<p class="muted">尚未调用工具。</p>';
  let actions='';
  if(r.State==='intake')actions=actionButton('发送这个案例问题','query');
  if(r.State==='answered'){
    if(r.Intent==='refund_progress'&&r.QueryOrder?.Payment==='PROCESSING'&&r.QueryOrder?.Ticket==='SUCCESS')actions+=actionButton('申请追踪，查看方案','track');
    actions+=actionButton('只需查询，完成咨询','consultation',false);
  }
  if(r.State==='plan')actions=actionButton('核对方案并确认','confirm')+actionButton('查看方案依据','explain',false);
  if(r.State==='handoff')actions=actionButton('坐席接手同一案件','accept');
  if(r.State==='human')actions=r.Risk===''&&r.Intent==='refund_progress'&&(!r.QueryOrder||(r.QueryOrder.Ticket==='SUCCESS'&&r.QueryOrder.Payment==='PROCESSING'))?actionButton('人工核验并提出方案','prepare'):'<span class="hint">坐席已接手，原因与证据随案保留；当前不满足普通追踪的执行条件。</span>';
  if(r.State==='waiting'&&r.Receipt)actions=actionButton('确认本次追踪请求已处理','resolve');
  if(r.State==='resolved')actions=r.Receipt?'<span class="hint">支持请求完成，支付状态仍以实际回执为准。</span>'+actionButton('看经验如何复用','evolution',false):'<span class="hint">本次咨询完成，没有创建任务或改变资金状态。</span>';
  if(r.State==='clarify')actions='<span class="hint">后端需要补充信息。实时模式可继续输入；回放模式可切换代表案例。</span>';
  $('#action-area').innerHTML=`<div class="action-row">${actions}</div>`;
  $('#demo-input').placeholder=live?'直接输入问题，发送到 ONpoint 后端':'输入代表问句，或点击发送案例';
  $('#demo-input').disabled=connection.busy||r.State==='resolved';
  $('#demo-form button').disabled=connection.busy||r.State==='resolved';
  $$('#action-area button').forEach(button=>button.disabled=connection.busy);
  $('.composer-hint').textContent=live?'实时请求 /api/state · 仅操作合成订单和本地沙箱任务':'离线回放 · 示例：'+example.question;
  requestAnimationFrame(()=>{const box=$('#conversation-scroll');if(perspective==='customer'&&plan&&!r.Receipt){box.scrollTop=box.scrollTop+$('#inline-plan').getBoundingClientRect().top-box.getBoundingClientRect().top-2;}else box.scrollTop=perspective==='customer'?box.scrollHeight:0;});
}

async function demoAction(action){
  if(connection.busy)return;
  if(action==='confirm'){
    $('#confirm-modal').returnValue='';$('#confirm-modal small').textContent=connection.mode==='live'?'将向后端提交当前方案的确认令牌，仅创建本地沙箱任务。':'本操作播放后端已生成的合成案例记录。';$('#confirm-modal').showModal();return;
  }
  if(action==='explain'){location.hash='decision';return;}
  if(action==='evolution'){location.hash='evolution';return;}
  if(connection.mode==='live'){
    const actions={query:'message',track:'request_tracking',accept:'accept',prepare:'human_prepare',resolve:'resolved',consultation:'consultation_done'};
    await liveOperation(async()=>{await sendLive({Action:actions[action],CaseID:connection.caseID,...(action==='query'?{Text:EXAMPLES[scenario].liveQuestion||EXAMPLES[scenario].question}:{})});if(action==='accept')perspective='human';if(action==='prepare')perspective='customer';});return;
  }
  switch(action){
    case 'query':stepTo(EXAMPLES[scenario].result);break;
    case 'track':stepTo(scenario==='human'?'human_handoff':'plan');break;
    case 'accept':perspective='human';stepTo(scenario==='human'?'human_accept':scenario+'_accepted');break;
    case 'prepare':perspective='customer';stepTo('human_plan');break;
    case 'resolve':stepTo(scenario==='human'?'human_resolved':'resolved');break;
    case 'consultation':stepTo(['inquiry','reuse'].includes(scenario)?'consultation':scenario==='human'?'human_consultation':scenario+'_done');break;
    case 'explain':location.hash='decision';break;
    case 'evolution':location.hash='evolution';break;
  }
}
$('#action-area').addEventListener('click',event=>{const button=event.target.closest('[data-demo-action]');if(button)demoAction(button.dataset.demoAction);});
$('#confirm-modal').addEventListener('close',()=>{if($('#confirm-modal').returnValue==='confirm'){perspective='customer';if(connection.mode==='live'){void liveOperation(async()=>{const r=frame().record,p=r.Plans[0];if(!p)throw new Error('当前方案已失效，请重新核验。');await sendLive({Action:'confirm',CaseID:r.ID,PlanID:p.ID,PlanToken:p.Token,Confirmed:true});notify('后端已处理确认，任务结果以右侧回执为准。');});}else{stepTo(scenario==='human'?'human_receipt':'receipt');notify('已载入后端确认执行后的任务回执。');}}});
$('#demo-form').addEventListener('submit',async event=>{
  event.preventDefault();const input=$('#demo-input'),text=input.value.trim();if(!text)return;
  if(connection.mode==='live'){await liveOperation(async()=>{await sendLive({Action:'message',CaseID:connection.caseID,Text:text});input.value='';});return;}
  const matched=Object.entries(EXAMPLES).find(([,v])=>v.question===text);
  if(matched){scenario=matched[0];perspective='customer';stepTo(matched[1].result);input.value='';return;}
  if(text==='申请追踪'&&(frameKey==='query'||frameKey==='human_query')){demoAction('track');input.value='';return;}
  notify('当前为固定案例回放。请使用示例问句或流程按钮；自由对话需要连接后端。');
});

async function requestAPI(input){
  if(!/^https?:$/.test(location.protocol))throw new Error('实时体验需要从 ONpoint 服务的 /story/index.html 打开；当前单文件可继续回放。');
  const response=await fetch('/api/state',{method:input?'POST':'GET',headers:input?{'Content-Type':'application/json'}:{},body:input?JSON.stringify(input):undefined,signal:AbortSignal.timeout(12000),cache:'no-store'});
  let result;try{result=await response.json();}catch{throw new Error('当前页面未连接 ONpoint API，请运行附带的实时体验启动脚本。');}
  if(!response.ok)throw new Error(result.error||`后端返回 ${response.status}`);
  if(!result.Cases||!result.Dataset?.Orders||!result.Tasks)throw new Error('后端返回的数据结构不符合 ONpoint Store。');
  return result;
}
async function sendLive(input){const store=await requestAPI(input);connection.store=store;return store;}
async function newLiveCase(){
  const before=new Set(Object.keys(connection.store.Cases));
  const store=await sendLive({Action:'new',OrderID:EXAMPLES[scenario].order});
  const added=Object.keys(store.Cases).filter(id=>!before.has(id));
  if(added.length!==1)throw new Error('未能唯一识别新案件，请重新开始本案例。');
  connection.caseID=added[0];
}
async function liveOperation(operation){
  if(connection.busy)return;connection.busy=true;connection.error='';renderExperience();
  try{await operation();}catch(error){connection.error=error.message;notify(error.message);}
  finally{connection.busy=false;renderExperience();}
}
$('#backend-toggle').addEventListener('click',async()=>{
  if(connection.mode==='live'){connection.mode='replay';connection.caseID=null;stepTo(scenarioStart());notify('已切回后端录制数据，适合稳定录像。');return;}
  await liveOperation(async()=>{
    const store=await requestAPI();
    if(!store.Dataset.Orders.every(order=>order.ID.startsWith('DEMO-')))throw new Error('此演示入口仅开放合成订单沙箱，未切换到真实业务写入。');
    connection.store=store;connection.mode='live';
    try{await newLiveCase();}catch(error){connection.mode='replay';throw error;}
    notify('已连接 ONpoint 后端。接下来的查询、方案和任务回执均为实时返回。');
  });
});

function selectNode(id){
  const node=NODE_CONTENT[id];if(!node)return;
  $$('[data-node]').forEach(el=>{el.classList.toggle('active',el.dataset.node===id);el.setAttribute('aria-pressed',String(el.dataset.node===id));});
  $('#node-detail').innerHTML=`<p class="eyebrow">NODE ${node.number} / 输入与输出</p><h2>${esc(node.title)}</h2><p class="node-desc">${esc(node.desc)}</p><dl><dt>输入</dt><dd>${esc(node.input)}</dd><dt>输出</dt><dd>${esc(node.output)}</dd><dt>边界</dt><dd>${esc(node.boundary)}</dd></dl><div>${node.files.map(f=>`<span class="source-chip">${esc(f)}</span>`).join('')}</div>`;
}
$$('[data-node]').forEach(button=>button.addEventListener('click',()=>{clearInterval(flowTimer);$('#run-flow').disabled=false;selectNode(button.dataset.node);}));
$('#run-flow').addEventListener('click',()=>{
  const ids=['intake','rules','plan','execute','store','evolution'];let i=0;clearInterval(flowTimer);$('#run-flow').disabled=true;selectNode(ids[i]);
  flowTimer=setInterval(()=>{i++;if(i>=ids.length){clearInterval(flowTimer);$('#run-flow').disabled=false;return;}selectNode(ids[i]);const el=$(`[data-node="${ids[i]}"]`);el.classList.remove('flow-pulse');void el.offsetWidth;el.classList.add('flow-pulse');},1000);
});

function renderShowcase(){
  const f=frame('plan');if(!f)return;const r=f.record,plan=r.Plans[0],receiptFrame=frame('receipt'),task=receiptFrame.store.Tasks[receiptFrame.record.Receipt];
  $('#showcase-plan').innerHTML=`<div class="plan-top"><span>PLAN / ${esc(plan.ArtifactID)}</span><span class="badge">待确认 · 未执行</span></div><h3>申请追踪退款进度</h3><p>先核对事实，再确认你希望我们做的下一步。</p><section class="plan-region highlight" data-region="facts"><small>01 / 已核验事实</small><div class="plan-facts"><span>退票记录<strong>${esc(r.Facts['退票状态'])}</strong></span><span>支付退款<strong>处理中，暂无到账回执</strong></span></div></section><section class="plan-region" data-region="scope"><small>02 / 方案与依据</small><p>${esc(plan.Description)}</p><p class="subtext">依据：${esc(plan.Clause)} · 固定流程 ${esc(r.Snapshot)}</p></section><section class="plan-region" data-region="consent"><small>03 / 由你明确确认</small><button class="primary" id="showcase-confirm">这个方案可以，查看确认 <span>→</span></button><p class="subtext">确认绑定当前订单与方案；执行前会重新核验。</p></section><section class="plan-region" data-region="proof" hidden><small>04 / 执行后的回执</small><p>任务号：${esc(task.ID)}</p><p class="subtext">任务已提交 · 支付仍为处理中，未被标为到账。</p></section>`;
  $('#showcase-confirm').addEventListener('click',async()=>{scenario='reuse';perspective='customer';if(connection.mode==='live'){await liveOperation(async()=>{await newLiveCase();await sendLive({Action:'message',CaseID:connection.caseID,Text:EXAMPLES.reuse.question});await sendLive({Action:'request_tracking',CaseID:connection.caseID});});if(frame().record.State!=='plan')return;}else frameKey='plan';location.hash='experience';void demoAction('confirm');});
  selectAnatomy('facts');
}
function selectAnatomy(id){
  $$('[data-anatomy]').forEach(el=>{el.classList.toggle('active',el.dataset.anatomy===id);el.setAttribute('aria-pressed',String(el.dataset.anatomy===id));});
  $$('[data-region]').forEach(el=>{el.classList.toggle('highlight',el.dataset.region===id);if(el.dataset.region==='proof')el.hidden=id!=='proof';});
  const consent=$('[data-region="consent"]');if(consent)consent.hidden=id==='proof';
  const phase=$('.plan-top .badge');if(phase)phase.textContent=id==='proof'?'执行后 · 已取得回执':'待确认 · 未执行';
  $('#anatomy-note').textContent=ANATOMY[id];
}
$$('[data-anatomy]').forEach(button=>button.addEventListener('click',()=>selectAnatomy(button.dataset.anatomy)));

const EVO_STEPS=[{key:'human_resolved',name:'人工完成',status:'来源已具备',title:'先有真实的操作轨迹',desc:'进度已说明、任务有回执，并由用户确认本次请求完成。聊天文字本身不构成流程资产。'}, {key:'evo_draft',name:'形成候选',status:'草稿 · 尚未生效',title:'从一次处理，形成流程候选',desc:'候选关联来源案件、方案与任务；适用范围和七步安全顺序受到约束。'}, {key:'evo_replayed',name:'隔离回放',status:'回放通过',title:'先让候选接受历史用例检验',desc:'对照原版本与候选版本，检查业务范围、确认门与工具故障路径。'}, {key:'evo_approved',name:'人工批准',status:'已批准 · 待发布',title:'通过测试之后，还要经过人批',desc:'批准与当前候选和回放记录绑定；批准不等于已经在新案件生效。'}, {key:'evo_active',name:'发布复用',status:'v2 已发布',title:'新案件，开始读取已审核流程',desc:'新案固定发布快照并读取流程，旧案保留原版本；用户确认与执行前核验继续生效。'}];
function renderEvolution(){
  const item=EVO_STEPS[evoIndex],f=frame(item.key),r=f.record,ch=f.store.Changes[0],task=f.store.Tasks[r.Receipt];
  $('#evolution-steps').innerHTML=EVO_STEPS.map((step,i)=>`<button class="evolution-step ${i<=evoIndex?'active':''}" data-evo="${i}" data-index="${String(i+1).padStart(2,'0')}" aria-pressed="${i===evoIndex}">${step.name}</button>`).join('');
  const passed=ch?.Replay?.filter(x=>x.Passed).length||0,total=ch?.Replay?.length||0;
  $('#asset-passport').innerHTML=`<p class="eyebrow">PROCEDURE PASSPORT / 流程资产护照</p><h3>${item.title}</h3><p>${item.desc}</p><div class="passport-grid"><span>来源案件<strong>${esc(r.ID)}</strong></span><span>成功工具回执<strong>${esc(task?.ID||'尚未取得')}</strong></span><span>候选流程<strong>${esc(ch?.Procedure.ID||'尚未起草')}</strong></span><span>${total?'本次隔离回放':'支持样本'}<strong>${total?`${passed} / ${total} 项通过`:'1 个合成人工处理案例'}</strong></span></div><div class="passport-state"><span>${item.status}</span><span>0${evoIndex+1} / 05</span></div>`;
  $$('[data-evo]').forEach(button=>button.addEventListener('click',()=>{clearInterval(evoTimer);$('#replay-evolution').disabled=false;evoIndex=Number(button.dataset.evo);renderEvolution();}));
}
$('#replay-evolution').addEventListener('click',()=>{clearInterval(evoTimer);evoIndex=0;renderEvolution();$('#replay-evolution').disabled=true;evoTimer=setInterval(()=>{evoIndex++;renderEvolution();if(evoIndex===4){clearInterval(evoTimer);$('#replay-evolution').disabled=false;}},1600);});

// A light pixel tunnel, inspired by the supplied reference. No remote assets.
let architectureView='product';
function selectArchitecture(id){
  architectureView=id;
  $$('[data-architecture]').forEach(button=>{button.classList.toggle('active',button.dataset.architecture===id);button.setAttribute('aria-pressed',String(button.dataset.architecture===id));});
  $('#diagram-section').hidden=id==='runtime';$('#runtime-architecture').hidden=id!=='runtime';
  if(id!=='runtime'){
    $('#architecture-diagram').innerHTML=window.ONPOINT_DIAGRAMS?.[id]||'<p>架构图尚未加载，请保留 diagrams.js。</p>';
    $('#diagram-caption').textContent=id==='product'?'四大 Agent、智能体配置、应用场景与 MVP 闭环。':'应用配置、Agent 框架、模型与数据源，按原分层结构呈现。';
  }
}
$$('[data-architecture]').forEach(button=>button.addEventListener('click',()=>selectArchitecture(button.dataset.architecture)));
$('#expand-diagram').addEventListener('click',()=>{
  $('#diagram-modal-title').textContent=architectureView==='product'?'产品全景视图':'系统架构';
  $('#diagram-modal-scroll').innerHTML=window.ONPOINT_DIAGRAMS[architectureView];
  $('#diagram-modal-scroll svg').style.width='100%';$('#diagram-modal').showModal();
});
$('#close-diagram').addEventListener('click',()=>$('#diagram-modal').close());
$$('[data-diagram-zoom]').forEach(button=>button.addEventListener('click',()=>{$('#diagram-modal-scroll svg').style.width=Number(button.dataset.diagramZoom)*100+'%';}));
selectArchitecture('product');
const canvas=$('#portal'),ctx=canvas.getContext('2d'),reduced=matchMedia('(prefers-reduced-motion: reduce)').matches;
let canvasW=0,canvasH=0,raf,lastPaint=0;
function sizePortal(){const rect=canvas.getBoundingClientRect(),dpr=Math.min(devicePixelRatio||1,1.5);canvasW=rect.width;canvasH=rect.height;canvas.width=canvasW*dpr;canvas.height=canvasH*dpr;ctx.setTransform(dpr,0,0,dpr,0,0);}
function drawPortal(now){
  if(now-lastPaint<42&&!reduced){raf=requestAnimationFrame(drawPortal);return;}lastPaint=now;
  if(route!=='home'||document.hidden){raf=requestAnimationFrame(drawPortal);return;}
  ctx.clearRect(0,0,canvasW,canvasH);const cx=canvasW/2,cy=canvasH*.52,time=reduced?0:now*.000035;
  for(let ring=0;ring<32;ring++){
    const depth=((ring/32+time)%1),radius=12+depth*depth*Math.min(canvasW*.36,390),alpha=(1-depth)*.28+.025;
    for(let dot=0;dot<64;dot++){
      const angle=dot/64*Math.PI*2+depth*.44+time*.8,x=cx+Math.cos(angle)*radius,y=cy+Math.sin(angle)*radius*.77;
      const side=1+depth*2.3;ctx.fillStyle=`rgba(162,237,185,${alpha})`;ctx.fillRect(x,y,side,side);
    }
  }
  const shade=ctx.createRadialGradient(cx,cy,5,cx,cy,120);shade.addColorStop(0,'rgba(9,15,13,.7)');shade.addColorStop(1,'rgba(9,15,13,0)');ctx.fillStyle=shade;ctx.fillRect(0,0,canvasW,canvasH);
  if(!reduced)raf=requestAnimationFrame(drawPortal);
}
addEventListener('resize',sizePortal);
selectNode('intake');navigate();sizePortal();raf=requestAnimationFrame(drawPortal);
