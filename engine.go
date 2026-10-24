package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

var orderPattern = regexp.MustCompile(`(?i)DEMO-(?:RAIL|HOTEL)-[A-Z0-9]+`)

// workflowSteps / stopConditions 由规则文件填充（applyRules）。这里的值是启动前的占位，
// 只为让测试在没加载规则时不至于比对空切片；启动路径一定会被 fixtures/rules.json 覆盖。
var workflowSteps = []string{"query_order", "query_refund_trace", "explain_status", "request_confirmation", "create_tracking", "show_receipt", "ask_result"}
var stopConditions = []string{"订单或退款状态未知", "非铁路或非沙箱渠道", "投诉争议或资金权益诉求", "用户要求人工", "工具失败", "未明确确认或证据已过期", "退票未成功或支付不在处理中"}

func (app *App) newCase(orderID string) *Case {
	record := &Case{ID: uniqueID("CASE-"), State: "intake", Snapshot: app.store.Active, OrderID: orderID, Updated: stamp(), Fault: "none", Facts: map[string]string{}, Messages: []Message{}, Events: []Event{}, Evidence: []Evidence{}, Plans: []Plan{}, ActualSteps: []string{}}
	record.event("case_opened", "固定快照 "+record.Snapshot+"；当前仅操作 DEMO-USER 合成订单")
	record.Owner = "agent"
	app.store.Cases[record.ID] = record
	return record
}

// 以下五个函数只是 activeRules 的转发层。判定依据全部来自规则文件（fixtures/rules.json），
// 这里不保留任何业务词表或优先级顺序——改规则改文件，不改代码。
func classify(text string) string { return activeRules.classify(text) }

func wantsTracking(text string) bool { return activeRules.wantsTracking(text) }

func explicitHumanRequest(text string) bool { return activeRules.humanRequest(text) }

func readOnlyRequest(text string) bool { return activeRules.readOnlyRequest(text) }

func withoutHumanRequest(text string) string { return activeRules.withoutHumanRequest(text) }

func (record *Case) routeTo(route, reason string) {
	record.Route = route
	record.Facts["服务分流"] = map[string]string{"static_consultation": "静态咨询", "dynamic_consultation": "动态咨询", "task_request": "办理请求", "clarify_or_human": "澄清 / 高风险 / 人工"}[route]
	record.event("route_selected", record.Facts["服务分流"]+"："+reason)
}

func (app *App) handoff(record *Case, reason string) {
	record.routeTo("clarify_or_human", reason)
	app.invalidateProposal(record, "cancelled")
	record.State = "handoff"
	record.Plans = []Plan{}
	record.Consent = ""
	record.Decision = reason
	record.Facts["交接原因"] = reason
	record.event("handoff_requested", reason+"；案件、证据、已尝试步骤随案保留")
	if record.Receipt != "" {
		record.say("assistant", "已移交人工待接手，保留已有追踪任务 "+record.Receipt+"，不会重复提交。人工将接着这份案件处理。")
	} else {
		record.say("assistant", "已移交人工待接手，尚未办理退款或创建新任务。人工将接着这份案件处理，不需要你重复说明。")
	}
}

func (app *App) tool(record *Case, name, input string, output any, success bool) {
	content, _ := json.Marshal(output)
	record.Evidence = append(record.Evidence, Evidence{uniqueID("EV-"), name, input, string(content), stamp(), success})
	record.event("tool_result", name+"："+string(content))
}

