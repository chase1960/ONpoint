package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testApp(test *testing.T) *App {
	test.Helper()
	dataset, err := loadDataset("fixtures/ota.json")
	if err != nil {
		test.Fatal(err)
	}
	return &App{store: freshStore(dataset), dataset: dataset}
}
func mustApply(test *testing.T, app *App, input Request) {
	test.Helper()
	if err := app.apply(input); err != nil {
		test.Fatal(input.Action, err)
	}
}
func confirmPlan(test *testing.T, app *App, record *Case) Request {
	test.Helper()
	if len(record.Plans) != 1 {
		test.Fatal("missing plan", record.State)
	}
	plan := record.Plans[0]
	input := Request{Action: "confirm", CaseID: record.ID, PlanID: plan.ID, PlanToken: plan.Token, Confirmed: true}
	mustApply(test, app, input)
	return input
}
func completedHumanCase(test *testing.T, app *App) *Case {
	test.Helper()
	record := app.newCase("DEMO-RAIL-001")
	mustApply(test, app, Request{Action: "message", CaseID: record.ID, Text: "票退了，钱咋还没回来？帮我查查"})
	if record.State != "answered" || record.Route != "dynamic_consultation" {
		test.Fatal("query must finish without requesting a task", record.State)
	}
	mustApply(test, app, Request{Action: "request_tracking", CaseID: record.ID})
	if record.State != "handoff" {
		test.Fatal("v1 must handoff", record.State)
	}
	mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
	mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
	if len(app.store.Tasks) != 0 || record.State != "plan" {
		test.Fatal("human bypassed confirmation")
	}
	confirmPlan(test, app, record)
	mustApply(test, app, Request{Action: "resolved", CaseID: record.ID})
	return record
}
func candidate(test *testing.T, app *App) *Change {
	test.Helper()
	source := completedHumanCase(test, app)
	mustApply(test, app, Request{Action: "draft", CaseID: source.ID})
	return app.store.Changes[len(app.store.Changes)-1]
}
func publish(test *testing.T, app *App, change *Change) {
	test.Helper()
	for _, action := range []string{"change_replay", "change_approve", "change_publish"} {
		mustApply(test, app, Request{Action: action, ChangeID: change.ID})
	}
}

func TestPublishedProcedureIsConsumedAndRollbackPreservesArtifacts(test *testing.T) {
	app := testApp(test)
	change := candidate(test, app)
	old := app.newCase("DEMO-RAIL-002")
	if err := app.apply(Request{Action: "change_publish", ChangeID: change.ID}); err == nil {
		test.Fatal("unapproved publish")
	}
	publish(test, app, change)
	fresh := app.newCase("DEMO-RAIL-002")
	mustApply(test, app, Request{Action: "message", CaseID: fresh.ID, Text: "退票显示成功了，卡里还没有钱"})
	mustApply(test, app, Request{Action: "request_tracking", CaseID: fresh.ID})
	if fresh.State != "plan" || !strings.Contains(fresh.Facts["复用流程"], change.Procedure.ID) {
		test.Fatal("published asset not consumed")
	}
	input := confirmPlan(test, app, fresh)
	receipt := fresh.Receipt
	mustApply(test, app, input)
	if len(app.store.Tasks) != 2 || app.store.Tasks[receipt].ProposalID != fresh.ProposalID || app.store.Proposals[fresh.ProposalID].Status != "executed" {
		test.Fatal("lost artifact chain or idempotency")
	}
	mustApply(test, app, Request{Action: "message", CaseID: old.ID, Text: "帮我查一下退款进度"})
	mustApply(test, app, Request{Action: "request_tracking", CaseID: old.ID})
	if old.State != "handoff" || old.Snapshot != "v1" {
		test.Fatal("old case drifted")
	}
	mustApply(test, app, Request{Action: "rollback", ChangeID: change.ID})
	after := app.newCase("DEMO-RAIL-002")
	mustApply(test, app, Request{Action: "message", CaseID: after.ID, Text: "帮我查一下退款进度"})
	mustApply(test, app, Request{Action: "request_tracking", CaseID: after.ID})
	if after.State != "handoff" || app.store.Tasks[receipt] == nil || fresh.Snapshot != "v2" {
		test.Fatal("rollback changed business history")
	}
	if fresh.QueryOrder.Payment != "PROCESSING" || app.store.Tasks[receipt].State != "SUBMITTED" {
		test.Fatal("task was falsely treated as payment")
	}
	if change.SourcePlan == "" || change.SourceTask == "" {
		test.Fatal("source card/task not traceable")
	}
}

