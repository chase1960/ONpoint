package main

import (
	"strings"
	"testing"
	"time"
)

// 承诺时钟的测试只回答三个问题：
// 1. 任务建起来时，有没有给出可核验的到点时间？
// 2. 到点了，系统是不是自己先开口？
// 3. 开口的内容，有没有越权宣布"已到账"？

func hasEventKind(record *Case, kind string) bool {
	for _, item := range record.Events {
		if item.Kind == kind {
			return true
		}
	}
	return false
}
func lastAssistantText(record *Case) string {
	for index := len(record.Messages) - 1; index >= 0; index-- {
		role := record.Messages[index].Role
		if role == "assistant" || role == "agent" || role == "human" {
			return record.Messages[index].Text
		}
	}
	return ""
}
func conversationText(record *Case) string {
	parts := make([]string, 0, len(record.Messages))
	for _, item := range record.Messages {
		parts = append(parts, item.Text)
	}
	return strings.Join(parts, "\n")
}

// slaApp 用真实规则文件跑，测完把全局规则恢复回去，避免污染其他用例。
func slaApp(test *testing.T) *App {
	test.Helper()
	dataset, err := loadDataset("fixtures/ota.json")
	if err != nil {
		test.Fatal(err)
	}
	rules, err := loadRules("fixtures/rules.json")
	if err != nil {
		test.Fatal(err)
	}
	previousRules, previousSteps, previousStops := activeRules, workflowSteps, stopConditions
	applyRules(rules)
	test.Cleanup(func() {
		activeRules, workflowSteps, stopConditions = previousRules, previousSteps, previousStops
	})
	if !rules.SLA.Enabled {
		test.Fatal("规则文件应启用承诺时钟")
	}
	return &App{store: freshStore(dataset), dataset: dataset}
}

// trackedWaitingCase 走到"任务已建、等待用户确认结果"这一步，不结案——
// 结案后承诺时钟不该再打扰，那是另一个分支。
func trackedWaitingCase(test *testing.T, app *App) *Case {
	test.Helper()
	record := app.newCase("DEMO-RAIL-001")
	mustApply(test, app, Request{Action: "message", CaseID: record.ID, Text: "票退了，钱咋还没回来？帮我查查"})
	mustApply(test, app, Request{Action: "request_tracking", CaseID: record.ID})
	if record.State == "handoff" {
		mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
		mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
	}
	confirmPlan(test, app, record)
	if record.Receipt == "" {
		test.Fatal("没有回执，承诺时钟无从启动", record.State)
	}
	return record
}

func TestPromiseClockStartsWhenTrackingTaskIsCreated(test *testing.T) {
	app := slaApp(test)
	record := trackedWaitingCase(test, app)

	task := app.store.Tasks[record.Receipt]
	if task == nil {
		test.Fatal("任务未落库")
	}
	if task.DueAt == "" || task.PromiseMinutes <= 0 {
		test.Fatal("承诺时钟未启动：", task.DueAt, task.PromiseMinutes)
	}
	if when, err := time.Parse(time.RFC3339Nano, task.DueAt); err != nil || !when.After(time.Now()) {
		test.Fatal("承诺时间必须是将来的可核验时间点：", task.DueAt, err)
	}
	if task.PromiseMinutes != activeRules.promiseMinutes() {
		test.Fatal("承诺时长应来自规则文件，不是代码常量")
	}
	if record.Facts["承诺到账时间"] == "" {
		test.Fatal("用户看不到承诺时间，等于没承诺")
	}
	if !hasEventKind(record, "promise_set") {
		test.Fatal("缺少 promise_set 事件，承诺无法回查")
	}
	// 承诺必须和"未到账"的现状一起说，不能让人误读成已经退了。
	text := conversationText(record)
	if !strings.Contains(text, "处理中") || !strings.Contains(text, "不等于") {
		test.Fatal("承诺文案必须同时说明当前未到账：", text)
	}
}

