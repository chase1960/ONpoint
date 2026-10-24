package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 这一组用例对应代码审查里复现的缺口。它们按"期望的正确行为"断言，
// 改坏了就红，不是为了让覆盖率好看。

// newSafetyCase 走一条会命中安全闸的消息，返回案件。
func newSafetyCase(test *testing.T, app *App) *Case {
	test.Helper()
	record := app.newCase("DEMO-RAIL-001")
	mustApply(test, app, Request{Action: "message", CaseID: record.ID, Text: "票退了，钱咋还没回来？帮我查查"})
	mustApply(test, app, Request{Action: "message", CaseID: record.ID, Text: "这个充电宝冒烟了，你们要负责"})
	return record
}

// F1：安全闸命中必须留下持续阻断，人工接手不解除，执行门与资格门共用同一道阻断。
func TestSafetyGatePersistsAcrossHumanTakeover(test *testing.T) {
	app := testApp(test)
	record := newSafetyCase(test, app)
	if record.SafetyHold.released() {
		test.Fatal("安全闸命中后必须落下持续阻断")
	}
	mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
	if record.SafetyHold.released() {
		test.Fatal("人工接手不能解除安全阻断：接手是接手，放行是放行")
	}
	mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
	if record.HumanPrepared || record.Receipt != "" {
		test.Fatal("存在未解除阻断时，人工也不能生成方案或创建任务")
	}
	if !hasEventKind(record, "human_blocked") {
		test.Fatal("阻断必须留痕，不能静默拒绝")
	}
}

// F1：解除必须是独立动作——要人工身份、要写明依据、要留痕。
func TestSafetyHoldReleaseRequiresIdentityReasonAndTrace(test *testing.T) {
	app := testApp(test)
	record := newSafetyCase(test, app)
	if err := app.apply(Request{Action: "release_hold", CaseID: record.ID, Text: "已经确认没有风险了"}); err == nil {
		test.Fatal("未接手不能解除阻断")
	}
	mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
	if err := app.apply(Request{Action: "release_hold", CaseID: record.ID, Text: "好了"}); err == nil {
		test.Fatal("解除依据过短应被拒绝：解除必须写明理由")
	}
	mustApply(test, app, Request{Action: "release_hold", CaseID: record.ID, Text: "已电话核实，设备已回收，无安全风险"})
	if !record.SafetyHold.released() {
		test.Fatal("填写依据后应能解除")
	}
	if !hasEventKind(record, "safety_hold_released") {
		test.Fatal("解除必须留痕")
	}
	if record.Risk != "" {
		test.Fatal("解除后不应残留同一条风险标记")
	}
}

// F2：到期触达不能依赖用户发消息。后台扫描一次就应该开口。
func TestPromiseScanFiresWithoutAnyUserAction(test *testing.T) {
	app := slaApp(test)
	record := app.newCase("DEMO-RAIL-001")
	overdue := &Task{ID: "TR-OVERDUE", CaseID: record.ID, OrderID: record.OrderID, Key: "k-overdue",
		State: "SUBMITTED", At: stamp(), DueAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), PromiseMinutes: 1}
	app.store.Tasks[overdue.ID] = overdue
	record.Receipt = overdue.ID
	app.scanPromises()
	if !hasEventKind(record, "promise_due") {
		test.Fatal("后台扫描必须在没有任何用户操作时主动触达")
	}
	before := len(record.Events)
	app.scanPromises()
	if len(record.Events) != before {
		test.Fatal("重复扫描不能重复通知")
	}
	if text := lastAssistantText(record); strings.Contains(text, "已到账") {
		test.Fatal("到期消息不能宣布到账：", text)
	}
}