func (app *App) query(record *Case) bool {
	record.QueryOrder = nil
	record.QueryAt = ""
	if record.OrderID == "" {
		return false
	}
	if record.Fault == "query" {
		app.tool(record, "query_order", record.OrderID, map[string]string{"error": "沙箱查询故障，无有效结果"}, false)
		return false
	}
	order, known := app.dataset.order(record.OrderID)
	if !known || order.Customer != "DEMO-USER" {
		app.tool(record, "query_order", record.OrderID, map[string]string{"error": "未找到可访问的合成订单"}, false)
		return false
	}
	app.tool(record, "query_order", order.ID, map[string]string{"order": order.ID, "business": order.Business, "cancellation": order.Ticket, "source": "synthetic_order_adapter"}, true)
	record.step("query_order")
	app.tool(record, "query_refund_trace", order.ID, map[string]string{"payment": order.Payment, "channel": order.Channel, "amount": order.Amount, "source": "synthetic_payment_adapter"}, true)
	record.step("query_refund_trace")
	record.QueryOrder = &order
	record.QueryAt = stamp()
	record.Facts["订单"] = order.ID
	record.Facts["行程"] = order.Route
	record.Facts["退票状态"] = map[string]string{"SUCCESS": "退票成功", "PENDING": "退票处理中", "UNKNOWN": "未知"}[order.Ticket]
	record.Facts["支付退款"] = map[string]string{"PROCESSING": "支付处理中，暂无到账回执", "PAID": "演示支付回执：已到账", "UNKNOWN": "未知", "NOT_STARTED": "退款未开始"}[order.Payment]
	record.Facts["金额"] = order.Amount + " 元（合成）"
	record.Facts["证据来源"] = "本地合成订单/支付适配器"
	record.Facts["查询时间"] = record.QueryAt
	return true
}

// setSafetyHold 落下持续阻断。写上去之后，只有 releaseSafetyHold 能清掉：
// 人工接手不清、用户改口不清、换一条普通表达也不清。
func (app *App) setSafetyHold(record *Case, reason, source string) {
	if !record.SafetyHold.released() {
		return
	}
	record.SafetyHold = &SafetyHold{Reason: reason, Source: source, At: stamp()}
	record.Risk = reason
	record.event("safety_hold_set", "持续安全阻断生效（来源 "+source+"）："+reason+"；人工接手不解除，只能显式解除并留痕")
}

func (app *App) eligible(record *Case) error {
	if !record.SafetyHold.released() {
		return errors.New("存在未解除的安全阻断：" + record.SafetyHold.Reason + "；需先由坐席显式解除并写明依据")
	}
	if record.Risk != "" {
		return errors.New("风险未解除，不能套用普通流程：" + record.Risk)
	}
	if record.Intent != "refund_progress" {
		return errors.New("不是退款进度请求")
	}
	if record.QueryOrder == nil {
		return errors.New("没有可核验的订单和退款证据")
	}
	order := record.QueryOrder
	rule := activeRules.Eligibility
	if order.Business != rule.Business || order.Channel != rule.Channel || order.Ticket != rule.Ticket || order.Payment != rule.Payment {
		return errors.New(rule.Reason)
	}
	snapshot, exists := app.store.Snapshots[record.Snapshot]
	if !exists || snapshot.PolicyHash != digest(app.dataset.Policy) || snapshot.DatasetHash != digest(app.dataset) {
		return errors.New("政策或演示数据版本变化，需要重新建案核验")
	}
	return nil
}