func TestEverySyntheticQuestionFollowsExpectedRoute(test *testing.T) {
	app := testApp(test)
	procedure := procedureTemplate("PROC-TEST", "SOURCE-TEST", app.dataset.Policy)
	for _, enabled := range []bool{false, true} {
		for _, sample := range app.dataset.Samples {
			test.Run(sample.ID+map[bool]string{false: "/base", true: "/candidate"}[enabled], func(test *testing.T) {
				isolated := testApp(test)
				if enabled {
					snapshot := isolated.store.Snapshots["v1"]
					snapshot.Procedure = &procedure
					isolated.store.Snapshots["v1"] = snapshot
				}
				record := isolated.newCase(sample.OrderID)
				isolated.assess(record, sample.Text)
				expected := sample.BaseExpected
				if enabled {
					expected = sample.CandidateExpected
				}
				if record.State != expected || record.Intent != sample.Intent {
					test.Fatalf("route %s/%s; expected %s/%s", record.State, record.Intent, expected, sample.Intent)
				}
				if len(isolated.store.Tasks) != 0 {
					test.Fatal("unconfirmed execution")
				}
			})
		}
	}
}

func TestUnknownOrUnrelatedInputDoesNotManufactureFacts(test *testing.T) {
	for _, fixture := range []struct{ order, text string }{{"", "票退了，退款没到"}, {"DEMO-RAIL-001", "明天杭州天气怎么样"}, {"DEMO-RAIL-999", "查退款进度"}} {
		app := testApp(test)
		record := app.newCase(fixture.order)
		app.assess(record, fixture.text)
		if record.QueryOrder != nil || record.Facts["支付退款"] != "" || len(record.Plans) != 0 {
			test.Fatal("invented successful order/payment")
		}
	}
}

func TestHumanOwnershipAndRiskAreSticky(test *testing.T) {
	app := testApp(test)
	record := app.newCase("DEMO-RAIL-001")
	app.assess(record, "查退款进度，申请追踪")
	app.assess(record, "再帮我看看退款")
	if record.State != "handoff" {
		test.Fatal("agent stole pending handoff")
	}
	mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
	mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
	plan := record.Plans[0]
	app.assess(record, "谢谢，继续帮我看看")
	if record.State != "plan" || record.Plans[0].Token != plan.Token {
		test.Fatal("agent stole human plan")
	}
	app.assess(record, "我要投诉并追究责任")
	if record.State != "human" || app.store.Proposals[record.ProposalID].Status != "cancelled" {
		test.Fatal("risk did not cancel plan")
	}
	if err := app.confirm(record, Request{PlanID: plan.ID, PlanToken: plan.Token, Confirmed: true}); err == nil {
		test.Fatal("old risky confirmation accepted")
	}
	mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
	if len(record.Plans) > 0 || len(app.store.Tasks) > 0 {
		test.Fatal("human bypassed risk")
	}
}

func TestManualRouteCannotBypassUnknownState(test *testing.T) {
	app := testApp(test)
	record := app.newCase("DEMO-RAIL-004")
	app.assess(record, "查退款进度，申请追踪")
	mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
	mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
	if len(record.Plans) > 0 || len(app.store.Tasks) > 0 {
		test.Fatal("unknown state bypassed by human")
	}
}

func TestConfirmationExpiryTamperAndToolFailures(test *testing.T) {
	for _, mode := range []string{"no_confirmation", "wrong_token", "expired", "query", "create"} {
		test.Run(mode, func(test *testing.T) {
			app := testApp(test)
			record := app.newCase("DEMO-RAIL-001")
			app.assess(record, "查退款进度，申请追踪")
			mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
			mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
			plan := record.Plans[0]
			input := Request{PlanID: plan.ID, PlanToken: plan.Token, Confirmed: true}
			switch mode {
			case "no_confirmation":
				input.Confirmed = false
			case "wrong_token":
				input.PlanToken = "old"
			case "expired":
				record.QueryAt = time.Now().Add(-6 * time.Minute).UTC().Format(time.RFC3339Nano)
			case "query", "create":
				record.Fault = mode
			}
			err := app.confirm(record, input)
			if mode == "query" || mode == "create" {
				if err != nil || record.State != "handoff" {
					test.Fatal("failure was not persisted as handoff", err)
				}
			} else if err == nil {
				test.Fatal("invalid consent accepted")
			}
			if len(app.store.Tasks) != 0 || record.Receipt != "" {
				test.Fatal("failure invented task receipt")
			}
		})
	}
}

func TestCandidateRequiresActualHumanSuccessEvidence(test *testing.T) {
	app := testApp(test)
	record := app.newCase("DEMO-RAIL-001")
	app.assess(record, "查退款进度，申请追踪")
	if app.draft(record) == nil {
		test.Fatal("handoff alone became learning evidence")
	}
	source := completedHumanCase(test, app)
	delete(app.store.Proposals, source.ProposalID)
	if app.draft(source) == nil {
		test.Fatal("source proposal was not verified")
	}
}

func TestEditingInvalidatesReplayAndApproval(test *testing.T) {
	app := testApp(test)
	change := candidate(test, app)
	mustApply(test, app, Request{Action: "change_replay", ChangeID: change.ID})
	mustApply(test, app, Request{Action: "change_approve", ChangeID: change.ID})
	mustApply(test, app, Request{Action: "change_edit", ChangeID: change.ID, Text: "人工补充审核说明"})
	if change.Status != "draft" || change.ReplayHash != "" || len(change.Replay) != 0 {
		test.Fatal("edit retained approval")
	}
	if app.change(Request{Action: "change_publish", ChangeID: change.ID}) == nil {
		test.Fatal("edited asset published")
	}
}

