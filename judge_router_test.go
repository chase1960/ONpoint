package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func switchObservation(refund, human string) JEVObservation {
	overrides := map[string]JEVAnswer{}
	for question, choice := range map[string]string{"refund_progress": refund, "human_requested": human} {
		probabilities := map[string]float64{"yes": 0.02, "no": 0.02, "unclear": 0.02}
		probabilities[choice] = 0.96
		overrides[question] = JEVAnswer{Type: "choice", Choice: choice, Confidence: 0.94, Probabilities: probabilities}
	}
	return JEVObservation{Status: "ok", Answers: fullAnswerSet(overrides)}
}

func switchResponse(refund, human string) []byte {
	observation := switchObservation(refund, human)
	answers := map[string]any{}
	for name, answer := range observation.Answers {
		answers[name] = map[string]any{"type": answer.Type, "choice": answer.Choice, "confidence": answer.Confidence, "probabilities": answer.Probabilities}
	}
	content, _ := json.Marshal(map[string]any{"model": "jev-switch-mock", "answers": answers, "usage": map[string]int{"input_tokens": 120, "output_tokens": 14}})
	return content
}

func switchAPI(app *App, input *Request) *httptest.ResponseRecorder {
	method := http.MethodGet
	var body []byte
	if input != nil {
		method = http.MethodPost
		body, _ = json.Marshal(input)
	}
	response := httptest.NewRecorder()
	app.api(response, httptest.NewRequest(method, "http://127.0.0.1:8088/api/state", bytes.NewReader(body)))
	return response
}

func switchApp(test *testing.T, serverURL, extraText string) *App {
	test.Helper()
	app := testApp(test)
	if extraText != "" {
		app.dataset.Samples = append(app.dataset.Samples, Sample{ID: "switch-synthetic", Text: extraText, Intent: "refund_progress"})
		app.store = freshStore(app.dataset)
	}
	app.judge = newJEVClient("testkey", serverURL, "", "assist", "true")
	app.store.JEV = app.judge.Status()
	return app
}

func TestJudgeModesRequireServerConsentAndOffNeverCalls(test *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Write(switchResponse("yes", "no"))
	}))
	defer server.Close()
	for _, consent := range []string{"", "true"} {
		app := testApp(test)
		app.judge = newJEVClient("testkey", server.URL, "", "off", consent)
		record := app.newCase("DEMO-RAIL-001")
		app.observeJudge(record, app.dataset.Samples[0].Text)
		if calls.Load() != 0 || record.Intake.Source != "rules" {
			test.Fatal("off mode sent data")
		}
		err := app.switchJudge("assist")
		if (err == nil) != (consent == "true") {
			test.Fatal("switch bypassed server consent")
		}
	}
	var absent *JEVClient
	if _, err := absent.withMode("assist"); err == nil {
		test.Fatal("missing configuration enabled model")
	}
	if _, err := absent.withMode("active"); err == nil {
		test.Fatal("unsupported mode accepted")
	}
}

func TestIntakeDecisionProbabilityTruthTable(test *testing.T) {
	strong := switchObservation("yes", "no")
	flat := switchObservation("yes", "no")
	answer := flat.Answers["refund_progress"]
	answer.Confidence = 0.99
	answer.Probabilities = map[string]float64{"yes": 0.4, "no": 0.3, "unclear": 0.3}
	flat.Answers["refund_progress"] = answer
	human := switchObservation("unclear", "yes")
	conflict := switchObservation("no", "no")
	for _, scenario := range []struct {
		name, text, mode, source, intent, reason string
		observation                              JEVObservation
	}{
		{"off", "来看看", "off", "rules", "unknown", "mode_off", strong},
		{"shadow", "来看看", "shadow", "rules", "unknown", "shadow_only", strong},
		{"assist read", "来看看", "assist", "jev", "refund_progress", "model_refund", strong},
		{"assist human", "来看看", "assist", "jev", "human", "model_human", human},
		{"uncertain", "查退款", "assist", "rules", "refund_progress", "model_uncertain", flat},
		{"conflict", "查退款", "assist", "rules", "refund_progress", "model_rule_conflict", conflict},
		{"risk", "我要投诉退款", "assist", "rules", "dispute", "rule_priority", strong},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			decision := decideIntake(scenario.text, scenario.mode, "", &scenario.observation)
			if decision.Source != scenario.source || decision.Intent != scenario.intent || decision.Reason != scenario.reason {
				test.Fatal(decision)
			}
		})
	}
}

