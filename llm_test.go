package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type llmTransport func(*http.Request) (*http.Response, error)

func (f llmTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeLLMResponse(code int, text, finish string) *http.Response {
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]string{"content": text}}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 30}})
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}
}

const goodLLMAnswer = `{"explanation":"合成记录显示退票成功，退款仍在支付渠道处理中，尚无到账回执。本次只查询进度；如需跟进，可以申请追踪，方案仍需您确认后才会执行。","sources":["decision","ticket","payment"]}`

func testLLM() *LLMClient {
	return newLLMClient("test-only-secret-key", "https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen3.8-flash", "Bailian", "grounded")
}

func llmAPICall(app *App, input Request) *httptest.ResponseRecorder {
	data, _ := json.Marshal(input)
	writer := httptest.NewRecorder()
	app.api(writer, httptest.NewRequest(http.MethodPost, "/api/state", bytes.NewReader(data)))
	return writer
}

func TestLLMActualAPILifecycleAndPersistence(t *testing.T) {
	app := testApp(t)
	repo, err := openRepository(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.close()
	app.db, app.llm = repo, testLLM()
	var calls int
	app.llm.httpClient.Transport = llmTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		for _, private := range []string{"private-customer-text", "13812345678", "DEMO-RAIL-001", "test-only-secret-key"} {
			if strings.Contains(string(body), private) {
				t.Errorf("private input included in model request: %s", private)
			}
		}
		if r.Header.Get("Authorization") != "Bearer test-only-secret-key" {
			t.Error("missing credential")
		}
		var request map[string]any
		if json.Unmarshal(body, &request) != nil || request["model"] != "qwen3.8-flash" || request["enable_thinking"] != false {
			t.Error("wrong provider contract")
		}
		return fakeLLMResponse(200, goodLLMAnswer, "stop"), nil
	})
	r := app.newCase("DEMO-RAIL-001")
	w := llmAPICall(app, Request{Action: "message", CaseID: r.ID, Text: "private-customer-text 13812345678 帮我查退款进度"})
	if w.Code != 200 || calls != 1 || len(r.LLMReplies) != 1 || r.LLMReplies[0].Status != "ok" {
		t.Fatal("API did not consume model explanation", w.Code, r.LLMReplies)
	}
	if r.State != "answered" || len(app.store.Tasks) != 0 || r.QueryOrder.Payment != "PROCESSING" {
		t.Fatal("model changed business state")
	}
	if strings.Contains(w.Body.String(), "test-only-secret-key") {
		t.Fatal("key leaked to browser")
	}
	saved, err := repo.load(app.dataset)
	if err != nil || len(saved.Cases[r.ID].LLMReplies) != 1 {
		t.Fatal("model receipt not persisted", err)
	}
}

func TestLLMFailuresKeepCanonicalAnswerAndNoTask(t *testing.T) {
	cases := []struct {
		name                 string
		code                 int
		answer, finish, want string
	}{
		{"permission", 403, "secret-provider-body", "stop", "http_403"},
		{"rate_limit", 429, "", "stop", "http_429"},
		{"truncated", 200, goodLLMAnswer, "length", "incomplete_response"},
		{"bad_json", 200, "not JSON", "stop", "invalid_json"},
		{"tool_injection", 200, `{"explanation":"正常解释文字足够长","sources":["decision"],"tools":["refund"]}`, "stop", "invalid_json"},
		{"paid_invention", 200, `{"explanation":"退款已到账，您可以直接结束咨询。","sources":["decision"]}`, "stop", "unsupported_explanation"},
		{"timing_invention", 200, `{"explanation":"退款将在24小时内到账，请您耐心等待。","sources":["decision"]}`, "stop", "unsupported_explanation"},
		{"unknown_source", 200, `{"explanation":"退款处理中，请等待已核验的后续结果。","sources":["decision","invented_bank"]}`, "stop", "unsupported_explanation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := testApp(t)
			app.llm = testLLM()
			app.llm.httpClient.Transport = llmTransport(func(*http.Request) (*http.Response, error) {
				return fakeLLMResponse(tc.code, tc.answer, tc.finish), nil
			})
			r := app.newCase("DEMO-RAIL-001")
			w := llmAPICall(app, Request{Action: "message", CaseID: r.ID, Text: "只查退款进度"})
			if w.Code != 200 || r.State != "answered" || len(app.store.Tasks) != 0 || len(r.LLMReplies) != 1 || r.LLMReplies[0].Error != tc.want || r.LLMReplies[0].Explanation != "" {
				t.Fatal("failure did not fall back", w.Code, r.LLMReplies)
			}
			if !strings.Contains(r.Messages[len(r.Messages)-1].Text, "查询结果") {
				t.Fatal("lost canonical answer")
			}
		})
	}
}

func TestLLMDoesNotBlockReadsOrOverwriteHumanHandoff(t *testing.T) {
	app := testApp(t)
	app.llm = testLLM()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	app.llm.httpClient.Transport = llmTransport(func(r *http.Request) (*http.Response, error) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return fakeLLMResponse(200, goodLLMAnswer, "stop"), nil
	})
	r := app.newCase("DEMO-RAIL-001")
	go func() {
		defer close(done)
		llmAPICall(app, Request{Action: "message", CaseID: r.ID, Text: "只查退款进度"})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model not started")
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		w := httptest.NewRecorder()
		app.api(w, httptest.NewRequest("GET", "/api/state", nil))
		llmAPICall(app, Request{Action: "handoff", CaseID: r.ID})
	}()
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		close(release)
		<-done
		t.Fatal("network blocked reads or handoff")
	}
	close(release)
	<-done
	if r.State != "handoff" || len(r.LLMReplies) != 0 {
		t.Fatal("late model output attached to changed case")
	}
}