// F3：系统只能承诺自己的跟进时限，不能编造渠道到账期限。
func TestPromiseLabelsServiceFeedbackNotChannelArrival(test *testing.T) {
	rules, err := loadRules("fixtures/rules.json")
	if err != nil {
		test.Fatal(err)
	}
	if strings.Contains(rules.SLA.PromiseText, "前到账") {
		test.Fatal("承诺文案不能宣称渠道到账时间：", rules.SLA.PromiseText)
	}
	previousRules, previousSteps, previousStops := activeRules, workflowSteps, stopConditions
	applyRules(rules)
	defer func() { activeRules, workflowSteps, stopConditions = previousRules, previousSteps, previousStops }()
	// 故意写回一条编造到账期限的文案，规则校验必须拒绝启动。
	bad := rules.SLA
	bad.PromiseText = "按当前渠道承诺，这笔退款预计在 {due} 前到账。"
	if err := (&bad).validate(); err == nil {
		test.Fatal("编造渠道到账期限的文案必须被拒绝，不能静默回退")
	}
}

// F4：JEV 安全信号必须有消费者。判出 risk 就转人工，且要落成持续阻断。
func TestJEVSafetySignalIsConsumedByRouting(test *testing.T) {
	observation := JEVObservation{Status: "ok", Answers: fullAnswerSet(map[string]JEVAnswer{
		"safety_signal":    {Type: "choice", Choice: "risk", Confidence: 0.93, Probabilities: map[string]float64{"risk": 0.93, "normal": 0.07}},
		"human_requested":  {Type: "choice", Choice: "no", Confidence: 0.95, Probabilities: map[string]float64{"no": 0.95, "yes": 0.05}},
		"refund_progress":  {Type: "choice", Choice: "yes", Confidence: 0.95, Probabilities: map[string]float64{"yes": 0.95, "no": 0.05}},
	})}
	decision := decideIntake("票退了，钱咋还没回来？帮我查查", "assist", "", &observation)
	if decision.Source != "jev" || decision.Intent != "human" || decision.Reason != "model_safety_signal" {
		test.Fatalf("安全信号应优先分流到人工，实际得到 %+v", decision)
	}
}

// F4 的另一半：判不出来（未采用）不能把请求全推给人工，那不叫加闸，叫瘫痪。
func TestJEVSafetySignalAbsentDoesNotFloodHuman(test *testing.T) {
	answers := fullAnswerSet(nil)
	delete(answers, "safety_signal")
	observation := JEVObservation{Status: "ok", Answers: answers}
	decision := decideIntake("票退了，钱咋还没回来？帮我查查", "assist", "", &observation)
	if decision.Reason == "model_safety_signal" {
		test.Fatal("拿不准时不能借安全理由把请求推给人工")
	}
}

// cloneRules 深拷贝规则，用来模拟"改了一次配置"。
func cloneRules(test *testing.T, rules *RuleSet) *RuleSet {
	test.Helper()
	content, err := json.Marshal(rules)
	if err != nil {
		test.Fatal(err)
	}
	var result RuleSet
	if err := json.Unmarshal(content, &result); err != nil {
		test.Fatal(err)
	}
	return &result
}

// F6：规则版本必须冻结在案件上。改了规则，旧的核验结果就得作废——
// 否则"规则外置"会变成一条后门：改个配置文件就能让旧案件悄悄换一套判据。
func TestRulesDriftInvalidatesOldVerification(test *testing.T) {
	app := testApp(test)
	order, known := app.dataset.order("DEMO-RAIL-001")
	if !known {
		test.Fatal("缺少合成订单")
	}
	record := app.newCase("DEMO-RAIL-001")
	if record.RulesHash == "" {
		test.Fatal("建案必须记下当时的规则指纹")
	}
	record.Intent = "refund_progress"
	record.QueryOrder = &order
	if err := app.eligible(record); err != nil {
		test.Fatal("同版本规则下应可核验：", err)
	}
	rules, err := loadRules("fixtures/rules.json")
	if err != nil {
		test.Fatal(err)
	}
	modified := cloneRules(test, rules)
	modified.Version = rules.Version + "-changed"
	previousRules, previousSteps, previousStops := activeRules, workflowSteps, stopConditions
	applyRules(modified)
	defer func() { activeRules, workflowSteps, stopConditions = previousRules, previousSteps, previousStops }()
	if rulesHash() == record.RulesHash {
		test.Fatal("改规则后指纹必须变化")
	}
	if err := app.eligible(record); err == nil || !strings.Contains(err.Error(), "业务规则已变化") {
		test.Fatal("规则变化后旧核验结果必须失效，实际：", err)
	}
}

