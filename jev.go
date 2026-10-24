package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	jevDefaultBaseURL = "https://api.typesafe.ai"
	jevDefaultModel   = "jev-latest"
	jevTimeout        = 2500 * time.Millisecond
	jevResponseLimit  = 64 << 10
)

// 判定点清单：八个判定点，每题独立版本、独立阈值。
// 题面是英文——问法本身就是参数，改问法 = 改判定，必须升版本并跑回放基线。
// 八题放在一次请求里并发提交，等待时间只算一次，这是前置分流的成本基础。
type jevQuestionSpec struct {
	ID           string
	Version      string
	Instructions string
	Criteria     map[string]string
	Threshold    float64
	FailClosed   bool
	Note         string
}

// 顺序固定，用于生成组合指纹与校验，不依赖 map 遍历顺序。
var jevQuestionOrder = []string{"refund_progress", "human_requested", "plan_response",
	"result_confirmed", "safety_signal", "emotion_level", "arrival_status", "returning_issue"}

// 阈值不是拍脑袋定的，是按「判错的代价方向」定的：
// 转人工不可逆 → 门槛最高 0.85；安全漏判代价极高 → 门槛最低 0.50 且 fail-closed。
var jevQuestionSpecs = map[string]jevQuestionSpec{
	"refund_progress": {
		// rp.v2：这一题只判「业务归属」，不判「办理意愿」。
		// v1 把"只查进度"判成 no，路由又把 no 排除在退款进度之外，
		// 于是正常口语的"钱到哪了"会被卡在 unknown——题目自己和路由打架。
		// 办理意愿本来就不该让模型猜：它由明确的追踪申请和确认按钮承载。
		ID: "refund_progress", Version: "rp.v2", Threshold: 0.80,
		Instructions: "Is this request about the progress or arrival of an existing refund? Answer yes even if the user only asks to be told the status and does not ask us to act.",
		Criteria: map[string]string{
			"yes":     "Asks about the progress, status, timing or arrival of an existing refund, including when they only want to be told.",
			"no":      "Not about an existing refund's progress: a different request, or an unrelated topic.",
			"unclear": "Ambiguous or too short to determine.",
		},
		Note: "只判业务归属，不判办理意愿；是否动手由明确的追踪申请与确认按钮决定",
	},
	"human_requested": {
		ID: "human_requested", Version: "hr.v1", Threshold: 0.85,
		Instructions: "Does the user explicitly ask to be transferred to a human agent?",
		Criteria: map[string]string{
			"yes":     "Explicitly asks for a human, a real person, or human handling.",
			"no":      "Does not ask for a human, or explicitly says NOT to transfer to a human.",
			"unclear": "The word human appears inside a complaint or a rhetorical question.",
		},
		Note: "抱怨与反问里的『人工』不当成要求转接",
	},
	"plan_response": {
		ID: "plan_response", Version: "pr.v1", Threshold: 0.80,
		Instructions: "How does the user respond to the plan just presented?",
		Criteria: map[string]string{
			"accept":   "Clearly agrees to the plan.",
			"reject":   "Clearly declines or asks to cancel.",
			"hesitate": "Hedging, questioning, or not clearly committing either way.",
		},
		Note: "判出接受 = 只把确认按钮亮出来，执行要用户亲手点",
	},
	"result_confirmed": {
		ID: "result_confirmed", Version: "rc.v1", Threshold: 0.80,
		Instructions: "Does the user say the issue is resolved?",
		Criteria: map[string]string{
			"yes":     "Clearly says the issue is resolved or the money arrived.",
			"no":      "Clearly says it is not resolved yet.",
			"unclear": "Ambiguous, or the user did not say.",
		},
		Note: "判出解决 = 只预选结案，仍需用户确认",
	},
	"safety_signal": {
		ID: "safety_signal", Version: "ss.v1", Threshold: 0.50, FailClosed: true,
		Instructions: "Does the text contain any safety or crisis signal?",
		Criteria: map[string]string{
			"risk": "Contains self-harm, fraud escalation, legal threat, or physical danger.",
			"none": "No such signal at all.",
		},
		Note: "刻意没有 unclear 选项：判不出来就按有风险处理",
	},
	"emotion_level": {
		ID: "emotion_level", Version: "el.v1", Threshold: 0.70,
		Instructions: "What is the user's emotional level?",
		Criteria: map[string]string{
			"L1": "Calm and factual.",
			"L2": "Dissatisfied but still cooperative.",
			"L3": "Angry, blaming, or threatening to escalate.",
			"L4": "Extreme: abusive, or threatening serious consequences.",
		},
		Note: "L4 收紧的是自动化程度，不是拒绝服务",
	},
	"arrival_status": {
		ID: "arrival_status", Version: "as.v1", Threshold: 0.80,
		Instructions: "What does the user say about whether the money arrived?",
		Criteria: map[string]string{
			"arrived":     "Money has arrived.",
			"not_arrived": "Money has not arrived.",
			"partial":     "Only part of it arrived.",
			"unclear":     "Cannot determine.",
		},
		Note: "驱动到账时钟：判出到账才关钟，判不出就继续跑",
	},
	"returning_issue": {
		ID: "returning_issue", Version: "ri.v1", Threshold: 0.70,
		Instructions: "Is the user referring to an issue they raised before?",
		Criteria: map[string]string{
			"yes":     "Refers to a previous conversation, ticket, or tracking task.",
			"no":      "This is a new issue.",
			"unclear": "Cannot determine.",
		},
		Note: "判出老问题 = 跳过重复收集，但不跳过验证",
	},
}

