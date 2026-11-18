package main

import (
	"slices"
	"testing"
)

// 澄清安全阀：连续多次仍无法取得可推进的信息（诉求不明或缺订单）后转人工，
// 而不是把客户一直留在重复澄清里。只加闸，不产生任何业务动作。
func TestClarifyEscalatesAfterRepeatedNonProgress(test *testing.T) {
	app := testApp(test)
	record := app.newCase("DEMO-RAIL-001")

	// 第一条无关消息：留在澄清态，计数 1，不升级。
	mustApply(test, app, Request{Action: "message", CaseID: record.ID, Text: "明天杭州天气怎么样"})
	if record.State != "clarify" || record.ClarifyCount != 1 {
		test.Fatal("first non-progress turn should stay clarify", record.State, record.ClarifyCount)
	}

	// 第二条仍无进展：达到规则上限，转人工交接。
	mustApply(test, app, Request{Action: "message", CaseID: record.ID, Text: "随便聊聊，没什么具体问题"})
	if record.State != "handoff" {
		test.Fatal("repeated non-progress should escalate to human handoff", record.State)
	}
}

// 一旦取得进展（识别到诉求并拿到可核验订单），澄清计数清零；
// 后续不会被此前的澄清轮次误升级。
func TestClarifyCountResetsOnceProgressMade(test *testing.T) {
	app := testApp(test)
	record := app.newCase("")

	// 第一条有诉求但没有订单：缺订单澄清，计数 1。
	mustApply(test, app, Request{Action: "message", CaseID: record.ID, Text: "票都退了，退款现在什么进度？"})
	if record.State != "clarify" || record.ClarifyCount != 1 {
		test.Fatal("missing order should stay clarify", record.State, record.ClarifyCount)
	}

	// 第二条带上合成订单：查询成功即进展，计数清零，走只读答复而不是被升级。
	mustApply(test, app, Request{Action: "message", CaseID: record.ID, Text: "DEMO-RAIL-001，钱咋还没回来，帮我查查"})
	if record.State != "answered" || record.ClarifyCount != 0 {
		test.Fatal("progress should reset clarify counter", record.State, record.ClarifyCount)
	}
}

// 判定账本：adopted 列出过线判定点，consumed 标记这条观测最终是否被业务裁决采用。
// 影子模式只观察不采用；辅助模式且模型结论进入分流时才记 consumed=true。
func TestJudgeLedgerMarksAdoptionAndConsumption(test *testing.T) {
	assist := testApp(test)
	assist.judge = newJEVClient("testkey", "http://127.0.0.1:9", "", "assist", "true")
	assistRecord := assist.newCase("DEMO-RAIL-001")
	assistObs := switchObservation("yes", "no")
	assist.useJudgment(assistRecord, "钱咋还没回来", &preparedJudge{client: assist.judge, mode: "assist", observation: &assistObs})

	if !assistObs.Consumed {
		test.Fatal("assist adopted conclusion should be marked consumed")
	}
	if !slices.Contains(assistObs.Adopted, "refund_progress") {
		test.Fatal("adopted list should include the over-threshold point", assistObs.Adopted)
	}

	shadow := testApp(test)
	shadow.judge = newJEVClient("testkey", "http://127.0.0.1:9", "", "shadow", "true")
	shadowRecord := shadow.newCase("DEMO-RAIL-001")
	shadowObs := switchObservation("yes", "no")
	shadow.useJudgment(shadowRecord, "钱咋还没回来", &preparedJudge{client: shadow.judge, mode: "shadow", observation: &shadowObs})

	if shadowObs.Consumed {
		test.Fatal("shadow observation must not be marked consumed")
	}
}