// F6 的另一半：批准绑定也要吃规则版本，改了规则旧批准不能继续生效。
func TestRulesDriftChangesApprovalBinding(test *testing.T) {
	app := testApp(test)
	change := &Change{ID: "CHG-BIND", Source: "test", Title: "候选流程", Base: app.store.Active}
	before := app.binding(change)
	rules, err := loadRules("fixtures/rules.json")
	if err != nil {
		test.Fatal(err)
	}
	modified := cloneRules(test, rules)
	modified.Version = rules.Version + "-changed"
	previousRules, previousSteps, previousStops := activeRules, workflowSteps, stopConditions
	applyRules(modified)
	defer func() { activeRules, workflowSteps, stopConditions = previousRules, previousSteps, previousStops }()
	if app.binding(change) == before {
		test.Fatal("规则变化后批准绑定必须变化，否则旧批准仍能生效")
	}
}

// F7：配置里声明的动作必须真的驱动分发，不能只是被加载着好看。
func TestConfigActionHandoffActuallyDispatches(test *testing.T) {
	rules, err := loadRules("fixtures/rules.json")
	if err != nil {
		test.Fatal(err)
	}
	modified := cloneRules(test, rules)
	for index := range modified.Intents {
		if modified.Intents[index].ID == "refund_progress" {
			modified.Intents[index].Action = "handoff"
			modified.Intents[index].Reason = "配置要求转人工"
		}
	}
	if err := modified.validate(); err != nil {
		test.Fatal(err)
	}
	previousRules, previousSteps, previousStops := activeRules, workflowSteps, stopConditions
	applyRules(modified)
	defer func() { activeRules, workflowSteps, stopConditions = previousRules, previousSteps, previousStops }()
	app := testApp(test)
	record := app.newCase("DEMO-RAIL-001")
	mustApply(test, app, Request{Action: "message", CaseID: record.ID, Text: "票退了，钱咋还没回来？帮我查查"})
	if record.State != "handoff" {
		test.Fatal("规则声明 handoff 就必须转人工，实际状态：", record.State)
	}
}

// F7：声明一个代码不认识的动作必须被拒绝，不能静默加载。
func TestUnknownConfigActionIsRejectedAtLoad(test *testing.T) {
	rules, err := loadRules("fixtures/rules.json")
	if err != nil {
		test.Fatal(err)
	}
	modified := cloneRules(test, rules)
	modified.Intents[0].Action = "auto_refund_everything"
	if err := modified.validate(); err == nil {
		test.Fatal("未知 action 必须被拒绝：配置不能看起来生效、实际没人读")
	}
}

// F5：refund_progress 只判业务归属，不再把"只查进度"判成 no——
// v1 的题面跟路由互相打架，正常口语的"钱到哪了"会被卡在 unknown。
func TestRefundProgressQuestionJudgesScopeNotIntentToAct(test *testing.T) {
	spec := jevQuestionSpecs["refund_progress"]
	if strings.Contains(spec.Instructions, "only to be told its status") {
		test.Fatal("题面仍在问办理意愿，会与路由打架：", spec.Instructions)
	}
	if !strings.Contains(spec.Criteria["yes"], "only want to be told") {
		test.Fatal("「只查询」必须算进退款进度业务，否则正常口语会卡在 unknown")
	}
	if spec.Version != "rp.v2" {
		test.Fatal("语义变更必须升版本号，回放基线才对得上：", spec.Version)
	}
}