func (app *App) assess(record *Case, text string) {
	// 安全闸（P1-4）：最先判断，只加闸。命中就转人工，永不放行，也永不由模型或流程学习改写。
	if hit, reason := activeRules.safetyCheck(text); hit {
		record.Facts["安全闸"] = reason
		record.event("safety_gate", reason+"；安全判断只加闸，不放行")
		app.setSafetyHold(record, reason, "rule_safety_gate")
		app.handoff(record, reason)
		return
	}
	if record.State == "resolved" {
		record.say("assistant", "本次支持请求已结案，退款状态没有被改写。新的请求请新建案件。")
		return
	}
	if record.State == "human" || record.State == "handoff" || record.Owner == "human" {
		record.event("context_added", "补充内容保留给人工；不会自动抢回处理权")
		intent := classify(text)
		if intent == "human" {
			record.HumanRequested = true
			intent = classify(withoutHumanRequest(text))
		}
		if intent == "refund_progress" && record.Risk == "" {
			record.Intent = intent
			if readOnlyRequest(text) {
				app.invalidateProposal(record, "cancelled")
				record.Plans = []Plan{}
				record.Consent = ""
				record.TrackingRequested = false
				if record.State == "plan" {
					record.State = "human"
				}
				record.routeTo("clarify_or_human", "客户取消办理，保留人工处理权；旧方案失效")
			}
			if mentioned := orderPattern.FindString(text); mentioned != "" {
				if record.OrderID != strings.ToUpper(mentioned) {
					app.invalidateProposal(record, "cancelled")
					record.Plans = []Plan{}
					record.Consent = ""
					if record.Owner == "human" && record.State == "plan" {
						record.State = "human"
					}
				}
				record.OrderID = strings.ToUpper(mentioned)
			}
		}
		if slices.Contains([]string{"ticket_change", "invoice", "qualification", "policy_query"}, intent) {
			app.invalidateProposal(record, "cancelled")
			record.Plans = []Plan{}
			record.Consent = ""
			record.TrackingRequested = false
			record.Intent = intent
			if record.State == "plan" {
				record.State = "human"
			}
		}
		if slices.Contains([]string{"dispute", "compensation", "unauthorized"}, intent) {
			app.invalidateProposal(record, "cancelled")
			record.Risk = intent
			record.Plans = []Plan{}
			if record.Owner == "human" && record.State == "plan" {
				record.State = "human"
			}
		}
		return
	}
	intent := app.intakeIntent(record, text)
	if intent == "unknown" && record.Intent == "refund_progress" && (orderPattern.MatchString(text) || containsAny(text, "查一下", "进度")) {
		intent = record.Intent
	}
	if intent == "human" || intent == "dispute" || intent == "compensation" || intent == "unauthorized" {
		record.Intent = intent
		if intent == "human" {
			record.HumanRequested = true
			if businessIntent := classify(withoutHumanRequest(text)); businessIntent != "unknown" {
				record.Intent = businessIntent
			}
			if mentioned := orderPattern.FindString(text); mentioned != "" {
				record.OrderID = strings.ToUpper(mentioned)
			}
			// JEV 判出的安全信号要变成持续阻断，否则转人工之后照样能被 accept → confirm 绕过。
			if record.Intake != nil && record.Intake.Reason == "model_safety_signal" {
				app.setSafetyHold(record, "JEV 安全信号判定为风险表达（来源 jev_safety_signal）", "jev_safety_signal")
			}
		} else {
			record.Risk = intent
		}
		record.event("intent_judged", "输入分诊："+intent+"；风险与人工交接由代码控制")
		app.handoff(record, map[string]string{"human": "用户明确要求人工", "dispute": "L3 投诉争议，停止自动处理", "compensation": "L2 资金或权益诉求，需要人工复核", "unauthorized": "拒绝越权资金动作与无依据到账承诺"}[intent])
		return
	}
	if record.Risk != "" {
		app.handoff(record, "已有风险标记，后续普通表达不能解除")
		return
	}
	if record.Receipt != "" {
		record.say("assistant", "已创建追踪任务 "+record.Receipt+"，不会重复提交。请明确确认本次进度咨询与追踪请求是否处理完成；这不代表退款到账。")
		return
	}
	app.invalidateProposal(record, "superseded")
	record.Plans = []Plan{}
	record.Consent = ""
	record.Intent = intent
	record.event("intent_judged", "输入分诊："+intent+"；判断来源见 intake_decided，业务权限仍由代码控制")
	if slices.Contains([]string{"ticket_change", "invoice", "qualification"}, intent) {
		app.handoff(record, "识别到 "+intent+"，本期未开放该业务执行接口；不能以流程学习绕过")
		return
	}
	if intent == "policy_query" {
		record.TrackingRequested = false
		record.routeTo("static_consultation", "读取版本化演示政策，不执行任务")
		app.tool(record, "read_policy", app.dataset.Policy.ID, app.dataset.Policy, true)
		record.State = "answered"
		record.Decision = "L0 只读政策，不产生业务动作"
		record.say("assistant", "本演示政策：可以查询合成订单和退款轨迹；铁路退票成功、支付处理中时，可在明确确认后创建沙箱追踪任务。不能办理真实退票、修改支付状态或保证到账时间。正式业务应以平台当期政策为准。")
		return
	}
	if intent != "refund_progress" {
		record.routeTo("clarify_or_human", "诉求不明确，先澄清")
		record.State = "clarify"
		record.Decision = "未识别到本期支持的售后请求"
		record.say("assistant", "这次体验只处理铁路退款进度和追踪请求。你是想查退款进度、了解演示政策，还是请人工接手？我不会把无关提问当成订单查询。")
		return
	}
	if readOnlyRequest(text) {
		record.TrackingRequested = false
	} else if wantsTracking(text) {
		record.TrackingRequested = true
	}
	if record.TrackingRequested {
		record.routeTo("task_request", "用户明确申请追踪；仍须核验与方案确认")
	} else {
		record.routeTo("dynamic_consultation", "只读查询订单与支付轨迹，不自动提交追踪")
	}
	if mentioned := orderPattern.FindString(text); mentioned != "" {
		record.OrderID = strings.ToUpper(mentioned)
	}
	if record.OrderID == "" {
		record.routeTo("clarify_or_human", "缺少订单，先补充必要信息")
		record.State = "clarify"
		record.Decision = "缺少订单，未调用查询工具"
		record.say("assistant", "请提供合成订单号，例如 DEMO-RAIL-001，或新建案件时选择演示订单。没有订单，我不会推定已退票或退款状态。")
		return
	}
	if !app.query(record) {
		app.handoff(record, "订单查询无有效回执，需人工核验")
		return
	}
	if record.QueryOrder.Business != "rail" {
		app.handoff(record, "当前流程仅限铁路，不跨酒店等业务复用")
		return
	}
	if record.QueryOrder.Payment == "PAID" {
		record.State = "answered"
		record.Decision = "支付证据显示已到账，不创建处理中追踪任务"
		record.say("assistant", "合成支付回执显示退款已到账。这是只读查询结果，不适用“支付处理中”的追踪流程。如你与回执有争议，可转人工核验。")
		return
	}
	if containsAny(text, "已经到账", "已经收到退款") {
		record.Risk = "用户自述与支付处理中证据冲突"
		app.handoff(record, record.Risk)
		return
	}
	if !record.TrackingRequested {
		if record.QueryOrder.Ticket == "UNKNOWN" || record.QueryOrder.Payment == "UNKNOWN" {
			app.handoff(record, "查询状态未知，交人工核验；不编造进度")
			return
		}
		record.State = "answered"
		record.Decision = "动态咨询已完成，没有申请或创建追踪任务"
		record.say("assistant", "查询结果："+record.Facts["退票状态"]+"；"+record.Facts["支付退款"]+"。来源为本地合成订单与支付轨迹，查询时间 "+record.QueryAt+"。仅查询不会提交追踪；如需继续跟进，请明确点击“申请追踪”。")
		return
	}
	if err := app.eligible(record); err != nil {
		app.handoff(record, err.Error())
		return
	}
	snapshot := app.store.Snapshots[record.Snapshot]
	if snapshot.Procedure == nil {
		record.say("assistant", "查询证据：退票成功；退款仍在支付渠道处理中，尚无到账回执。初始版本缺少已审核的跟进流程，接下来由人工核验并提出追踪方案，不代表退款失败。")
		app.handoff(record, "流程缺口：尚无已审核的退款追踪流程")
		return
	}
	if err := validateProcedure(*snapshot.Procedure, app.dataset.Policy); err != nil {
		app.handoff(record, "发布流程校验失败："+err.Error())
		return
	}
	if !matches(*snapshot.Procedure, record) {
		app.handoff(record, "不满足已发布流程的全部前提")
		return
	}
	record.Facts["复用流程"] = snapshot.Procedure.ID + " / " + record.Snapshot
	record.event("procedure_matched", "实际读取发布资产 "+snapshot.Procedure.ID+"；来源 "+snapshot.Procedure.Source)
	app.offer(record, *snapshot.Procedure, "agent")
}

