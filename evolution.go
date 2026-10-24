package main

import (
	"errors"
	"fmt"
	"slices"
)

func procedureTemplate(id, source string, policy Policy) Procedure {
	return Procedure{ID: id, Title: "铁路退款处理中：核验、说明与追踪", Source: source, Intent: "refund_progress", Business: "rail", Ticket: "SUCCESS", Payment: "PROCESSING", Channel: "sandbox", Steps: slices.Clone(workflowSteps), StopConditions: slices.Clone(stopConditions), Success: policy.Success, Support: 1}
}

func validateProcedure(procedure Procedure, policy Policy) error {
	if procedure.ID == "" || procedure.Source == "" || procedure.Support != 1 {
		return errors.New("需要真实来源；本期来源支持数只能为1")
	}
	if procedure.Intent != "refund_progress" || procedure.Business != "rail" || procedure.Ticket != "SUCCESS" || procedure.Payment != "PROCESSING" || procedure.Channel != "sandbox" {
		return errors.New("不能扩大本期适用范围或修改业务政策")
	}
	if !slices.Equal(procedure.Steps, workflowSteps) {
		return errors.New("步骤必须保留查询、说明、确认、追踪、回执与结果确认的安全顺序")
	}
	for _, step := range procedure.Steps {
		if !slices.Contains(policy.AllowedSteps, step) {
			return errors.New("动作不在工具白名单：" + step)
		}
	}
	if !slices.Equal(procedure.StopConditions, stopConditions) || procedure.Success != policy.Success {
		return errors.New("不能删掉禁用条件或将支持请求成功改成退款到账")
	}
	return nil
}

func (app *App) draft(record *Case) error {
	if record.State != "resolved" || !record.HumanHandled || !record.HumanPrepared || record.Receipt == "" {
		return errors.New("只能从人工实际处理、有工具回执并获用户确认的案件起草")
	}
	task := app.store.Tasks[record.Receipt]
	if task == nil || task.Actor != "human" || task.CaseID != record.ID || task.Consent == "" {
		return errors.New("缺少可验证的人工任务与授权证据")
	}
	proposal := app.store.Proposals[record.ProposalID]
	if proposal == nil || proposal.Status != "executed" || proposal.Receipt != task.ID || task.ProposalID != proposal.ID {
		return errors.New("缺少方案卡—授权—任务的完整来源链")
	}
	for _, change := range app.store.Changes {
		if change.Source == record.ID {
			return errors.New("本案已经生成候选，请查看流程改进")
		}
	}
	observed := []string{}
	for _, step := range record.ActualSteps {
		if !slices.Contains(observed, step) {
			observed = append(observed, step)
		}
	}
	if !slices.Equal(observed, workflowSteps) {
		return errors.New("处理轨迹不完整，不能把聊天回复当作可执行经验")
	}
	procedure := procedureTemplate(uniqueID("PROC-"), record.ID, app.dataset.Policy)
	procedure.Steps = observed
	change := &Change{ID: uniqueID("CH-"), Source: record.ID, Title: procedure.Title, Note: "来源为1个合成人工案例。仅复用核验、说明、确认后创建追踪任务的做法；不扩展资金权限。", Status: "draft", Base: app.store.Active, Procedure: procedure, Replay: []ReplayRow{}, Events: []Event{{"drafted", "从实际操作事件起草；来源任务 " + record.Receipt + "；支持数1，仅证明演示闭环", stamp()}}}
	change.SourcePlan = record.ProposalID
	change.SourceTask = record.Receipt
	change.Hash = hashChange(change)
	app.store.Changes = append(app.store.Changes, change)
	record.event("procedure_drafted", "候选 "+change.ID+" 尚未生效")
	return nil
}