func TestReplayBindingsRejectStaleChanges(test *testing.T) {
	for _, mode := range []string{"baseline", "procedure", "policy", "fixtures", "report"} {
		test.Run(mode, func(test *testing.T) {
			app := testApp(test)
			change := candidate(test, app)
			mustApply(test, app, Request{Action: "change_replay", ChangeID: change.ID})
			switch mode {
			case "baseline":
				app.store.Active = "v99"
			case "procedure":
				change.Procedure.Title += " changed"
			case "policy":
				app.dataset.Policy.Version = "policy-v2"
			case "fixtures":
				app.dataset.Samples[0].Text += " changed"
			case "report":
				change.Replay[0].After = "tampered"
			}
			if app.change(Request{Action: "change_approve", ChangeID: change.ID}) == nil {
				test.Fatal("stale binding approved", mode)
			}
		})
	}
}

func TestReplayRunsEngineIsolatedAndNotJustRouteFlags(test *testing.T) {
	app := testApp(test)
	change := candidate(test, app)
	before := digest(app.store)
	rows := app.replay(change.Procedure, app.store.Snapshots[app.store.Active])
	if len(rows) != len(app.dataset.Samples)+4 {
		test.Fatal("unexpected replay count", len(rows))
	}
	for _, row := range rows {
		if !row.Passed {
			test.Fatal(row)
		}
	}
	if digest(app.store) != before || len(rows[0].AfterSteps) < len(workflowSteps) {
		test.Fatal("replay mutated live state or skipped execution")
	}
	publish(test, app, change)
	rows = app.replay(change.Procedure, app.store.Snapshots[app.store.Active])
	if rows[0].Before != "plan" {
		test.Fatal("replay ignored actual current baseline")
	}
}

func TestWorkflowCannotWidenPermissionsOrSkipConsent(test *testing.T) {
	app := testApp(test)
	procedure := procedureTemplate("PROC-TEST", "SOURCE", app.dataset.Policy)
	procedure.Steps = []string{"create_tracking"}
	if validateProcedure(procedure, app.dataset.Policy) == nil {
		test.Fatal("unsafe steps accepted")
	}
	procedure = procedureTemplate("PROC-TEST", "SOURCE", app.dataset.Policy)
	procedure.Business = "hotel"
	if validateProcedure(procedure, app.dataset.Policy) == nil {
		test.Fatal("cross business widening")
	}
}

func TestSQLiteRestoresCasesCardsTasksAndVersions(test *testing.T) {
	app := testApp(test)
	databasePath := filepath.Join(test.TempDir(), "ota.db")
	repository, err := openRepository(databasePath)
	if err != nil {
		test.Fatal(err)
	}
	change := candidate(test, app)
	publish(test, app, change)
	if err = repository.save(app.store); err != nil {
		test.Fatal(err)
	}
	repository.close()
	repository, err = openRepository(databasePath)
	if err != nil {
		test.Fatal(err)
	}
	restored, err := repository.load(app.dataset)
	if err != nil {
		test.Fatal(err)
	}
	if digest(restored) != digest(app.store) {
		test.Fatal("restart lost persisted artifacts")
	}
	if err = repository.save(restored); err != nil {
		test.Fatal("second transaction failed", err)
	}
	modified := app.dataset
	modified.Version = "changed"
	if _, err = repository.load(modified); err == nil {
		test.Fatal("dataset drift silently accepted")
	}
	repository.close()
}

func TestAPIErrorsRollbackAndOriginBoundary(test *testing.T) {
	app := testApp(test)
	body := []byte(`{"Action":"new","OrderID":"DEMO-RAIL-001"}`)
	request := httptest.NewRequest("POST", "http://127.0.0.1:8088/api/state", bytes.NewReader(body))
	request.Header.Set("Origin", "http://evil.test")
	response := httptest.NewRecorder()
	app.api(response, request)
	if response.Code != 403 || len(app.store.Cases) != 0 {
		test.Fatal("cross-origin write allowed")
	}
	repository, err := openRepository(":memory:")
	if err != nil {
		test.Fatal(err)
	}
	repository.close()
	app.db = repository
	request = httptest.NewRequest("POST", "http://127.0.0.1:8088/api/state", bytes.NewReader(body))
	response = httptest.NewRecorder()
	app.api(response, request)
	if response.Code != 500 || len(app.store.Cases) != 0 {
		test.Fatal("failed transaction claimed success")
	}
	var result map[string]string
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil || result["error"] == "" {
		test.Fatal("no readable failure")
	}
}

func TestJEVDoesNotRunWithoutConfiguration(test *testing.T) {
	app := testApp(test)
	record := app.newCase("DEMO-RAIL-001")
	app.observeJudge(record, app.dataset.Samples[0].Text)
	if len(record.Judgments) != 0 || app.compareJudge() == nil {
		test.Fatal("unconfigured JEV invented results")
	}
	if app.syntheticSample("客户真实自由输入") {
		test.Fatal("free input allowed to egress")
	}
}
