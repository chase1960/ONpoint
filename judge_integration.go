package main

import "errors"

func (app *App) syntheticSample(text string) bool {
	for _, sample := range app.dataset.Samples {
		if sample.Text == text {
			return true
		}
	}
	return false
}

func (app *App) observeJudge(record *Case, text string) {
	pending := app.prepareMessageJudge(record, text)
	pending.collect()
	app.useJudgment(record, text, pending)
}

func (app *App) compareJudge() error {
	if app.judge == nil || !app.judge.Status().Ready {
		return errors.New("JEV未启用：需服务端密钥、合成数据外发许可及 shadow / assist 模式；当前没有在线评测结果")
	}
	report := []JEVObservation{}
	for _, sample := range app.dataset.Samples {
		observation := app.judge.Judge(sample.Text)
		app.labelJudgment(&observation, sample.Text)
		report = append(report, observation)
		if report[len(report)-1].Status != "ok" {
			break
		}
	}
	app.store.JudgeReport = report
	return nil
}

func (app *App) labelJudgment(observation *JEVObservation, text string) {
	for _, sample := range app.dataset.Samples {
		if sample.Text == text {
			observation.SampleID = sample.ID
			observation.LocalIntent = classify(text)
			observation.ExpectedIntent = sample.Intent
			localHuman := explicitHumanRequest(text)
			expectedHuman := sample.ID == "explicit_human" || sample.ID == "human_progress" || sample.ID == "mixed_risk"
			observation.LocalHumanRequested = &localHuman
			observation.ExpectedHumanRequested = &expectedHuman
			return
		}
	}
}
