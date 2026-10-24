package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJudgeReportLabelsRealKeywordBaselineAndSyntheticExpectations(test *testing.T) {
	app := testApp(test)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, jevMockResponse) }))
	defer server.Close()
	app.judge = newJEVClient("testkey", server.URL, "", "shadow", "true")
	if err := app.compareJudge(); err != nil {
		test.Fatal(err)
	}
	if len(app.store.JudgeReport) != len(app.dataset.Samples) {
		test.Fatal("incomplete report")
	}
	for position, observation := range app.store.JudgeReport {
		sample := app.dataset.Samples[position]
		if observation.SampleID != sample.ID || observation.ExpectedIntent != sample.Intent || observation.LocalIntent != classify(sample.Text) {
			test.Fatal("invented baseline or expectation")
		}
		if observation.Answers["refund_progress"].Choice != "yes" || observation.Status != "ok" {
			test.Fatal("raw model result was replaced by label")
		}
	}
	if len(app.store.Tasks) != 0 || len(app.store.Cases) != 0 {
		test.Fatal("shadow evaluation mutated business state")
	}
}

func TestFreeInputIsNotLabeledAsSyntheticJudgeEvidence(test *testing.T) {
	app := testApp(test)
	observation := JEVObservation{}
	app.labelJudgment(&observation, "一条未列入合成白名单的输入")
	if observation.SampleID != "" || observation.ExpectedIntent != "" || observation.LocalIntent != "" {
		test.Fatal("free input became labeled test data")
	}
}
