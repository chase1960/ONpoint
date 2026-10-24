package main

import (
	"strings"
	"testing"
	"time"
)

func TestFourRoutesAndReadOnlyQueriesNeverCreatePlans(test *testing.T) {
	for _, scenario := range []struct{ text, route, state string }{
		{"退票退款规则是什么", "static_consultation", "answered"},
		{"票退了，钱咋还没回来？帮我查查", "dynamic_consultation", "answered"},
		{"只查退款，不申请追踪", "dynamic_consultation", "answered"},
		{"请申请追踪退款进度", "clarify_or_human", "handoff"},
		{"我还没想好要问什么", "clarify_or_human", "clarify"},
		{"转人工，我要投诉这笔退款", "clarify_or_human", "handoff"},
	} {
		test.Run(scenario.text, func(test *testing.T) {
			app := testApp(test)
			record := app.newCase("DEMO-RAIL-001")
			app.assess(record, scenario.text)
			if record.Route != scenario.route || record.State != scenario.state || len(record.Plans) != 0 || len(app.store.Tasks) != 0 {
				test.Fatalf("unexpected route/state %s/%s", record.Route, record.State)
			}
		})
	}
}

func TestPublishedFlowStillRequiresSeparateTrackingRequest(test *testing.T) {
	app := testApp(test)
	change := candidate(test, app)
	publish(test, app, change)
	record := app.newCase("DEMO-RAIL-002")
	app.assess(record, "票退了，钱咋还没回来？帮我查查")
	if record.State != "answered" || record.Route != "dynamic_consultation" || len(record.Plans) != 0 {
		test.Fatal("publication turned consultation into an unsolicited task")
	}
	mustApply(test, app, Request{Action: "request_tracking", CaseID: record.ID})
	if record.Route != "task_request" || record.State != "plan" || len(app.store.Tasks) != 1 {
		test.Fatal("request did not form an unexecuted plan")
	}
	confirmPlan(test, app, record)
	if len(app.store.Tasks) != 2 {
		test.Fatal("explicit consent did not execute exactly one task")
	}
}

func TestHumanPreferenceDoesNotBlockManualSupportOrClearRealRisk(test *testing.T) {
	app := testApp(test)
	record := app.newCase("DEMO-RAIL-001")
	app.assess(record, "转人工客服帮我查退款进度")
	if record.Risk != "" || !record.HumanRequested || record.Intent != "refund_progress" {
		test.Fatal("human preference treated as business risk")
	}
	mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
	mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
	if record.State != "plan" || record.PlanActor != "human" {
		test.Fatal("manual plan blocked")
	}
	app.assess(record, "转人工，我要投诉退款并追究责任")
	mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
	if record.Risk != "dispute" || len(record.Plans) != 0 || len(app.store.Tasks) != 0 {
		test.Fatal("human preference bypassed complaint risk")
	}
}

func TestHumanCanReceiveMissingBusinessContextWithoutAgentTakeover(test *testing.T) {
	app := testApp(test)
	record := app.newCase("")
	app.assess(record, "转人工客服")
	mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
	app.assess(record, "我想查退款进度，订单 DEMO-RAIL-002")
	if record.Owner != "human" || record.State != "human" || record.Intent != "refund_progress" || record.OrderID != "DEMO-RAIL-002" {
		test.Fatal("business context was lost or agent stole case")
	}
	mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
	if record.State != "plan" {
		test.Fatal("manual continuation blocked")
	}
}

func TestExpiredManualAndPublishedPlansRefreshWithoutOldConsent(test *testing.T) {
	for _, actor := range []string{"human", "agent"} {
		test.Run(actor, func(test *testing.T) {
			app := testApp(test)
			if actor == "agent" {
				publish(test, app, candidate(test, app))
			}
			record := app.newCase("DEMO-RAIL-002")
			app.assess(record, "查退款进度，申请追踪")
			if actor == "human" {
				mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
				mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
			}
			oldPlan, oldProposal, taskCount := record.Plans[0], record.ProposalID, len(app.store.Tasks)
			record.QueryAt = time.Now().Add(-6 * time.Minute).UTC().Format(time.RFC3339Nano)
			if app.confirm(record, Request{PlanID: oldPlan.ID, PlanToken: oldPlan.Token, Confirmed: true}) == nil {
				test.Fatal("expired evidence accepted")
			}
			mustApply(test, app, Request{Action: "refresh_plan", CaseID: record.ID})
			if record.State != "plan" || record.PlanActor != actor || record.ProposalID == oldProposal || record.Plans[0].Token == oldPlan.Token || app.store.Proposals[oldProposal].Status != "superseded" || len(app.store.Tasks) != taskCount {
				test.Fatal("refresh reused consent or executed a task")
			}
			if app.confirm(record, Request{PlanID: oldPlan.ID, PlanToken: oldPlan.Token, Confirmed: true}) == nil {
				test.Fatal("old confirmation accepted after refresh")
			}
			confirmPlan(test, app, record)
			if len(app.store.Tasks) != taskCount+1 {
				test.Fatal("refreshed confirmation did not execute once")
			}
		})
	}
}