func TestPromiseDueNotifiesProactivelyWithoutClaimingArrival(test *testing.T) {
	app := slaApp(test)
	record := trackedWaitingCase(test, app)
	before := len(record.Messages)

	// 把时钟拨到已过期，然后只做一次无关交互：系统应当自己开口。
	app.store.Tasks[record.Receipt].DueAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	mustApply(test, app, Request{Action: "fault", CaseID: record.ID, Text: "none"})

	if !hasEventKind(record, "promise_due") {
		test.Fatal("到期没有主动触达，用户仍需自己来问")
	}
	if len(record.Messages) <= before {
		test.Fatal("到期触达没有产生可看见的消息")
	}
	text := lastAssistantText(record)
	if !strings.Contains(text, "未到账") && !strings.Contains(text, "仍为处理中") {
		test.Fatal("到期文案必须说明仍未到账：", text)
	}
	for _, banned := range []string{"已到账", "已退款", "钱已到"} {
		if strings.Contains(text, banned) {
			test.Fatal("到期触达不得宣布到账：", text)
		}
	}
}

func TestPromiseNotificationFiresOnlyOnce(test *testing.T) {
	app := slaApp(test)
	record := trackedWaitingCase(test, app)
	app.store.Tasks[record.Receipt].DueAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)

	mustApply(test, app, Request{Action: "fault", CaseID: record.ID, Text: "none"})
	mustApply(test, app, Request{Action: "fault", CaseID: record.ID, Text: "none"})

	count := 0
	for _, item := range record.Events {
		if item.Kind == "promise_due" {
			count++
		}
	}
	if count != 1 {
		test.Fatal("同一承诺只能主动触达一次，实际", count)
	}
}

func TestPromiseApproachingNotifiesBeforeDue(test *testing.T) {
	app := slaApp(test)
	record := trackedWaitingCase(test, app)

	// 落在"临近窗口"内：还没到期，但应该提前说一声。
	app.store.Tasks[record.Receipt].DueAt = time.Now().UTC().Add(time.Duration(activeRules.SLA.SoonHours) * time.Hour / 2).Format(time.RFC3339Nano)
	mustApply(test, app, Request{Action: "fault", CaseID: record.ID, Text: "none"})

	if !hasEventKind(record, "promise_soon") {
		test.Fatal("临近承诺时间未提前同步")
	}
	if hasEventKind(record, "promise_due") {
		test.Fatal("还没到期，不能当成到期处理")
	}
}

func TestResolvedCaseIsNotDisturbedByPromiseClock(test *testing.T) {
	app := slaApp(test)
	record := trackedWaitingCase(test, app)
	mustApply(test, app, Request{Action: "resolved", CaseID: record.ID})

	app.store.Tasks[record.Receipt].DueAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	mustApply(test, app, Request{Action: "fault", CaseID: record.ID, Text: "none"})

	if hasEventKind(record, "promise_due") {
		test.Fatal("已结案的案件不该再被承诺时钟打扰")
	}
}

func TestPromiseDurationComesFromRulesNotCode(test *testing.T) {
	rules, err := loadRules("fixtures/rules.json")
	if err != nil {
		test.Fatal(err)
	}
	if got := rules.promiseMinutes(); got != rules.SLA.PromiseHours*60 {
		test.Fatal("未开演示开关时应使用规则里的小时数，实际", got)
	}
	demo := *rules
	demo.SLA.DemoDueMinutes = 3
	if got := demo.promiseMinutes(); got != 3 {
		test.Fatal("演示开关应优先，实际", got)
	}
	noPlaceholder := SLARule{Enabled: true, PromiseHours: 72, SoonHours: 1, PromiseText: "预计很快到账", SoonText: "y", DueText: "z"}
	if err := noPlaceholder.validate(); err == nil {
		test.Fatal("缺少 {due} 占位符的承诺文案应被拒绝")
	}
	if err := (&SLARule{Enabled: true, PromiseHours: 0}).validate(); err == nil {
		test.Fatal("承诺时长为 0 应被拒绝")
	}
}
