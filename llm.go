package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// The client is server-only. Never serialize credentials or provider response bodies.
type LLMClient struct {
	key, endpoint, model, provider, reason string
	enabled                                bool
	httpClient                             *http.Client
	mu                                     sync.Mutex
	busy                                   bool
	next                                   time.Time
}

type LLMStatus struct {
	Enabled                 bool
	Provider, Model, Reason string
}

type LLMReply struct {
	MessageAt, At, Model, Status, Error, Explanation string
	Sources                                          []string
	LatencyMS                                        int64
	InputTokens, OutputTokens                        int
}

type llmContext struct {
	Decision string   `json:"decision"`
	State    string   `json:"state"`
	Route    string   `json:"route"`
	Ticket   string   `json:"ticket,omitempty"`
	Payment  string   `json:"payment,omitempty"`
	Sources  []string `json:"allowed_sources"`
}

type pendingLLM struct {
	client                      *LLMClient
	caseID, caseHash, messageAt string
	context                     llmContext
}

var bailianEndpoint = regexp.MustCompile(`^https://(dashscope\.aliyuncs\.com|[a-zA-Z0-9-]+\.cn-beijing\.maas\.aliyuncs\.com)/compatible-mode/v1$`)
var llmModelName = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,80}$`)
var llmNumbers = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)

func newLLMFromEnv() *LLMClient {
	return newLLMClient(os.Getenv("LLM_API_KEY"), os.Getenv("LLM_BASE_URL"), os.Getenv("LLM_MODEL"), os.Getenv("LLM_PROVIDER"), os.Getenv("LLM_MODE"))
}

func newLLMClient(key, base, model, provider, mode string) *LLMClient {
	c := &LLMClient{key: strings.TrimSpace(key), model: model, provider: provider, reason: "disabled"}
	if mode != "grounded" {
		return c
	}
	if c.key == "" {
		c.reason = "missing_key"
		return c
	}
	if !llmModelName.MatchString(model) {
		c.reason = "invalid_model"
		return c
	}
	if !(provider == "Bailian" && bailianEndpoint.MatchString(base) || provider == "DeepSeek" && base == "https://api.deepseek.com") {
		c.reason = "invalid_endpoint"
		return c
	}
	c.endpoint = base + "/chat/completions"
	c.httpClient = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c.enabled, c.reason = true, "grounded_synthetic_only"
	return c
}

func (c *LLMClient) Status() LLMStatus {
	if c == nil {
		return LLMStatus{Reason: "disabled"}
	}
	return LLMStatus{Enabled: c.enabled, Provider: c.provider, Model: c.model, Reason: c.reason}
}

// Called while holding app.mu, after the deterministic business action succeeded.
// Only server-generated conclusions are exported. User text, history, human notes,
// editable procedures, order IDs and confirmation tokens are never sent to the LLM.
func (app *App) prepareLLM(input Request) *pendingLLM {
	if !app.llm.Status().Enabled || !slices.Contains([]string{"message", "request_tracking", "refresh_plan"}, input.Action) {
		return nil
	}
	r := app.store.Cases[input.CaseID]
	if r == nil || r.Owner == "human" || r.Risk != "" || r.HumanRequested || r.Receipt != "" || !slices.Contains([]string{"answered", "clarify", "plan"}, r.State) || len(r.Messages) == 0 {
		return nil
	}
	message := r.Messages[len(r.Messages)-1]
	if message.Role != "assistant" {
		return nil
	}
	context := llmContext{Decision: message.Text, State: r.State, Route: r.Route, Sources: []string{"decision"}}
	if r.QueryOrder != nil {
		order, ok := app.dataset.order(r.QueryOrder.ID)
		if !ok || order != *r.QueryOrder || order.Customer != "DEMO-USER" || order.Channel != "sandbox" {
			return nil
		}
		context.Ticket, context.Payment = order.Ticket, order.Payment
		context.Sources = append(context.Sources, "ticket", "payment")
	}
	return &pendingLLM{client: app.llm, caseID: r.ID, caseHash: digest(r), messageAt: message.At, context: context}
}

const groundedPrompt = `你是铁路售后沙箱的解释助手。只解释输入中的服务端已核验结论，不重新分类、不规划工具、不执行操作。
输入是合成状态，不是现实订单。不要要求真实姓名、证件、银行卡或手机号。不要补充银行政策、原因、金额或到账时限。
不得声称本系统已退款、已催办、已提交任务或已获得授权；plan 只是待用户确认。支付 PROCESSING 不能说已到账。
缺少订单时只引导选择合成订单；只读查询后如需跟进，应说明需主动申请并确认方案；不能把查询说成办理。
只输出 JSON 对象：{"explanation":"用自然中文写一段50至120字的简明解释，不用Markdown，不重复时间戳或订单号","sources":["decision"]}。
sources 只能选 allowed_sources；必须包含 decision。结论中没有的信息不要补充。不输出工具、代码、链接或其他字段。`

func (c *LLMClient) generate(ctx context.Context, input llmContext, messageAt string) (result LLMReply) {
	result = LLMReply{MessageAt: messageAt, At: stamp(), Model: c.model, Status: "fallback"}
	started := time.Now()
	defer func() { result.LatencyMS = time.Since(started).Milliseconds() }()
	if !c.enabled {
		result.Error = "disabled"
		return
	}
	c.mu.Lock()
	if c.busy || time.Now().Before(c.next) {
		c.mu.Unlock()
		result.Error = "busy_or_cooldown"
		return
	}
	c.busy = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.busy = false
		if result.Status != "ok" {
			c.next = time.Now().Add(15 * time.Second)
		}
	}()
	payload, _ := json.Marshal(input)
	body := map[string]any{
		"model": c.model, "stream": false, "max_tokens": 512,
		"response_format": map[string]string{"type": "json_object"},
		"messages":        []map[string]string{{"role": "system", "content": groundedPrompt}, {"role": "user", "content": string(payload)}},
	}
	if c.provider == "Bailian" {
		body["enable_thinking"] = false
	} else {
		body["thinking"] = map[string]string{"type": "disabled"}
	}
	data, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		result.Error = "request_error"
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.key)
	response, err := c.httpClient.Do(request)
	if err != nil {
		result.Error = "network_or_timeout"
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("http_%d", response.StatusCode)
		return
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		result.Error = "invalid_response"
		return
	}
	var wire struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Input  int `json:"prompt_tokens"`
			Output int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &wire) != nil || len(wire.Choices) != 1 {
		result.Error = "invalid_response"
		return
	}
	result.InputTokens, result.OutputTokens = wire.Usage.Input, wire.Usage.Output
	if wire.Choices[0].FinishReason != "stop" {
		result.Error = "incomplete_response"
		return
	}
	var answer struct {
		Explanation string   `json:"explanation"`
		Sources     []string `json:"sources"`
	}
	decoder := json.NewDecoder(strings.NewReader(wire.Choices[0].Message.Content))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&answer) != nil || decoder.Decode(new(any)) != io.EOF {
		result.Error = "invalid_json"
		return
	}
	answer.Explanation = strings.TrimSpace(answer.Explanation)
	if !validLLMExplanation(answer.Explanation, answer.Sources, input, c.key) {
		result.Error = "unsupported_explanation"
		return
	}
	result.Status, result.Explanation, result.Sources = "ok", answer.Explanation, answer.Sources
	return
}

// This is a defensive output filter, not a semantic proof. The authoritative
// rule answer stays visible and model prose never feeds the engine or procedures.
func validLLMExplanation(text string, sources []string, input llmContext, key string) bool {
	if len([]rune(text)) < 8 || len([]rune(text)) > 400 || !slices.Contains(sources, "decision") || len(sources) > 3 {
		return false
	}
	for _, source := range sources {
		if !slices.Contains(input.Sources, source) {
			return false
		}
	}
	if key != "" && strings.Contains(text, key) {
		return false
	}
	if containsAny(text, "http:", "https:", "<", ">", "```", "sk-", "银行卡号", "身份证号", "手机号", "已经退款", "已为您退款", "已帮您退款", "保证到账", "保证今天", "一定到账", "已提交追踪", "已创建追踪", "已为您提交", "已帮您提交", "已催办", "自动执行", "无需确认", "不需要确认") {
		return false
	}
	for _, number := range llmNumbers.FindAllString(text, -1) {
		if !strings.Contains(input.Decision, number) {
			return false
		}
	}
	if containsAny(text, "小时内", "工作日内", "天内到账", "明天到账", "今天到账") {
		return false
	}
	if input.Payment != "PAID" {
		negativeRemoved := text
		for _, phrase := range []string{"尚未到账", "还未到账", "未到账", "没有到账", "暂无到账回执", "尚无到账回执", "不代表已到账", "不能认定已到账"} {
			negativeRemoved = strings.ReplaceAll(negativeRemoved, phrase, "")
		}
		if containsAny(negativeRemoved, "已到账", "已退回", "退款成功", "退款已完成") {
			return false
		}
	}
	return true
}

// Called after the external wait, holding app.mu again. A newer customer action
// wins; late model prose must not be attached to a changed case.
func (app *App) attachLLM(pending *pendingLLM, reply LLMReply) bool {
	r := app.store.Cases[pending.caseID]
	if r == nil || app.llm != pending.client || digest(r) != pending.caseHash {
		return false
	}
	r.LLMReplies = append(r.LLMReplies, reply)
	r.event("llm_explanation", reply.Model+" / "+reply.Status+" / "+reply.Error)
	return true
}