func (app *App) requestTracking(record *Case) error {
	if !record.SafetyHold.released() {
		return errors.New("存在未解除的安全阻断：" + record.SafetyHold.Reason + "；不能申请追踪")
	}
	if record.State != "answered" || record.Owner == "human" || record.Risk != "" || record.Receipt != "" || record.Intent != "refund_progress" {
		return errors.New("只有已完成的退款进度咨询可以申请追踪；人工案件请由坐席继续处理")
	}
	record.say("user", "申请追踪：请针对本案核验并形成方案，尚未授权执行")
	record.TrackingRequested = true
	app.assess(record, "申请追踪")
	return nil
}

func (app *App) refreshPlan(record *Case) error {
	if record.State != "plan" || len(record.Plans) != 1 || record.Receipt != "" {
		return errors.New("只有未执行的当前方案可以重新核验")
	}
	actor := record.PlanActor
	if actor != "human" && actor != "agent" {
		return errors.New("方案执行者无效")
	}
	app.invalidateProposal(record, "superseded")
	record.Plans = []Plan{}
	record.Consent = ""
	if !app.query(record) {
		app.handoff(record, "方案重新核验失败，旧方案已作废，没有创建任务")
		return nil
	}
	if err := app.eligible(record); err != nil {
		app.handoff(record, err.Error())
		return nil
	}
	procedure := procedureTemplate("HUMAN-MANUAL", record.ID, app.dataset.Policy)
	if actor == "agent" {
		snapshot := app.store.Snapshots[record.Snapshot]
		if snapshot.Procedure == nil || validateProcedure(*snapshot.Procedure, app.dataset.Policy) != nil || !matches(*snapshot.Procedure, record) {
			app.handoff(record, "固定版本的已发布流程不再适用，不能刷新方案")
			return nil
		}
		procedure = *snapshot.Procedure
	}
	record.event("plan_refreshed", "旧方案失效；已重新查询并生成需重新确认的新方案")
	app.offer(record, procedure, actor)
	return nil
}

