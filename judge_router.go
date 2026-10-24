package main

import (
	"errors"
	"slices"
	"strings"
)

const (
	jevRefundProbability = 0.85
	jevHumanProbability  = 0.90
	jevProbabilityMargin = 0.20
)

type IntakeDecision struct {
	Mode, Source, Reason, RuleIntent, Intent, MessageHash, At string
	Fallback                                                  bool
}

type preparedJudge struct {
	client      *JEVClient
	mode        string
	caseHash    string
	text        string
	reason      string
	samples     []Sample
	observation *JEVObservation
	report      []JEVObservation
	current     func() bool
}

func rulePriority(intent string) bool {
	return intent != "unknown" && intent != "refund_progress"
}

func (app *App) prepareMessageJudge(record *Case, text string) *preparedJudge {
	status := app.judge.Status()
	pending := &preparedJudge{client: app.judge, mode: status.Mode, text: text, caseHash: digest(record)}
	switch {
	case record.State == "human" || record.State == "handoff" || record.State == "resolved" || record.Owner == "human" || record.Risk != "" || record.Receipt != "":
		pending.reason = "case_owned_or_closed"
	case rulePriority(classify(text)):
		pending.reason = "rule_priority"
	case !status.Ready:
		pending.reason = status.Reason
	case !app.syntheticSample(text):
		pending.reason = "synthetic_only"
	}
	return pending
}

func (app *App) prepareJudge(input Request) *preparedJudge {
	if input.Action == "message" {
		text := strings.TrimSpace(input.Text)
		record := app.store.Cases[input.CaseID]
		if record == nil || text == "" || len([]rune(text)) > 2000 {
			return nil
		}
		return app.prepareMessageJudge(record, text)
	}
	if input.Action == "judge_compare" {
		status := app.judge.Status()
		pending := &preparedJudge{client: app.judge, mode: status.Mode, samples: slices.Clone(app.dataset.Samples)}
		pending.current = func() bool {
			app.mu.Lock()
			defer app.mu.Unlock()
			return app.judge == pending.client
		}
		if !status.Ready {
			pending.reason = status.Reason
		}
		return pending
	}
	return nil
}

func (pending *preparedJudge) collect() {
	if pending == nil || pending.reason != "" {
		return
	}
	if pending.samples != nil {
		for _, sample := range pending.samples {
			if pending.current != nil && !pending.current() {
				pending.reason = "mode_changed"
				break
			}
			observation := pending.client.Judge(sample.Text)
			pending.report = append(pending.report, observation)
			if observation.Status != "ok" {
				break
			}
		}
		return
	}
	observation := pending.client.Judge(pending.text)
	pending.observation = &observation
}

func (app *App) validatePreparedJudge(input Request, pending *preparedJudge) {
	if pending == nil {
		return
	}
	if pending.client != app.judge {
		pending.reason = "mode_changed"
	}
	if input.Action == "message" && digest(app.store.Cases[input.CaseID]) != pending.caseHash {
		pending.reason = "case_changed"
	}
}

func strongChoice(answer JEVAnswer, choice string, threshold float64) bool {
	probability, exists := answer.Probabilities[choice]
	if !exists || answer.Choice != choice || !(probability >= threshold) {
		return false
	}
	for other, otherProbability := range answer.Probabilities {
		if other != choice && !(probability-otherProbability >= jevProbabilityMargin) {
			return false
		}
	}
	return len(answer.Probabilities) == 3
}

func decideIntake(text, mode, reason string, observation *JEVObservation) IntakeDecision {
	ruleIntent := classify(text)
	decision := IntakeDecision{Mode: mode, Source: "rules", Reason: reason, RuleIntent: ruleIntent, Intent: ruleIntent, MessageHash: digest(text), At: stamp()}
	unavailableReason := "model_unavailable"
	if reason == "" {
		switch {
		case mode == "off":
			decision.Reason = "mode_off"
		case observation == nil:
			decision.Reason = unavailableReason
		case observation.Status != "ok":
			decision.Reason = observation.Error
			if decision.Reason == "" {
				decision.Reason = unavailableReason
			}
		case mode == "shadow":
			decision.Reason = "shadow_only"
		case mode == "assist" && rulePriority(ruleIntent):
			decision.Reason = "rule_priority"
		case mode == "assist":
			human := observation.Answers["human_requested"]
			refund := observation.Answers["refund_progress"]
			switch {
			case strongChoice(human, "yes", jevHumanProbability):
				decision.Source, decision.Intent, decision.Reason = "jev", "human", "model_human"
			case !strongChoice(human, "no", jevHumanProbability):
				decision.Reason = "model_uncertain"
			case strongChoice(refund, "yes", jevRefundProbability):
				decision.Source, decision.Intent, decision.Reason = "jev", "refund_progress", "model_refund"
			case strongChoice(refund, "no", jevRefundProbability):
				decision.Reason = "outside_model_scope"
				if ruleIntent == "refund_progress" {
					decision.Reason = "model_rule_conflict"
				}
			default:
				decision.Reason = "model_uncertain"
			}
		default:
			decision.Reason = "mode_off"
		}
	}
	decision.Fallback = mode == "assist" && decision.Source == "rules" && decision.Reason != "rule_priority" && decision.Reason != "case_owned_or_closed"
	return decision
}

func (app *App) useJudgment(record *Case, text string, pending *preparedJudge) {
	if pending.observation != nil {
		app.labelJudgment(pending.observation, text)
		record.Judgments = append(record.Judgments, *pending.observation)
		record.event("judge_observed", "JEV "+pending.mode+"："+pending.observation.Status+"；原始结果与实际采用的判断分开记录")
		// 落判定账本：只追加，不随会话重置。写失败不阻断裁决，但会记一条事件。
		if app.db != nil {
			if err := app.db.saveJudgment(record.ID, *pending.observation); err != nil {
				record.event("ledger_write_failed", "判定账本写入失败："+err.Error())
			}
		}
	}
	decision := decideIntake(text, app.judge.Status().Mode, pending.reason, pending.observation)
	record.Intake = &decision
	record.event("intake_decided", decision.Source+" / "+decision.Intent+" / "+decision.Reason)
}

func (app *App) intakeIntent(record *Case, text string) string {
	ruleIntent := classify(text)
	if record.Intake != nil && record.Intake.MessageHash == digest(text) && record.Intake.Source == "jev" && !rulePriority(ruleIntent) {
		return record.Intake.Intent
	}
	return ruleIntent
}

func (app *App) switchJudge(mode string) error {
	next, err := app.judge.withMode(mode)
	if err != nil {
		return err
	}
	app.judge = next
	app.store.JEV = next.Status()
	return nil
}

func (app *App) usePreparedReport(pending *preparedJudge) error {
	if pending.reason != "" {
		return errors.New("JEV未就绪或配置已变化，未提交在线对照；请检查开关和服务端配置")
	}
	for index := range pending.report {
		app.labelJudgment(&pending.report[index], pending.samples[index].Text)
	}
	app.store.JudgeReport = pending.report
	return nil
}
