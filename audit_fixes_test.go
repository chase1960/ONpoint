package main

import (
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