func hashChange(change *Change) string {
	return digest([]any{change.ID, change.Source, change.SourcePlan, change.SourceTask, change.Title, change.Note, change.Procedure})
}
func (app *App) binding(change *Change) string {
	return digest([]any{hashChange(change), app.store.Active, app.store.Snapshots[app.store.Active], app.dataset, "engine-v4-jev-switch/tools-v1/replay-v3"})
}
func (app *App) validApproval(change *Change) bool {
	if change.ReplayHash != hashChange(change) || change.Base != app.store.Active || change.Binding != app.binding(change) || change.ReportHash != digest(change.Replay) || len(change.Replay) == 0 {
		return false
	}
	for _, row := range change.Replay {
		if !row.Passed {
			return false
		}
	}
	return true
}

func (app *App) change(input Request) error {
	var change *Change
	for _, candidate := range app.store.Changes {
		if candidate.ID == input.ChangeID {
			change = candidate
			break
		}
	}
	if change == nil {
		return errors.New("候选流程不存在")
	}
	if input.Action != "rollback" && (change.Status == "active" || change.Status == "rolled_back") {
		return errors.New("已发布的不可变流程不能直接编辑或再次发布")
	}
	switch input.Action {
	case "change_edit":
		if len([]rune(input.Text)) > 4000 {
			return errors.New("审核说明最多4000字")
		}
		if input.Procedure != nil {
			if input.Procedure.ID != change.Procedure.ID || input.Procedure.Source != change.Source {
				return errors.New("不能更换来源或流程标识")
			}
			if err := validateProcedure(*input.Procedure, app.dataset.Policy); err != nil {
				return err
			}
			change.Procedure = *input.Procedure
		}
		change.Note = input.Text
		change.Status = "draft"
		change.ReplayHash = ""
		change.Binding = ""
		change.ReportHash = ""
		change.Replay = []ReplayRow{}
		change.Hash = hashChange(change)
	case "change_replay":
		if err := validateProcedure(change.Procedure, app.dataset.Policy); err != nil {
			return err
		}
		change.Base = app.store.Active
		change.Hash = hashChange(change)
		change.Replay = app.replay(change.Procedure, app.store.Snapshots[app.store.Active])
		change.ReplayHash = change.Hash
		change.Binding = app.binding(change)
		change.ReportHash = digest(change.Replay)
		change.Status = "replayed"
		for _, row := range change.Replay {
			if !row.Passed {
				change.Status = "draft"
			}
		}
	case "change_approve":
		if change.Status != "replayed" || !app.validApproval(change) {
			return errors.New("批准失败：请重新运行绑定当前资产、基线、政策、数据和用例的回放")
		}
		change.Status = "approved"
	case "change_publish":
		if change.Status != "approved" || !app.validApproval(change) {
			return errors.New("发布失败：缺少有效回放或人工批准")
		}
		if err := validateProcedure(change.Procedure, app.dataset.Policy); err != nil {
			return err
		}
		procedure := change.Procedure
		procedure.Steps = slices.Clone(procedure.Steps)
		procedure.StopConditions = slices.Clone(procedure.StopConditions)
		change.Snapshot = fmt.Sprintf("v%d", len(app.store.Snapshots)+1)
		app.store.Snapshots[change.Snapshot] = Snapshot{ID: change.Snapshot, Parent: app.store.Active, Change: change.ID, PolicyVersion: app.dataset.Policy.Version, PolicyHash: digest(app.dataset.Policy), DatasetHash: digest(app.dataset), Procedure: &procedure}
		app.store.Active = change.Snapshot
		change.Status = "active"
	case "rollback":
		if change.Status != "active" || change.Snapshot != app.store.Active {
			return errors.New("只能回滚当前激活版本；不会撤销业务任务")
		}
		app.store.Active = app.store.Snapshots[change.Snapshot].Parent
		change.Status = "rolled_back"
	default:
		return errors.New("不支持的候选动作")
	}
	change.Events = append(change.Events, Event{input.Action, "本地演示审核员 · " + change.Status, stamp()})
	return nil
}

func (app *App) isolated(snapshot Snapshot) *App {
	state := freshStore(app.dataset)
	state.Active = snapshot.ID
	state.Snapshots = map[string]Snapshot{snapshot.ID: snapshot}
	return &App{store: state, dataset: app.dataset}
}

