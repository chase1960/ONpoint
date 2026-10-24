package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const jevMockResponse = `{"model":"jev-mock-1","answers":{"refund_progress":{"type":"choice","choice":"yes","confidence":0.42,"probabilities":{"yes":0.8,"no":0.1,"unclear":0.1}},"human_requested":{"type":"choice","choice":"no","confidence":0.61,"probabilities":{"yes":0.1,"no":0.7,"unclear":0.2}},"plan_response":{"type":"choice","choice":"hesitate","confidence":0.55,"probabilities":{"accept":0.2,"reject":0.25,"hesitate":0.55}},"result_confirmed":{"type":"choice","choice":"no","confidence":0.7,"probabilities":{"yes":0.15,"no":0.7,"unclear":0.15}},"safety_signal":{"type":"choice","choice":"none","confidence":0.9,"probabilities":{"risk":0.1,"none":0.9}},"emotion_level":{"type":"choice","choice":"L2","confidence":0.75,"probabilities":{"L1":0.1,"L2":0.75,"L3":0.1,"L4":0.05}},"arrival_status":{"type":"choice","choice":"not_arrived","confidence":0.82,"probabilities":{"arrived":0.05,"not_arrived":0.82,"partial":0.03,"unclear":0.1}},"returning_issue":{"type":"choice","choice":"no","confidence":0.65,"probabilities":{"yes":0.15,"no":0.65,"unclear":0.2}}},"usage":{"input_tokens":123,"output_tokens":17}}`

// 默认答案的共同特征：置信度低于该题阈值，一律不采用。
// 安全题是例外——按 fail-closed 给足置信度，否则测试里会凭空触发安全闸。
var jevDefaultChoice = map[string]string{
	"refund_progress":  "unclear",
	"human_requested":  "unclear",
	"plan_response":    "hesitate",
	"result_confirmed": "unclear",
	"safety_signal":    "none",
	"emotion_level":    "L1",
	"arrival_status":   "unclear",
	"returning_issue":  "unclear",
}

func lowConfidenceAnswer(id string) JEVAnswer {
	spec := jevQuestionSpecs[id]
	choice := jevDefaultChoice[id]
	confidence := 0.40
	if id == "safety_signal" {
		confidence = 0.90
	}
	remainder := 1.0 - confidence
	others := float64(len(spec.Criteria) - 1)
	probabilities := make(map[string]float64, len(spec.Criteria))
	for option := range spec.Criteria {
		if option == choice {
			probabilities[option] = confidence
		} else {
			probabilities[option] = remainder / others
		}
	}
	return JEVAnswer{Type: "choice", Choice: choice, Confidence: confidence, Probabilities: probabilities}
}

// fullAnswerSet 补齐八个判定点。缺一个就整包作废是刻意设计：宁可整包不信，不采信半包。
func fullAnswerSet(overrides map[string]JEVAnswer) map[string]JEVAnswer {
	answers := make(map[string]JEVAnswer, len(jevQuestionOrder))
	for _, id := range jevQuestionOrder {
		answers[id] = lowConfidenceAnswer(id)
	}
	for id, answer := range overrides {
		answers[id] = answer
	}
	return answers
}

func jevTestEnv(test *testing.T) {
	test.Helper()
	for _, name := range []string{"TYPESAFE_API_KEY", "TYPESAFE_BASE_URL", "TYPESAFE_MODEL", "JEV_MODE", "TYPESAFE_ALLOW_SYNTHETIC_EGRESS", "TYPESAFE_PROXY_URL", "TYPESAFE_TIMEOUT_MS"} {
		test.Setenv(name, "")
	}
}

func jevTestClient(test *testing.T, endpoint string) *JEVClient {
	test.Helper()
	client := newJEVClient("testkey", endpoint, "", "shadow", "true")
	if !client.Status().Ready {
		test.Fatal(client.Status())
	}
	transport := client.httpClient.Transport.(*http.Transport)
	test.Cleanup(transport.CloseIdleConnections)
	return client
}

func jevAssertNoSecrets(test *testing.T, value any) {
	test.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		test.Fatal(err)
	}
	for _, forbidden := range []string{"testkey", "http://", "https://", "PRIVATE_RESPONSE", "Authorization", "Bearer"} {
		if strings.Contains(string(encoded), forbidden) {
			test.Fatalf("result exposes forbidden data: %s", forbidden)
		}
	}
}