func TestLLMTimeoutRedirectAndDisabledEndpoints(t *testing.T) {
	for _, base := range []string{"http://dashscope.aliyuncs.com/compatible-mode/v1", "https://dashscope.aliyuncs.com.evil.test/compatible-mode/v1", "https://example.com", "https://dashscope.aliyuncs.com/compatible-mode/v1?leak=1"} {
		if newLLMClient("test", base, "qwen3.8-flash", "Bailian", "grounded").Status().Enabled {
			t.Fatal("unsafe endpoint accepted")
		}
	}
	input := llmContext{Decision: "退款处理中", Sources: []string{"decision"}, Payment: "PROCESSING"}
	c := testLLM()
	var calls atomic.Int32
	c.httpClient.Transport = llmTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		response := fakeLLMResponse(302, "", "stop")
		response.Header.Set("Location", "https://example.com/leak")
		return response, nil
	})
	if result := c.generate(context.Background(), input, "now"); result.Error != "http_302" || calls.Load() != 1 {
		t.Fatal("redirect followed", result)
	}
	c = testLLM()
	c.httpClient.Transport = llmTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if result := c.generate(ctx, input, "now"); result.Error != "network_or_timeout" {
		t.Fatal("timeout did not fall back")
	}
	c = testLLM()
	c.httpClient.Transport = llmTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("network failure secret") })
	if result := c.generate(context.Background(), input, "now"); strings.Contains(result.Error, "secret") {
		t.Fatal("raw network error leaked")
	}
}

func TestLLMRiskAndConfirmedActionsDoNotCallModel(t *testing.T) {
	app := testApp(t)
	app.llm = testLLM()
	app.llm.httpClient.Transport = llmTransport(func(*http.Request) (*http.Response, error) {
		t.Error("risk path called model")
		return nil, errors.New("unexpected")
	})
	r := app.newCase("DEMO-RAIL-001")
	llmAPICall(app, Request{Action: "message", CaseID: r.ID, Text: "我要投诉并追究责任"})
	if r.State != "handoff" || len(r.LLMReplies) != 0 {
		t.Fatal("risk path changed")
	}
	source := completedHumanCase(t, app)
	if pending := app.prepareLLM(Request{Action: "confirm", CaseID: source.ID}); pending != nil {
		t.Fatal("confirmation exported data")
	}
}

func TestLLMLiveSyntheticMVP(t *testing.T) {
	if os.Getenv("ONPOINT_TEST_LLM_LIVE") != "1" {
		t.Skip("explicit live test opt-in required")
	}
	app := testApp(t)
	app.llm = newLLMFromEnv()
	if !app.llm.Status().Enabled {
		t.Fatal("LLM not configured")
	}
	repo, err := openRepository(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.close()
	app.db = repo
	change := candidate(t, app)
	publish(t, app, change)
	r := app.newCase("DEMO-RAIL-002")
	report := []LLMReply{}
	for _, input := range []Request{{Action: "message", CaseID: r.ID, Text: "只查退款进度，不申请追踪，不提交任务"}, {Action: "request_tracking", CaseID: r.ID}} {
		before := len(r.LLMReplies)
		w := llmAPICall(app, input)
		if w.Code != 200 || len(r.LLMReplies) != before+1 {
			t.Fatal("live API did not record model result", w.Code)
		}
		reply := r.LLMReplies[len(r.LLMReplies)-1]
		if reply.Status != "ok" {
			t.Fatalf("model failed: %s (no raw provider response printed)", reply.Error)
		}
		report = append(report, reply)
		t.Logf("model=%s action=%s status=%s tokens=%d+%d latency_ms=%d explanation=%s", reply.Model, input.Action, reply.Status, reply.InputTokens, reply.OutputTokens, reply.LatencyMS, reply.Explanation)
	}
	if r.State != "plan" || len(app.store.Tasks) != 1 || r.QueryOrder.Payment != "PROCESSING" {
		t.Fatal("model altered authorization or payment")
	}
	if err := app.apply(Request{Action: "confirm", CaseID: r.ID, PlanID: r.Plans[0].ID, PlanToken: r.Plans[0].Token}); err == nil {
		t.Fatal("unconfirmed plan executed")
	}
	confirmPlan(t, app, r)
	if r.Receipt == "" || len(app.store.Tasks) != 2 || r.QueryOrder.Payment != "PROCESSING" {
		t.Fatal("confirmed sandbox tool failed")
	}
	data, _ := json.MarshalIndent(map[string]any{"model": app.llm.model, "replies": report, "confirmation_gate_passed": true, "sandbox_receipt": r.Receipt, "payment_unchanged": r.QueryOrder.Payment}, "", "  ")
	if err := os.WriteFile("cache/llm-live-mvp.json", data, 0600); err != nil {
		t.Fatal(err)
	}
}