func (app *App) replay(procedure Procedure, baseline Snapshot) []ReplayRow {
	candidate := Snapshot{ID: "candidate", Parent: baseline.ID, PolicyVersion: app.dataset.Policy.Version, PolicyHash: digest(app.dataset.Policy), DatasetHash: digest(app.dataset), Procedure: &procedure}
	rows := []ReplayRow{}
	for _, sample := range app.dataset.Samples {
		outcomes := []string{}
		steps := [][]string{}
		passed := true
		for _, snapshot := range []Snapshot{baseline, candidate} {
			isolated := app.isolated(snapshot)
			record := isolated.newCase(sample.OrderID)
			isolated.assess(record, sample.Text)
			outcomes = append(outcomes, record.State)
			passed = passed && record.Intent == sample.Intent && len(isolated.store.Tasks) == 0
			if record.State == "plan" {
				plan := record.Plans[0]
				if err := isolated.confirm(record, Request{PlanID: plan.ID, PlanToken: plan.Token, Confirmed: true}); err != nil {
					passed = false
				}
				firstReceipt := record.Receipt
				if err := isolated.confirm(record, Request{PlanID: plan.ID, PlanToken: plan.Token, Confirmed: true}); err != nil {
					passed = false
				}
				passed = passed && record.State == "waiting" && firstReceipt != "" && record.Receipt == firstReceipt && len(isolated.store.Tasks) == 1 && record.QueryOrder.Payment == "PROCESSING"
			}
			steps = append(steps, slices.Clone(record.ActualSteps))
		}
		beforeExpected := sample.BaseExpected
		if baseline.Procedure != nil {
			beforeExpected = sample.CandidateExpected
		}
		passed = passed && outcomes[0] == beforeExpected && outcomes[1] == sample.CandidateExpected
		rows = append(rows, ReplayRow{Name: sample.Group + " / " + sample.ID, Before: outcomes[0], After: outcomes[1], ExpectedBefore: beforeExpected, ExpectedAfter: sample.CandidateExpected, Passed: passed, BeforeSteps: steps[0], AfterSteps: steps[1]})
	}
	for _, fault := range []string{"query", "create"} {
		outcomes := []string{}
		steps := [][]string{}
		passed := true
		for _, snapshot := range []Snapshot{baseline, candidate} {
			isolated := app.isolated(snapshot)
			record := isolated.newCase("DEMO-RAIL-001")
			record.Fault = fault
			isolated.assess(record, "退款进度，钱还没到账，申请追踪")
			if record.State == "plan" {
				plan := record.Plans[0]
				passed = passed && isolated.confirm(record, Request{PlanID: plan.ID, PlanToken: plan.Token, Confirmed: true}) == nil
			}
			outcomes = append(outcomes, record.State)
			steps = append(steps, record.ActualSteps)
			passed = passed && len(isolated.store.Tasks) == 0 && record.Receipt == "" && record.State == "handoff"
		}
		rows = append(rows, ReplayRow{Name: "工具故障 / " + fault, Before: outcomes[0], After: outcomes[1], ExpectedBefore: "handoff", ExpectedAfter: "handoff", Passed: passed, BeforeSteps: steps[0], AfterSteps: steps[1]})
	}
	isolated := app.isolated(candidate)
	record := isolated.newCase("DEMO-RAIL-001")
	isolated.assess(record, "查一下退款进度，申请追踪")
	plan := record.Plans[0]
	err := isolated.confirm(record, Request{PlanID: plan.ID, PlanToken: plan.Token})
	rows = append(rows, ReplayRow{Name: "无用户确认不能执行", Before: "未授权", After: record.State, ExpectedBefore: "未授权", ExpectedAfter: "plan", Passed: err != nil && len(isolated.store.Tasks) == 0})
	isolated.assess(record, "我要投诉退款并追究责任")
	err = isolated.confirm(record, Request{PlanID: plan.ID, PlanToken: plan.Token, Confirmed: true})
	rows = append(rows, ReplayRow{Name: "投诉后旧方案不得执行", Before: "plan", After: record.State, ExpectedBefore: "plan", ExpectedAfter: "handoff", Passed: err != nil && len(isolated.store.Tasks) == 0})
	return rows
}