func matches(procedure Procedure, record *Case) bool {
	order := record.QueryOrder
	return order != nil && procedure.Intent == record.Intent && procedure.Business == order.Business && procedure.Ticket == order.Ticket && procedure.Payment == order.Payment && procedure.Channel == order.Channel
}

func (app *App) offer(record *Case, procedure Procedure, actor string) {
	for _, step := range procedure.Steps {
		switch step {
		case "explain_status":
			record.step(step)
			record.say(actorRole(actor), "已核验：退票成功，支付退款处理中。能做的是创建一个关联本订单的沙箱追踪请求，不能保证到账时间或发起真实资金操作。")
		case "request_confirmation":
			record.step(step)
			record.PlanActor = actor
			record.State = "plan"
			record.Decision = "政策允许的低风险沙箱任务；等待用户确认"
			record.event("policy_checked", app.dataset.Policy.ID+"：允许 create_tracking；禁止资金动作；执行前将重新查询")
			app.invalidateProposal(record, "superseded")
			record.ProposalID = uniqueID("PLAN-")
			token := digest([]any{record.ID, record.ProposalID, record.OrderID, record.Snapshot, record.QueryOrder, record.QueryAt, procedure.ID, actor})
			record.Plans = []Plan{{ID: "followup", ArtifactID: record.ProposalID, Title: "查询说明已完成，申请追踪退款进度", Description: "关联当前合成订单，创建可查询的追踪任务；不发起退款，不承诺到账。", Clause: app.dataset.Policy.ID + " / " + app.dataset.Policy.Version, ProcedureID: procedure.ID, Token: token, Steps: slices.Clone(procedure.Steps), Benefits: []string{"不再重复说明，查询证据与任务始终关联同案", "可查看任务号、创建时间、执行者与流程版本"}, Notes: []string{"仅提交沙箱追踪请求，不能改变支付退款状态", "需要客户本人明确确认；查询超过5分钟需重新核验"}}}
			app.store.Proposals[record.ProposalID] = &Proposal{ID: record.ProposalID, CaseID: record.ID, OrderID: record.OrderID, PolicyVersion: app.dataset.Policy.Version, Snapshot: record.Snapshot, ProcedureID: procedure.ID, Status: "proposed", CreatedAt: stamp(), Plan: record.Plans[0]}
			record.event("plan_created", "方案卡 "+record.ProposalID+" 关联本案件、订单和流程；尚未执行")
			return
		}
	}
}

func (app *App) invalidateProposal(record *Case, status string) {
	if proposal := app.store.Proposals[record.ProposalID]; proposal != nil && proposal.Status == "proposed" {
		proposal.Status = status
	}
}

func actorRole(actor string) string {
	if actor == "human" {
		return "agent"
	}
	return "assistant"
}