func TestRefreshFailureCancelsOldPlanAndPreservesRisk(test *testing.T) {
	for _, fault := range []string{"query", "risk"} {
		test.Run(fault, func(test *testing.T) {
			app := testApp(test)
			record := app.newCase("DEMO-RAIL-001")
			app.assess(record, "申请追踪退款")
			mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
			mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
			old := record.Plans[0]
			if fault == "query" {
				record.Fault = "query"
			} else {
				record.Risk = "compensation"
			}
			mustApply(test, app, Request{Action: "refresh_plan", CaseID: record.ID})
			if record.State != "handoff" || len(record.Plans) != 0 || len(app.store.Tasks) != 0 {
				test.Fatal("failed refresh left executable plan")
			}
			if app.confirm(record, Request{PlanID: old.ID, PlanToken: old.Token, Confirmed: true}) == nil {
				test.Fatal("failed refresh accepted old confirmation")
			}
		})
	}
}

func TestConsultationCanFinishWithoutBecomingTrainingEvidence(test *testing.T) {
	app := testApp(test)
	record := app.newCase("DEMO-RAIL-001")
	app.assess(record, "只查退款进度")
	mustApply(test, app, Request{Action: "consultation_done", CaseID: record.ID})
	if record.State != "resolved" || len(app.store.Tasks) != 0 || app.draft(record) == nil {
		test.Fatal("read-only consultation fabricated task or learning evidence")
	}
}

func TestExplicitReadOnlyRequestCancelsUnexecutedAgentPlan(test *testing.T) {
	app := testApp(test)
	publish(test, app, candidate(test, app))
	record := app.newCase("DEMO-RAIL-002")
	app.assess(record, "申请追踪退款")
	old, proposal := record.Plans[0], record.ProposalID
	app.assess(record, "只查退款进度，不申请追踪")
	if record.State != "answered" || record.TrackingRequested || len(record.Plans) != 0 || app.store.Proposals[proposal].Status != "superseded" {
		test.Fatal("read-only preference did not supersede task proposal")
	}
	if err := app.confirm(record, Request{PlanID: old.ID, PlanToken: old.Token, Confirmed: true}); err == nil || !strings.Contains(err.Error(), "方案") {
		test.Fatal("cancelled plan remained executable", err)
	}
}

func TestHumanPlanCannotSurviveCancellationOrBusinessSwitch(test *testing.T) {
	for _, text := range []string{"只查退款，不申请追踪", "我现在需要开发票"} {
		app := testApp(test)
		record := app.newCase("DEMO-RAIL-001")
		app.assess(record, "申请追踪退款")
		mustApply(test, app, Request{Action: "accept", CaseID: record.ID})
		mustApply(test, app, Request{Action: "human_prepare", CaseID: record.ID})
		old := record.Plans[0]
		app.assess(record, text)
		if record.Owner != "human" || record.State != "human" || len(record.Plans) != 0 || record.Consent != "" {
			test.Fatal("human plan survived a changed request")
		}
		if app.confirm(record, Request{PlanID: old.ID, PlanToken: old.Token, Confirmed: true}) == nil {
			test.Fatal("cancelled human plan executed")
		}
	}
}

func TestJudgeLabelsSeparateHumanPreferenceFromBusinessIntent(test *testing.T) {
	app := testApp(test)
	for _, sampleID := range []string{"human_progress", "mixed_risk"} {
		var observation JEVObservation
		for _, sample := range app.dataset.Samples {
			if sample.ID == sampleID {
				app.labelJudgment(&observation, sample.Text)
			}
		}
		if observation.LocalHumanRequested == nil || !*observation.LocalHumanRequested || observation.ExpectedHumanRequested == nil || !*observation.ExpectedHumanRequested {
			test.Fatal("human preference label lost", sampleID)
		}
	}
}