func TestJEVEnvDefaultsAndDisabledNoEgress(test *testing.T) {
	jevTestEnv(test)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		fmt.Fprint(writer, jevMockResponse)
	}))
	defer server.Close()
	test.Setenv("TYPESAFE_BASE_URL", server.URL)
	for _, scenario := range []struct {
		name, mode, key, consent, reason string
	}{
		{"default off", "", "testkey", "true", "mode_off"},
		{"explicit off", "off", "testkey", "true", "mode_off"},
		{"active forbidden", "active", "testkey", "true", "mode_off"},
		{"case sensitive mode", "SHADOW", "testkey", "true", "mode_off"},
		{"missing key", "shadow", "", "true", "missing_api_key"},
		{"missing consent", "shadow", "testkey", "", "synthetic_egress_not_allowed"},
		{"uppercase consent", "shadow", "testkey", "TRUE", "synthetic_egress_not_allowed"},
		{"numeric consent", "shadow", "testkey", "1", "synthetic_egress_not_allowed"},
		{"padded consent", "shadow", "testkey", " true ", "synthetic_egress_not_allowed"},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			test.Setenv("JEV_MODE", scenario.mode)
			test.Setenv("TYPESAFE_API_KEY", scenario.key)
			test.Setenv("TYPESAFE_ALLOW_SYNTHETIC_EGRESS", scenario.consent)
			client := newJEVFromEnv()
			status := client.Status()
			if status.Ready || status.Reason != scenario.reason || status.Model != jevDefaultModel {
				test.Fatal(status)
			}
			observation := client.Judge("Synthetic refund progress example")
			if observation.Status != "skipped" || observation.Error != scenario.reason || observation.Answers != nil {
				test.Fatal(observation)
			}
			jevAssertNoSecrets(test, status)
			jevAssertNoSecrets(test, observation)
		})
	}
	if calls.Load() != 0 {
		test.Fatal("disabled client sent a request")
	}
	var absent *JEVClient
	for _, client := range []*JEVClient{absent, {}} {
		if client.Status().Ready || client.Status().Mode != "off" || client.Judge("synthetic").Status != "skipped" {
			test.Fatal("nil or zero client must skip")
		}
	}
}

func TestJEVEndpointAndTransportConfiguration(test *testing.T) {
	jevTestEnv(test)
	test.Setenv("TYPESAFE_API_KEY", "testkey")
	test.Setenv("JEV_MODE", "shadow")
	test.Setenv("TYPESAFE_ALLOW_SYNTHETIC_EGRESS", "true")
	client := newJEVFromEnv()
	if !client.Status().Ready || client.endpoint != "https://api.typesafe.ai/v1/systemone" || client.model != "jev-latest" {
		test.Fatal("incorrect official defaults")
	}
	if client.httpClient.Timeout != 2500*time.Millisecond || client.httpClient.Transport.(*http.Transport).Proxy != nil {
		test.Fatal("timeout or proxy policy is incorrect")
	}
	test.Setenv("TYPESAFE_BASE_URL", "https://internal.example.test/")
	test.Setenv("TYPESAFE_MODEL", "jev-custom")
	client = newJEVFromEnv()
	if !client.Status().Ready || client.endpoint != "https://internal.example.test/v1/systemone" || client.Status().Model != "jev-custom" {
		test.Fatal("explicit HTTPS override not honored")
	}
	jevAssertNoSecrets(test, client.Status())
	for _, endpoint := range []string{
		"http://external.example.test", "http://localhost.example.test", "//api.typesafe.ai", "ftp://localhost",
		"https://testkey@api.typesafe.ai", "https://api.typesafe.ai?key=testkey", "https://api.typesafe.ai?",
		"https://api.typesafe.ai/#testkey", "https://api.typesafe.ai/other", "://broken", "https:///missing-host",
	} {
		test.Run(endpoint, func(test *testing.T) {
			client := newJEVClient("testkey", endpoint, "", "shadow", "true")
			if client.Status().Ready || client.Status().Reason != "invalid_base_url" || client.Judge("synthetic").Status != "skipped" {
				test.Fatal("unsafe endpoint accepted")
			}
			jevAssertNoSecrets(test, client.Status())
		})
	}
	for _, endpoint := range []string{"http://127.0.0.1:1234", "http://[::1]:1234", "http://localhost:1234"} {
		jevTestClient(test, endpoint)
	}
	for _, model := range []string{"testkey", "bad model", "https://private", strings.Repeat("x", 129)} {
		client := newJEVClient("testkey", "", model, "shadow", "true")
		if client.Status().Ready || client.Status().Reason != "invalid_model" {
			test.Fatal("invalid model accepted")
		}
		jevAssertNoSecrets(test, client.Status())
	}
}