func (app *App) confirm(record *Case, input Request) error {
	if !input.Confirmed {
		return errors.New("需要用户明确确认，聊天内容不能代替授权")
	}
	// 执行门与资格门共用同一道阻断：人工接手、套模板、换按钮都不能绕过安全闸。
	if !record.SafetyHold.released() {
		return errors.New("存在未解除的安全阻断：" + record.SafetyHold.Reason + "；确认不能越过安全阻断")
	}
	if record.Receipt != "" {
		if input.PlanID != record.Selected || input.PlanToken != record.Consent {
			return errors.New("不能将旧确认用于其他方案")
		}
		record.event("idempotent_return", "返回已有任务 "+record.Receipt+"，没有重复创建")
		return nil
	}
	if record.State != "plan" || len(record.Plans) != 1 {
		return errors.New("没有可执行的当前方案")
	}
	plan := record.Plans[0]
	proposal := app.store.Proposals[record.ProposalID]
	if proposal == nil || proposal.Status != "proposed" || proposal.Plan.Token != plan.Token {
		return errors.New("方案卡资产已失效")
	}
	if input.PlanID != plan.ID || input.PlanToken != plan.Token {
		return errors.New("方案确认已失效，请重新核对")
	}
	when, err := time.Parse(time.RFC3339Nano, record.QueryAt)
	if err != nil || time.Since(when) > activeRules.evidenceTTL() {
		return errors.New("证据超过5分钟，请重新查询并确认新方案")
	}
	if err = app.eligible(record); err != nil {
		return err
	}
	previous := digest(record.QueryOrder)
	if !app.query(record) {
		app.handoff(record, "执行前重新查询失败，未创建任务")
		return nil
	}
	if previous != digest(record.QueryOrder) {
		app.handoff(record, "执行前订单状态已变化，旧方案失效")
		return nil
	}
	if err = app.eligible(record); err != nil {
		app.handoff(record, err.Error())
		return nil
	}
	record.Consent = input.PlanToken
	proposal.Status = "confirmed"
	proposal.ConfirmedAt = stamp()
	record.say("user", "确认：针对 "+record.OrderID+" 创建沙箱追踪任务")
	record.event("confirmation_received", "用户按钮确认，绑定当前订单、方案、证据与版本")
	for _, step := range plan.Steps {
		switch step {
		case "create_tracking":
			if record.Fault == "create" {
				proposal.Status = "failed"
				app.tool(record, "create_tracking", record.OrderID, map[string]string{"error": "沙箱任务工具故障，未创建"}, false)
				app.handoff(record, "创建任务失败，没有成功回执；原支付状态不变")
				return nil
			}
			key := digest([]string{record.ID, record.OrderID, "refund_tracking"})
			var task *Task
			for _, existing := range app.store.Tasks {
				if existing.Key == key {
					task = existing
					break
				}
			}
			if task == nil {
				task = &Task{ID: uniqueID("TR-"), CaseID: record.ID, OrderID: record.OrderID, Key: key, State: "SUBMITTED", At: stamp(), Snapshot: record.Snapshot, ProcedureID: plan.ProcedureID, Consent: record.Consent, Actor: record.PlanActor, ProposalID: record.ProposalID}
				// 承诺时钟随任务一起落库：有了"什么时候到"，用户才不必反复来问。
				if activeRules.SLA.Enabled {
					task.PromiseMinutes = activeRules.promiseMinutes()
					task.DueAt = time.Now().UTC().Add(time.Duration(task.PromiseMinutes) * time.Minute).Format(time.RFC3339Nano)
				}
				app.store.Tasks[task.ID] = task
			}
			record.Receipt = task.ID
			proposal.Receipt = task.ID
			proposal.Status = "executed"
			record.Selected = plan.ID
			record.step(step)
			app.tool(record, "create_tracking", record.OrderID, task, true)
		case "show_receipt":
			if record.Receipt == "" {
				return errors.New("工具无回执，不能宣称成功")
			}
			record.step(step)
			record.Facts["追踪任务"] = record.Receipt
			message := "追踪请求已提交，沙箱任务号 " + record.Receipt + "。当前支付退款仍为处理中；任务提交不等于退款到账。"
			if task := app.store.Tasks[record.Receipt]; task != nil && task.DueAt != "" {
				when := humanDue(task.DueAt)
				record.Facts["下次反馈时间"] = when
				record.event("promise_set", "反馈时钟已启动："+when+" 前主动同步一次进度（"+promiseLabel(task.PromiseMinutes)+"）；这是服务跟进时限，不是渠道到账期限")
				message += " " + strings.ReplaceAll(activeRules.SLA.PromiseText, "{due}", when)
			}
			record.say(actorRole(record.PlanActor), message)
		case "ask_result":
			record.step(step)
			record.State = "waiting"
			record.say(actorRole(record.PlanActor), "请用按钮确认：本次进度已说明、追踪请求已提交；如仍有问题，可以让人工继续接着处理。")
		}
	}
	record.HumanHandled = record.PlanActor == "human"
	return nil
}