func TestAssistCanAddReadOnlyUnderstandingOrHandoffButNeverConsent(test *testing.T) {
	const syntheticText = "那笔款一直没影儿，想知道走到哪儿了"
	for _, human := range []string{"no", "yes"} {
		test.Run(human, func(test *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.Write(switchResponse("yes", human)) }))
			defer server.Close()
			app := switchApp(test, server.URL, syntheticText)
			record := app.newCase("DEMO-RAIL-001")
			response := switchAPI(app, &Request{Action: "message", CaseID: record.ID, Text: syntheticText})
			expected := "answered"
			if human == "yes" {
				expected = "handoff"
			}
			if response.Code != 200 || record.State != expected || record.Intake.Source != "jev" || len(record.Plans) != 0 || len(app.store.Tasks) != 0 || record.Consent != "" || record.TrackingRequested {
				test.Fatal("model failed to route safely", response.Code, record.State, record.Intake)
			}
		})
	}
}

func TestRiskRulesAndFreeInputDoNotInvokeOnlineJudge(test *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Write(switchResponse("yes", "no"))
	}))
	defer server.Close()
	app := switchApp(test, server.URL, "")
	for _, text := range []string{app.dataset.Samples[7].Text, "转人工客服", "自由输入：想查退款进度"} {
		record := app.newCase("DEMO-RAIL-001")
		if response := switchAPI(app, &Request{Action: "message", CaseID: record.ID, Text: text}); response.Code != 200 {
			test.Fatal(response.Body.String())
		}
		if record.Intake.Source != "rules" || len(record.Judgments) != 0 {
			test.Fatal("restricted input invoked model")
		}
	}
	if calls.Load() != 0 {
		test.Fatal("data egress bypassed guards")
	}
}

func TestJudgeErrorsAutomaticallyFallBackToOriginalReadOnlyRules(test *testing.T) {
	for _, mode := range []string{"401", "402", "403", "429", "500", "malformed", "timeout", "network"} {
		test.Run(mode, func(test *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if mode == "timeout" {
					io.Copy(io.Discard, request.Body)
					<-request.Context().Done()
					return
				}
				if mode == "malformed" {
					fmt.Fprint(writer, "not-json")
					return
				}
				var status int
				fmt.Sscan(mode, &status)
				writer.WriteHeader(status)
			}))
			defer server.Close()
			app := switchApp(test, server.URL, "")
			if mode == "timeout" {
				app.judge.httpClient.Timeout = 40 * time.Millisecond
			}
			if mode == "network" {
				server.Close()
			}
			record := app.newCase("DEMO-RAIL-001")
			text := "只查退款进度，不申请追踪，不提交任务"
			response := switchAPI(app, &Request{Action: "message", CaseID: record.ID, Text: text})
			if response.Code != 200 || record.State != "answered" || !record.Intake.Fallback || record.Intake.Source != "rules" || len(app.store.Tasks) != 0 || len(record.Judgments) != 1 || record.Judgments[0].Status != "error" {
				test.Fatal(mode, response.Code, record.State, record.Intake)
			}
		})
	}
}