func TestJEVBatchedSyntheticTextContract(test *testing.T) {
	const syntheticText = "合成示例：票退了，钱还没回来；不要转人工。"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != "POST" || request.URL.RequestURI() != "/v1/systemone" ||
			request.Header.Get("Authorization") != "Bearer testkey" || request.Header.Get("Content-Type") != "application/json" {
			test.Error("incorrect HTTP contract")
		}
		var input struct {
			State     map[string]string `json:"state"`
			Model     string            `json:"model"`
			Questions map[string]struct {
				Type, Instructions string
				Criteria           map[string]string
			} `json:"questions"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			test.Error(err)
		}
		if decoder.Decode(new(any)) != io.EOF || len(input.State) != 1 || input.State["text"] != syntheticText ||
			input.Model != "jev-latest" || len(input.Questions) != len(jevQuestionOrder) {
			test.Error("request must contain only supplied text and the full question set, without orders or case context")
		}
		for _, name := range jevQuestionOrder {
			question := input.Questions[name]
			if question.Type != "choice" || question.Instructions == "" ||
				len(question.Criteria) != len(jevQuestionSpecs[name].Criteria) {
				test.Error("invalid question contract")
			}
			for choice := range jevQuestionSpecs[name].Criteria {
				if question.Criteria[choice] == "" {
					test.Error("missing choice criterion")
				}
			}
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, jevMockResponse)
	}))
	defer server.Close()
	test.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	test.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	client := jevTestClient(test, server.URL)
	observation := client.Judge(syntheticText)
	if calls.Load() != 1 || observation.Status != "ok" || observation.Error != "" || observation.Mode != "shadow" ||
		observation.Model != "jev-mock-1" || observation.Text != syntheticText || len(observation.Answers) != len(jevQuestionOrder) ||
		observation.InputTokens != 123 || observation.OutputTokens != 17 {
		test.Fatal(observation)
	}
	if observation.Answers["refund_progress"].Confidence != 0.42 || observation.Answers["refund_progress"].Probabilities["yes"] != 0.8 {
		test.Fatal("confidence must remain separate from choice probability")
	}
	if _, err := time.Parse(time.RFC3339Nano, observation.At); err != nil || observation.LatencyMS < 0 {
		test.Fatal("invalid timing metadata")
	}
	jevAssertNoSecrets(test, observation)
	encoded, err := json.Marshal(observation)
	if err != nil {
		test.Fatal(err)
	}
	var external map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &external); err != nil || external["Answers"] == nil || external["InputTokens"] == nil || external["answers"] != nil {
		test.Fatal("external state must retain PascalCase JSON fields")
	}
}

func TestJEVInvalidResponses(test *testing.T) {
	for _, scenario := range []struct{ name, body string }{
		{"malformed", `PRIVATE_RESPONSE testkey`},
		{"missing model", strings.Replace(jevMockResponse, `"model":"jev-mock-1",`, "", 1)},
		{"reflected secret model", strings.Replace(jevMockResponse, "jev-mock-1", "testkey", 1)},
		{"missing answer", strings.Replace(jevMockResponse, `"human_requested"`, `"unexpected"`, 1)},
		{"extra answer", strings.Replace(jevMockResponse, `"answers":{`, `"answers":{"extra":{},`, 1)},
		{"wrong type", strings.Replace(jevMockResponse, `"type":"choice"`, `"type":"score"`, 1)},
		{"unknown choice", strings.Replace(jevMockResponse, `"choice":"yes"`, `"choice":"maybe"`, 1)},
		{"not maximum", strings.Replace(jevMockResponse, `"choice":"yes"`, `"choice":"no"`, 1)},
		{"missing confidence", strings.Replace(jevMockResponse, `"confidence":0.42,`, "", 1)},
		{"null confidence", strings.Replace(jevMockResponse, `"confidence":0.42`, `"confidence":null`, 1)},
		{"negative confidence", strings.Replace(jevMockResponse, `"confidence":0.42`, `"confidence":-0.1`, 1)},
		{"large confidence", strings.Replace(jevMockResponse, `"confidence":0.42`, `"confidence":1.1`, 1)},
		{"missing probability", strings.Replace(jevMockResponse, `,"unclear":0.1`, "", 1)},
		{"null probability", strings.Replace(jevMockResponse, `"yes":0.8`, `"yes":null`, 1)},
		{"unknown probability", strings.Replace(jevMockResponse, `"unclear":0.1`, `"maybe":0.1`, 1)},
		{"negative probability", strings.Replace(jevMockResponse, `"yes":0.8`, `"yes":-0.1`, 1)},
		{"large probability", strings.Replace(jevMockResponse, `"yes":0.8`, `"yes":1.1`, 1)},
		{"overflow probability", strings.Replace(jevMockResponse, `"yes":0.8`, `"yes":1e999`, 1)},
		{"NaN probability", strings.Replace(jevMockResponse, `"yes":0.8`, `"yes":NaN`, 1)},
		{"not distribution", strings.Replace(jevMockResponse, `"yes":0.8`, `"yes":0.9`, 1)},
		{"missing usage", strings.Replace(jevMockResponse, `,"usage":{"input_tokens":123,"output_tokens":17}`, "", 1)},
		{"missing token count", strings.Replace(jevMockResponse, `"input_tokens":123,`, "", 1)},
		{"negative tokens", strings.Replace(jevMockResponse, `"input_tokens":123`, `"input_tokens":-1`, 1)},
		{"fractional tokens", strings.Replace(jevMockResponse, `"input_tokens":123`, `"input_tokens":1.5`, 1)},
		{"unknown field", strings.Replace(jevMockResponse, `"model":`, `"private":"PRIVATE_RESPONSE testkey","model":`, 1)},
		{"trailing JSON", jevMockResponse + `{}`},
		{"trailing garbage", jevMockResponse + `PRIVATE_RESPONSE testkey`},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				fmt.Fprint(writer, scenario.body)
			}))
			defer server.Close()
			observation := jevTestClient(test, server.URL).Judge("synthetic")
			if observation.Status != "error" || observation.Error != "invalid_response" || observation.Answers != nil ||
				observation.InputTokens != 0 || observation.OutputTokens != 0 || calls.Load() != 1 {
				test.Fatal(observation)
			}
			jevAssertNoSecrets(test, observation)
		})
	}
}

func TestJEVFiniteIntervals(test *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.1, 1.1} {
		if jevUnitInterval(&value) {
			test.Fatal("accepted non-finite or out-of-range value")
		}
	}
	for _, value := range []float64{0, 0.5, 1} {
		if !jevUnitInterval(&value) {
			test.Fatal("rejected valid probability")
		}
	}
	if jevUnitInterval(nil) {
		test.Fatal("accepted missing probability")
	}
}

func TestJEVResponseLimit(test *testing.T) {
	for _, size := range []int{jevResponseLimit, jevResponseLimit + 1} {
		test.Run(fmt.Sprint(size), func(test *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				fmt.Fprint(writer, jevMockResponse+strings.Repeat(" ", size-len(jevMockResponse)))
			}))
			defer server.Close()
			observation := jevTestClient(test, server.URL).Judge("synthetic")
			if size == jevResponseLimit && observation.Status != "ok" ||
				size > jevResponseLimit && (observation.Error != "response_too_large" || observation.Answers != nil) {
				test.Fatal(observation)
			}
		})
	}
}

func TestJEVHTTPFailureAndRedirectNeverRetryOrForwardCredentials(test *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		destinationCalls.Add(1)
		if request.Header.Get("Authorization") != "" {
			test.Error("credentials forwarded on redirect")
		}
	}))
	defer destination.Close()
	for _, code := range []int{301, 302, 303, 307, 308, 401, 429, 500, 503} {
		test.Run(fmt.Sprint(code), func(test *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				writer.Header().Set("Location", destination.URL+"/PRIVATE_RESPONSE?key=testkey")
				writer.WriteHeader(code)
				fmt.Fprint(writer, "PRIVATE_RESPONSE testkey")
			}))
			defer server.Close()
			observation := jevTestClient(test, server.URL).Judge("synthetic")
			if observation.Error != "http_error" || observation.Status != "error" || observation.Answers != nil || calls.Load() != 1 {
				test.Fatal(observation)
			}
			jevAssertNoSecrets(test, observation)
		})
	}
	if destinationCalls.Load() != 0 {
		test.Fatal("redirect destination contacted")
	}
}

func TestJEVTimeoutAndMeasuredLatency(test *testing.T) {
	for _, slowBody := range []bool{false, true} {
		test.Run(fmt.Sprint(slowBody), func(test *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				io.Copy(io.Discard, request.Body)
				if slowBody {
					writer.WriteHeader(http.StatusOK)
					writer.(http.Flusher).Flush()
				}
				<-request.Context().Done()
			}))
			defer server.Close()
			started := time.Now()
			observation := jevTestClient(test, server.URL).Judge("synthetic")
			elapsed := time.Since(started).Milliseconds()
			if observation.Status != "error" || observation.Error != "timeout" || observation.Answers != nil || calls.Load() != 1 {
				test.Fatal(observation)
			}
			if observation.LatencyMS < jevTimeout.Milliseconds()-100 || observation.LatencyMS > elapsed || elapsed-observation.LatencyMS > 1000 {
				test.Fatalf("latency not measured: recorded=%d elapsed=%d", observation.LatencyMS, elapsed)
			}
			jevAssertNoSecrets(test, observation)
		})
	}
}

func TestJEVNetworkFailureAndRedaction(test *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {}))
	endpoint := server.URL
	server.Close()
	client := jevTestClient(test, endpoint)
	observation := client.Judge("synthetic testkey")
	if observation.Error != "network_error" || observation.Status != "error" || observation.Text != "synthetic [redacted]" {
		test.Fatal("incorrect sanitized network failure")
	}
	jevAssertNoSecrets(test, observation)
	if observation := client.Judge(" \n\t "); observation.Status != "skipped" || observation.Error != "empty_text" {
		test.Fatal("empty input should skip")
	}
}