// checkPromise 是承诺时钟的检查点：临近提醒一次，到期主动触达一次。
// 「状态可见」的另一半不是把状态画出来，而是到点了系统先开口——
// 但开口只能说"仍为处理中、未到账"，绝不能代替支付状态宣布到账。
func (app *App) checkPromise(record *Case) {
	if !activeRules.SLA.Enabled || record.Receipt == "" || record.State == "resolved" {
		return
	}
	task := app.store.Tasks[record.Receipt]
	if task == nil || task.DueAt == "" {
		return
	}
	due, err := time.Parse(time.RFC3339Nano, task.DueAt)
	if err != nil {
		return
	}
	seen := func(kind string) bool {
		for _, item := range record.Events {
			if item.Kind == kind {
				return true
			}
		}
		return false
	}
	if remaining := time.Until(due); remaining <= 0 {
		if seen("promise_due") {
			return
		}
		record.event("promise_due", "承诺时间已到：支付退款仍为处理中，未到账；系统主动同步，不静默等待")
		record.say("assistant", activeRules.SLA.DueText)
		return
	} else if activeRules.SLA.SoonHours > 0 && remaining <= time.Duration(activeRules.SLA.SoonHours)*time.Hour && !seen("promise_soon") {
		record.event("promise_soon", "临近承诺时间：仍为处理中，系统提前主动同步")
		record.say("assistant", activeRules.SLA.SoonText)
	}
}

// humanDue 把到期时间渲染成人能读的样子；解析失败就原样返回，不猜。
func humanDue(value string) string {
	when, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return when.Local().Format("2006-01-02 15:04")
}

// promiseLabel 承诺时长的人读形式：按天/小时/分钟，不做四舍五入的"大约"。
func promiseLabel(minutes int) string {
	if minutes >= 1440 && minutes%1440 == 0 {
		return fmt.Sprintf("%d 天", minutes/1440)
	}
	if minutes >= 60 && minutes%60 == 0 {
		return fmt.Sprintf("%d 小时", minutes/60)
	}
	return fmt.Sprintf("%d 分钟", minutes)
}