func TestJudgeCooldownSkipsRepeatedLimitedCallsAndCanRecover(test *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if calls.Add(1) == 1 {
			writer.WriteHeader(429)
			return
		}
		writer.Write(switchResponse("yes", "no"))
	}))
	defer server.Close()
	client := jevTestClient(test, server.URL)
	if client.Judge("synthetic").HTTPStatus != 429 || client.Status().Ready || client.Status().Reason != "cooldown" {
		test.Fatal("rate limit did not open cooldown")
	}
	if client.Judge("synthetic").Error != "cooldown" || calls.Load() != 1 {
		test.Fatal("cooldown repeatedly hit provider")
	}
	client.backoff.mu.Lock()
	client.backoff.until = time.Now().Add(-time.Second)
	client.backoff.mu.Unlock()
	if client.Judge("synthetic").Status != "ok" || !client.Status().Ready || calls.Load() != 2 {
		test.Fatal("cooldown never recovered")
	}
}

func TestSlowJudgeDoesNotHoldStateLockOrApplyStaleResults(test *testing.T) {
	for _, change := range []string{"mode", "case", "batch"} {
		test.Run(change, func(test *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce, startOnce sync.Once
			test.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				startOnce.Do(func() { close(started) })
				<-release
				writer.Write(switchResponse("yes", "no"))
			}))
			defer func() { releaseOnce.Do(func() { close(release) }); server.Close() }()
			const text = "那笔款一直没影儿，想知道走到哪儿了"
			app := switchApp(test, server.URL, text)
			record := app.newCase("DEMO-RAIL-001")
			input := Request{Action: "message", CaseID: record.ID, Text: text}
			if change == "batch" {
				input = Request{Action: "judge_compare"}
			}
			finished := make(chan *httptest.ResponseRecorder, 1)
			go func() { finished <- switchAPI(app, &input) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				test.Fatal("mock judge never started")
			}
			changed := make(chan int, 1)
			go func() {
				if response := switchAPI(app, nil); response.Code != 200 {
					changed <- response.Code
					return
				}
				action := Request{Action: "judge_mode", Text: "off"}
				if change == "case" {
					action = Request{Action: "handoff", CaseID: record.ID}
				}
				changed <- switchAPI(app, &action).Code
			}()
			select {
			case code := <-changed:
				if code != 200 {
					test.Fatal("read or switch failed", code)
				}
			case <-time.After(time.Second):
				test.Fatal("model call blocked state read or safety switch")
			}
			releaseOnce.Do(func() { close(release) })
			response := <-finished
			if change == "batch" {
				if response.Code != 409 || calls.Load() != 1 || len(app.store.JudgeReport) != 0 {
					test.Fatal("off switch did not stop batch calls")
				}
			} else if response.Code != 200 || record.Intake.Source != "rules" || len(app.store.Tasks) != 0 || (change == "case" && record.State != "handoff") || (change == "mode" && record.State != "clarify") {
				test.Fatal("stale judgment changed business route", record.State, record.Intake)
			}
		})
	}
}

func TestJudgeModeTransactionFailureRestoresClientAndState(test *testing.T) {
	app := testApp(test)
	app.judge = newJEVClient("testkey", "https://api.typesafe.ai", "", "assist", "true")
	app.store.JEV = app.judge.Status()
	previous := app.judge
	repository, err := openRepository(":memory:")
	if err != nil {
		test.Fatal(err)
	}
	repository.close()
	app.db = repository
	response := switchAPI(app, &Request{Action: "judge_mode", Text: "off"})
	if response.Code != 500 || app.judge != previous || app.store.JEV.Mode != "assist" {
		test.Fatal("failed commit left mismatched mode")
	}
}

func TestJudgeAuditPayloadNeverIncludesAPIKey(test *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input jevRequest
		json.NewDecoder(request.Body).Decode(&input)
		if strings.Contains(input.State.Text, "testkey") {
			test.Error("API key copied into model state")
		}
		writer.Write(switchResponse("yes", "no"))
	}))
	defer server.Close()
	observation := jevTestClient(test, server.URL).Judge("synthetic testkey")
	if observation.QuestionVersion == "" || len(observation.RequestPayload) == 0 {
		test.Fatal("missing replayable question payload")
	}
	jevAssertNoSecrets(test, observation)
}