// jevVersionMap 题面版本表，供回放报告与账本展示使用。
func jevVersionMap() map[string]string {
	versions := make(map[string]string, len(jevQuestionOrder))
	for _, id := range jevQuestionOrder {
		versions[id] = jevQuestionSpecs[id].Version
	}
	return versions
}

// questionSetVersion 是这批题面的组合指纹，顺序固定，可直接比较。
func questionSetVersion() string {
	parts := make([]string, 0, len(jevQuestionOrder))
	for _, id := range jevQuestionOrder {
		parts = append(parts, jevQuestionSpecs[id].Version)
	}
	return strings.Join(parts, "+")
}

type JEVStatus struct {
	Mode, Model, Reason string
	Ready               bool
	Configured          bool
	ConfigurationReason string
	CooldownUntil       string
}

type JEVObservation struct {
	HTTPStatus                                  int
	QuestionVersion                             string
	RequestPayload                              json.RawMessage
	LocalHumanRequested, ExpectedHumanRequested *bool
	SampleID, LocalIntent, ExpectedIntent       string
	At, Text, Model, Mode, Status, Error        string
	LatencyMS                                   int64
	Answers                                     map[string]JEVAnswer
	InputTokens, OutputTokens                   int
}

type JEVAnswer struct {
	Type, Choice  string
	Confidence    float64
	Probabilities map[string]float64
}

type JEVClient struct {
	key, endpoint, model, mode, reason string
	httpClient                         *http.Client
	backoff                            *jevBackoff
}

type jevBackoff struct {
	mu       sync.Mutex
	failures int
	until    time.Time
}

type jevQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type jevRequest struct {
	State struct {
		Text string `json:"text"`
	} `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]jevQuestion `json:"questions"`
}

type jevResponse struct {
	Model   string                       `json:"model"`
	Answers map[string]jevResponseAnswer `json:"answers"`
	Usage   *struct {
		InputTokens  *int `json:"input_tokens"`
		OutputTokens *int `json:"output_tokens"`
	} `json:"usage"`
}

type jevResponseAnswer struct {
	Type          string              `json:"type"`
	Choice        string              `json:"choice"`
	Confidence    *float64            `json:"confidence"`
	Probabilities map[string]*float64 `json:"probabilities"`
}

func newJEVFromEnv() *JEVClient {
	client := newJEVClient(os.Getenv("TYPESAFE_API_KEY"), os.Getenv("TYPESAFE_BASE_URL"),
		os.Getenv("TYPESAFE_MODEL"), os.Getenv("JEV_MODE"), os.Getenv("TYPESAFE_ALLOW_SYNTHETIC_EGRESS"))
	return client.withNetwork(os.Getenv("TYPESAFE_PROXY_URL"), os.Getenv("TYPESAFE_TIMEOUT_MS"))
}

func newJEVClient(key, baseURL, model, mode, allowSyntheticEgress string) *JEVClient {
	client := &JEVClient{key: strings.TrimSpace(key), model: jevDefaultModel, mode: "off", reason: "missing_api_key", backoff: &jevBackoff{}}
	if mode == "shadow" || mode == "assist" {
		client.mode = mode
	}
	if model != "" {
		if !jevValidModel(model, client.key) {
			client.reason = "invalid_model"
			return client
		}
		client.model = model
	}
	if client.key == "" {
		client.reason = "missing_api_key"
		return client
	}
	if allowSyntheticEgress != "true" {
		client.reason = "synthetic_egress_not_allowed"
		return client
	}
	if baseURL == "" {
		baseURL = jevDefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" {
		client.reason = "invalid_base_url"
		return client
	}
	host := parsed.Hostname()
	address := net.ParseIP(host)
	loopback := host == "localhost" || (address != nil && address.IsLoopback())
	if host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback)) {
		client.reason = "invalid_base_url"
		return client
	}
	parsed.Path = "/v1/systemone"
	client.endpoint = parsed.String()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	client.httpClient = &http.Client{
		Timeout:   jevTimeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	client.reason = "ready"
	return client
}

func (client *JEVClient) Status() JEVStatus {
	if client == nil || client.mode == "" {
		return JEVStatus{Mode: "off", Model: jevDefaultModel, Reason: "mode_off"}
	}
	status := JEVStatus{Mode: client.mode, Model: client.model, Reason: client.reason,
		Configured: client.reason == "ready" && client.key != "" && client.httpClient != nil, ConfigurationReason: client.reason}
	status.Ready = status.Configured && (client.mode == "shadow" || client.mode == "assist")
	if client.mode == "off" {
		status.Reason = "mode_off"
	}
	if client.backoff != nil {
		client.backoff.mu.Lock()
		if time.Now().Before(client.backoff.until) {
			status.CooldownUntil = client.backoff.until.UTC().Format(time.RFC3339Nano)
			if client.mode != "off" {
				status.Ready = false
				status.Reason = "cooldown"
			}
		}
		client.backoff.mu.Unlock()
	}
	return status
}

func (client *JEVClient) withMode(mode string) (*JEVClient, error) {
	if mode != "off" && mode != "shadow" && mode != "assist" {
		return nil, errors.New("JEV模式只支持 off / shadow / assist")
	}
	if client == nil {
		client = newJEVClient("", "", "", "off", "")
	}
	if mode != "off" && !client.Status().Configured {
		return nil, errors.New("不能启用JEV：服务端密钥、合成数据外发许可或接口配置未就绪")
	}
	next := *client
	next.mode = mode
	return &next, nil
}

func (client *JEVClient) recordResult(observation JEVObservation) {
	if client == nil || client.backoff == nil || observation.Status == "skipped" {
		return
	}
	client.backoff.mu.Lock()
	defer client.backoff.mu.Unlock()
	if observation.Status == "ok" {
		client.backoff.failures = 0
		return
	}
	client.backoff.failures++
	if observation.HTTPStatus == 401 || observation.HTTPStatus == 402 || observation.HTTPStatus == 403 || observation.HTTPStatus == 429 || client.backoff.failures >= 3 {
		client.backoff.until = time.Now().Add(30 * time.Second)
	}
}

func (client *JEVClient) Judge(text string) (observation JEVObservation) {
	started := time.Now()
	status := client.Status()
	observation = JEVObservation{At: started.UTC().Format(time.RFC3339Nano), Text: text,
		Model: status.Model, Mode: status.Mode, Status: "skipped"}
	defer func() { observation.LatencyMS = time.Since(started).Milliseconds() }()
	defer func() { client.recordResult(observation) }()
	if client != nil && client.key != "" {
		observation.Text = strings.ReplaceAll(text, client.key, "[redacted]")
	}
	if !status.Ready {
		observation.Error = status.Reason
		return observation
	}
	if strings.TrimSpace(text) == "" {
		observation.Error = "empty_text"
		return observation
	}
	// 八题并发：一次请求提交全部判定点，等待时间只算一次。
	questions := make(map[string]jevQuestion, len(jevQuestionOrder))
	for _, id := range jevQuestionOrder {
		spec := jevQuestionSpecs[id]
		questions[id] = jevQuestion{Type: "choice", Instructions: spec.Instructions, Criteria: spec.Criteria}
	}
	input := jevRequest{Model: client.model, Questions: questions}
	input.State.Text = observation.Text
	body, err := json.Marshal(input)
	if err != nil {
		observation.Status, observation.Error = "error", "invalid_request"
		return observation
	}
	observation.QuestionVersion = questionSetVersion()
	observation.RequestPayload = json.RawMessage(body)
	ctx, cancel := context.WithTimeout(context.Background(), client.httpClient.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		observation.Status, observation.Error = "error", "invalid_request"
		return observation
	}
	request.Header.Set("Authorization", "Bearer "+client.key)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		observation.Status, observation.Error = "error", jevNetworkError(err)
		return observation
	}
	defer response.Body.Close()
	observation.HTTPStatus = response.StatusCode
	observation.Status = "error"
	if response.StatusCode != http.StatusOK {
		observation.Error = "http_error"
		return observation
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, jevResponseLimit+1))
	if err != nil {
		observation.Error = jevNetworkError(err)
		return observation
	}
	if len(content) > jevResponseLimit {
		observation.Error = "response_too_large"
		return observation
	}
	var result jevResponse
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		observation.Error = "invalid_response"
		return observation
	}
	if decoder.Decode(new(any)) != io.EOF || !jevValidResponse(result, client.key) {
		observation.Error = "invalid_response"
		return observation
	}
	observation.Answers = make(map[string]JEVAnswer, len(jevQuestionOrder))
	for question, answer := range result.Answers {
		probabilities := make(map[string]float64, 3)
		for choice, probability := range answer.Probabilities {
			probabilities[choice] = *probability
		}
		observation.Answers[question] = JEVAnswer{Type: answer.Type, Choice: answer.Choice,
			Confidence: *answer.Confidence, Probabilities: probabilities}
	}
	observation.Model = result.Model
	observation.InputTokens, observation.OutputTokens = *result.Usage.InputTokens, *result.Usage.OutputTokens
	observation.Status = "ok"
	return observation
}

func jevValidModel(model, key string) bool {
	if model == "" || len(model) > 128 || (key != "" && strings.Contains(model, key)) {
		return false
	}
	for _, character := range model {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func jevValidResponse(result jevResponse, key string) bool {
	if !jevValidModel(result.Model, key) || len(result.Answers) != len(jevQuestionOrder) || result.Usage == nil ||
		result.Usage.InputTokens == nil || result.Usage.OutputTokens == nil ||
		*result.Usage.InputTokens < 0 || *result.Usage.OutputTokens < 0 {
		return false
	}
	for _, question := range jevQuestionOrder {
		spec := jevQuestionSpecs[question]
		answer, exists := result.Answers[question]
		if !exists || answer.Type != "choice" || !jevUnitInterval(answer.Confidence) ||
			len(answer.Probabilities) != len(spec.Criteria) {
			return false
		}
		selected := answer.Probabilities[answer.Choice]
		if !jevUnitInterval(selected) {
			return false
		}
		sum := 0.0
		for choice := range spec.Criteria {
			probability := answer.Probabilities[choice]
			if !jevUnitInterval(probability) || *selected < *probability {
				return false
			}
			sum += *probability
		}
		if math.Abs(sum-1) > 1e-6 {
			return false
		}
	}
	return true
}

func jevUnitInterval(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0 && *value <= 1
}

// adopted 过线才采用（只加闸的铁律）。没过线的一律不采用，只写进判定账本备查。
// 这是「错误代价天花板」的落地点：判错最坏是让用户多点一下，而不是产生一个越权动作。
func (observation JEVObservation) adopted(id string) bool {
	answer, exists := observation.Answers[id]
	if !exists || observation.Status != "ok" {
		return false
	}
	return answer.Confidence >= jevQuestionSpecs[id].Threshold
}

// adoptedCount 用于展示：八题里真被当成事实的有几题。
func (observation JEVObservation) adoptedCount() (adopted, total int) {
	total = len(jevQuestionOrder)
	for _, id := range jevQuestionOrder {
		if observation.adopted(id) {
			adopted++
		}
	}
	return adopted, total
}

// safetyRisk 安全题 fail-closed：判不出来 = 按有风险处理。
func (observation JEVObservation) safetyRisk() bool {
	if !observation.adopted("safety_signal") {
		return true
	}
	return observation.Answers["safety_signal"].Choice == "risk"
}

// emotionLevel 取情绪等级。判不出来按 L1：收紧权限要有依据，不能凭空收紧。
func (observation JEVObservation) emotionLevel() string {
	if !observation.adopted("emotion_level") {
		return "L1"
	}
	return observation.Answers["emotion_level"].Choice
}

func jevNetworkError(err error) string {
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		return "timeout"
	}
	return "network_error"
}