func (app *App) apply(input Request) error {
	if input.Action == "judge_mode" {
		return app.switchJudge(input.Text)
	}
	if input.Action == "judge_compare" {
		if input.prepared != nil {
			return app.usePreparedReport(input.prepared)
		}
		return app.compareJudge()
	}
	if strings.HasPrefix(input.Action, "change_") || input.Action == "rollback" {
		return app.change(input)
	}
	if input.Action == "new" {
		if input.OrderID != "" {
			if _, known := app.dataset.order(input.OrderID); !known {
				return errors.New("请选择已列出的合成订单，不能输入真实订单")
			}
		}
		app.newCase(input.OrderID)
		return nil
	}
	record := app.store.Cases[input.CaseID]
	if record == nil {
		return errors.New("案件不存在，请新建案件")
	}
	// 每次交互都顺带过一遍承诺时钟：到点了系统先说，不等用户追问。
	app.checkPromise(record)
	switch input.Action {
	case "message":
		text := strings.TrimSpace(input.Text)
		if text == "" || len([]rune(text)) > 2000 {
			return errors.New("请输入1至2000字消息；勿输入真实个人信息")
		}
		record.say("user", text)
		if input.prepared != nil {
			app.useJudgment(record, text, input.prepared)
		} else {
			app.observeJudge(record, text)
		}
		app.assess(record, text)
	case "handoff", "unresolved":
		if record.State == "resolved" {
			return errors.New("已结案，新的请求请新建案件")
		}
		if input.Action == "unresolved" {
			record.Risk = "用户反馈仍未解决，不再复用同一流程"
		}
		app.handoff(record, "用户要求人工继续接手")
	case "release_hold":
		// 解除必须是一个独立动作：要人工身份、要写明依据、要留痕。
		// 不能让"接手案件"顺手把风险清掉，也不能让用户说一句"没事了"就解除。
		if record.Owner != "human" {
			return errors.New("安全阻断只能由已接手的坐席显式解除")
		}
		if record.SafetyHold.released() {
			return errors.New("当前案件没有生效中的安全阻断")
		}
		note := strings.TrimSpace(input.Text)
		if len([]rune(note)) < 6 {
			return errors.New("解除必须写明依据（不少于 6 字），留痕备查")
		}
		record.SafetyHold.ReleasedBy = "human"
		record.SafetyHold.ReleasedAt = stamp()
		record.SafetyHold.ReleaseNote = note
		if record.Risk == record.SafetyHold.Reason {
			record.Risk = ""
		}
		record.event("safety_hold_released", "人工显式解除阻断："+note+"；解除已留痕，不代表支付状态改变")
		record.say("agent", "已按你填写的依据解除本次安全阻断（"+note+"）。这是人工动作，已留痕；支付退款状态本身没有被改写，仍是处理中。")
	case "accept":
		if record.State != "handoff" {
			return errors.New("仅能接手待人工案件")
		}
		record.State = "human"
		record.Owner = "human"
		record.HumanRequested = true
		if record.Risk == "human" {
			record.Risk = ""
			for _, message := range record.Messages {
				if message.Role == "user" && classify(withoutHumanRequest(message.Text)) == "refund_progress" {
					record.Intent = "refund_progress"
				}
			}
			record.event("human_preference_migrated", "旧版人工偏好不再作为业务风险；未解除投诉、资金或其他风险")
		}
		record.event("human_accepted", "本地演示坐席接手同一案件")
		record.say("agent", "已看过你的问题和查询记录，我将接着本案处理，不需要重复描述。")
	case "human_reply":
		text := strings.TrimSpace(input.Text)
		if record.State != "human" || text == "" || len([]rune(text)) > 2000 {
			return errors.New("请先接手案件，回复需为1至2000字")
		}
		record.say("agent", text)
		record.event("human_replied", "人工回复入同案；回复文字不等于工具执行证据")
	case "human_prepare":
		if record.State != "human" || record.Receipt != "" {
			return errors.New("请先接手尚未创建任务的案件")
		}
		if !app.query(record) {
			record.event("human_blocked", "人工核验查询失败，不能生成方案")
			record.say("agent", "未拿到有效查询证据，暂不能生成追踪方案。")
			return nil
		}
		if err := app.eligible(record); err != nil {
			record.event("human_blocked", err.Error())
			record.say("agent", "追踪方案被政策阻止："+err.Error())
			return nil
		}
		record.HumanPrepared = true
		record.TrackingRequested = true
		record.routeTo("task_request", "人工核验并提出追踪方案，仍由客户明确确认")
		app.offer(record, procedureTemplate("HUMAN-MANUAL", record.ID, app.dataset.Policy), "human")
	case "request_tracking":
		return app.requestTracking(record)
	case "refresh_plan":
		return app.refreshPlan(record)
	case "consultation_done":
		if record.State != "answered" || record.Receipt != "" {
			return errors.New("只有未办理任务的已答复咨询可以确认完成")
		}
		record.State = "resolved"
		record.say("user", "查询说明已足够，本次咨询完成；没有申请追踪或改变资金状态")
		record.event("consultation_confirmed", "用户确认只读咨询完成，无任务回执，不作为流程学习来源")
	case "confirm":
		return app.confirm(record, input)
	case "resolved":
		if record.State != "waiting" || record.Receipt == "" {
			return errors.New("必须取得追踪回执后，由用户明确确认本次请求结果")
		}
		record.State = "resolved"
		record.say("user", "确认：进度已说明，追踪请求已提交")
		record.say("assistant", "已记录本次支持请求完成。支付退款仍按实际回执更新，没有被标成已到账。")
		record.event("user_confirmed", app.dataset.Policy.Success)
	case "draft":
		return app.draft(record)
	case "fault":
		if !slices.Contains([]string{"none", "query", "create"}, input.Text) {
			return errors.New("故障选项只支持 none/query/create")
		}
		record.Fault = input.Text
		record.event("fault_injected", "沙箱故障模式："+input.Text)
	default:
		return fmt.Errorf("不支持的动作 %q", input.Action)
	}
	return nil
}
