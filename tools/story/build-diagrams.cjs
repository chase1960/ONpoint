// Editable SVG reconstruction of the two user-supplied architecture layouts.
const fs=require('node:fs'),path=require('node:path');
const out=path.join(__dirname,'dist');
const C={ink:'#e6f1e9',muted:'#9ab8a4',line:'#42604b',panel:'#13251a',cell:'#274a33',core:'#8ed3a4',model:'#c5ceaa',data:'#afdcc0'};
const escape=s=>String(s).replace(/[&<>]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;'}[c]));
let parts=[];
function rect(x,y,w,h,fill=C.cell,stroke=C.line,rx=10){parts.push(`<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="${rx}" fill="${fill}" stroke="${stroke}" stroke-width="1.5"/>`);}
function text(x,y,value,size=18,color=C.ink,weight=400,anchor='middle'){const lines=Array.isArray(value)?value:[value];parts.push(`<text x="${x}" y="${y}" fill="${color}" font-size="${size}" font-weight="${weight}" text-anchor="${anchor}" font-family="Microsoft YaHei,PingFang SC,sans-serif">${lines.map((s,i)=>`<tspan x="${x}" dy="${i?size*1.45:0}">${escape(s)}</tspan>`).join('')}</text>`);}
function cell(x,y,w,h,label,core=false,size=18){rect(x,y,w,h,core?C.core:'url(#block)',core?'#b9f1ca':C.line);text(x+w/2,y+h/2+(Array.isArray(label)?-5:7),label,size,core?'#15331f':C.ink,core?700:400);}
function line(x1,y1,x2,y2,dashed=false){parts.push(`<path d="M${x1} ${y1}H${x2}" fill="none" stroke="${C.line}" stroke-width="1.5" ${dashed?'stroke-dasharray="5 4"':''}/>`);}
function begin(title){parts=[`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 2048 1152" role="img" aria-label="${title}"><title>${title}</title><defs><linearGradient id="bg" x2="1" y2="1"><stop stop-color="#172c1e"/><stop offset="1" stop-color="#090f0d"/></linearGradient><linearGradient id="block" x2="0" y2="1"><stop stop-color="#30563c"/><stop offset="1" stop-color="#203d2a"/></linearGradient><pattern id="grid" width="112" height="112" patternUnits="userSpaceOnUse"><path d="M112 0H0V112" fill="none" stroke="#8ed3a4" stroke-opacity=".035"/></pattern></defs><rect width="2048" height="1152" rx="12" fill="url(#bg)"/><rect width="2048" height="1152" fill="url(#grid)"/>`];text(38,80,title,38,C.ink,700,'start');}
function end(){return parts.join('')+'</svg>';}
function panel(x,y,w,h,title){rect(x,y,w,h,C.panel);text(x+w/2,y+43,title,21,C.ink,700);line(x+16,y+58,x+w-16,y+58);}
function product(){
 begin('产品全景视图');rect(1655,40,48,48,C.core);text(1679,72,'ON',20,'#15331f',700);text(1716,67,'ONpoint',33,C.ink,700,'start');text(1716,94,'OTA 售后履约 AGENT · 每一步，有依据',15,C.muted,400,'start');
 const headers=['多轮理解与事实归集','诉求分流与风险识别','方案生成与授权执行','回执留痕与经验沉淀'];
 headers.forEach((s,i)=>{rect(39+i*498,118,479,58,'url(#block)');rect(59+i*498,132,29,29,'#8ed3a433','none',5);text(74+i*498,154,i+1,18);text(100+i*498,154,s,20,C.ink,700,'start');});
 panel(39,197,825,592,'ONpoint · 售后履约智能体方案');
 const agents=[['分诊判断 Agent',['意图强度','风险与危机','槽位补齐','路由决策','情绪分级','旧事识别']],['方案生成 Agent',['规则核验','证据查询','方案卡生成','下一步建议','授权校验','替代方案']],['人工接管 Agent',['接管摘要','同案续办','越权拦截','结果回收','改判校验','回执追踪']],['自进化 Agent',['轨迹归档','候选起草','隔离回放','人工批准','发布激活','版本回滚']]];
 agents.forEach(([title,labels],i)=>{const x=55+(i%2)*403,y=270+Math.floor(i/2)*260;rect(x,y,389,245,'#1a3323');text(x+19,y+34,title,20,C.ink,700,'start');labels.forEach((s,j)=>cell(x+14+(j%2)*185,y+51+Math.floor(j/2)*63,175,53,s,false,17));});
 panel(883,197,305,592,'智能体配置');['业务规则集','判定点与阈值','题面与提示词','运行与观测'].forEach((s,i)=>cell(900,281+i*127,272,114,s,false,19));
 panel(1207,197,407,592,'ONpoint · 智能履约体');cell(1224,282,373,133,'接入客户诉求');text(1410,443,'▼',20,C.muted);cell(1224,458,373,141,'判定 + 规则 + 证据',true,21);text(1410,628,'▼',20,C.muted);cell(1224,641,373,133,'输出方案卡 / 下一步');
 panel(1633,197,376,592,'应用场景');['退款进度追踪','退改签办理','发票与资质','政策条款咨询','投诉与转人工','异常与工具故障'].forEach((s,i)=>{cell(1649,270+i*86,344,75,s,false,18);});
 rect(39,817,142,59,'#799461');text(110,855,'模型能力',20,C.ink,700);[['JEV 判定模型','只出概率，不出文字'],['抽取模型','口语 → 结构化字段'],['表达模型','只改措辞，可关闭'],['视觉核验模型','只观察，不下结论']].forEach(([a,b],i)=>{rect(198+i*457,808,441,78,C.model,'#d6dfc5');text(419+i*457,842,a,18,'#183622',700);text(419+i*457,868,b,14,'#48614d');});
 rect(39,905,142,58,'#4f8062');text(110,942,'数据来源',20,C.ink,700);['订单中心','支付与退款流水','政策条款库','历史处理案例','流程资产库'].forEach((s,i)=>{rect(198+i*366,905,350,58,C.data,'#c8ecd3');text(373+i*366,942,s,18,'#183622',700);});
 parts.push(`<rect x="39" y="983" width="1970" height="84" rx="12" fill="none" stroke="${C.line}" stroke-width="1.5" stroke-dasharray="5 4"/>`);rect(56,970,132,28,'#607d52','none',4);text(122,990,'优先 MVP 闭环',15,C.ink,700);
 ['用户提问','JEV 分诊','规则核验','方案卡 / 下一步','授权执行 或 转人工','回执与评测','流程优化'].forEach((s,i)=>{cell(61+i*280,1002,247,46,s,i===1,16);if(i<6)text(325+i*280,1032,'→',22,C.muted);});
 line(39,1086,2009,1086);text(39,1118,'MVP 版本：先聚焦「分诊判断 → 规则裁决 → 方案卡 → 人工接管 → 评测闭环」，再逐步扩展。',15,C.muted,400,'start');text(1040,1118,'核心亮点：模型负责理解，规则负责裁决，回执负责证明；反思驱动流程改进。',15,C.muted,400,'start');return end();
}
function system(){
 begin('系统架构');rect(34,98,1980,72,'url(#block)');text(1024,145,'ONpoint · OTA 售后履约 Agent 架构',29,C.ink,700);
 cell(34,187,147,55,'履约服务',true,21);['退款进度追踪','方案推演','授权执行','人工接管','流程沉淀'].forEach((s,i)=>cell(197+i*353,187,339,55,s,i===4,19));
 rect(34,257,147,390,'url(#block)');text(107,451,'应用',25,C.ink,700);text(107,478,'业务与运营维护',13,C.muted);
 parts.push(`<rect x="197" y="257" width="1748" height="390" rx="14" fill="none" stroke="${C.line}" stroke-width="1.5" stroke-dasharray="5 4"/>`);rect(215,244,315,28,C.panel);text(228,264,'业务配置（谁维护：运营 / 业务专家）',17,C.muted,400,'start');
 const cols=[['基础配置',['渠道适配','数据源绑定','工具白名单']],['内容配置',['方案卡模板','来源标注规则','证据展示口径']],['提示词配置',['判定题面','抽取指令','表达约束']],['高级配置',['阈值与置信','分流策略','回放基线']]];
 cols.forEach(([title,items],i)=>{const x=214+i*432;parts.push(`<rect x="${x}" y="277" width="415" height="355" rx="10" fill="#102117" stroke="${C.line}" stroke-dasharray="5 4"/>`);rect(x+18,269,90,23,C.panel,'none',0);text(x+24,285,title,15,C.muted,400,'start');items.forEach((s,j)=>cell(x+12,296+j*112,391,100,s,false,18));});
 rect(34,662,147,249,'url(#block)');text(107,782,'AI 服务',24,C.ink,700);text(107,809,'工程实现',14,C.muted);
 parts.push(`<rect x="197" y="662" width="1748" height="249" rx="14" fill="none" stroke="${C.line}" stroke-width="1.5" stroke-dasharray="5 4"/>`);rect(215,649,255,29,C.panel);text(228,670,'Agent 框架（谁维护：工程）',17,C.muted,400,'start');
 [['Agents','分诊/方案/接管/进化'],['Tools','只读查询/沙箱写'],['Prompts','题面与版本'],['Memory','案件/偏好/旧事'],['JEV 判定层','8 点 · 只加闸'],['规则引擎','规则表 → 可行集'],['状态机','八态流转'],['SQLite','事务与快照'],['幂等回执','TR- · 单一任务']].forEach(([title,sub],i)=>{const x=214+i*192;cell(x,683,179,212,'',i===4||i===5);text(x+89,777,title,18,i===4||i===5?'#15331f':C.ink,700);const words=sub.split('/');text(x+89,803,words.length>1?words:[sub],13,i===4||i===5?'#335b40':C.muted);});
 rect(1962,257,52,654,'#253b2d');['证据池','判定账本','TRACE 回放','授权与权限'].forEach((s,i)=>{if(i)line(1962,420+i*140,2014,420+i*140);text(1988,315+i*150,[...s.replace(' ','')],15,C.muted);});
 rect(34,926,147,78,'#799461');text(107,975,'模型',21,C.ink,700);[['JEV 判定模型','概率，不是结论'],['抽取模型','只输出识别到的字段'],['表达模型','过不了校验退回模板'],['视觉核验模型','只观察，不下结论'],['可替换','关闭不影响闭环']].forEach(([a,b],i)=>{rect(197+i*366,926,352,78,C.model,'#d6dfc5');text(373+i*366,962,a,18,'#183622',700);text(373+i*366,986,b,13,'#48614d');});
 rect(34,1019,147,78,'#4f8062');text(107,1068,'数据源',21,C.ink,700);[['订单中心','状态 · 渠道 · 时效'],['支付与退款流水','到账事实'],['政策条款库','每条处置挂条款'],['历史处理案例','来源与支持数'],['流程资产库','版本化 · 可回滚']].forEach(([a,b],i)=>{rect(197+i*366,1019,352,78,C.data,'#c8ecd3');text(373+i*366,1055,a,18,'#183622',700);text(373+i*366,1079,b,13,'#48614d');});
 const legends=[['#8ed3a4','自研核心'],['#30563c','可配置 / 可替换'],[C.model,'模型依赖'],[C.data,'业务数据']];legends.forEach(([fill,label],i)=>{rect(36+i*145,1113,15,15,fill,'none',3);text(58+i*145,1126,label,14,C.muted,400,'start');});text(665,1126,'中间两层为核心；最上层是可替换接入与底座，右栏贯穿所有层的留痕。',14,C.muted,400,'start');return end();
}
const diagrams={product:product(),system:system()};
for(const [name,svg]of Object.entries(diagrams))fs.writeFileSync(path.join(out,`architecture-${name}.svg`),svg,'utf8');
fs.writeFileSync(path.join(out,'diagrams.js'),'window.ONPOINT_DIAGRAMS = '+JSON.stringify(diagrams)+';\n');
console.log('Generated product and system architecture SVGs.');
