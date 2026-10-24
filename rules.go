package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// 业务规则外置（P0-1）。内核只读规则文件，不读硬编码常量——
// 「加一个业务 = 改一份规则」，裁决代码零改动。规则文件缺失或校验不过就拒绝启动，不静默回退。

type IntentRule struct {
	ID, Level, Action, Reason, Matcher string
	Words                              []string
	Note                               string
}

type SpecialIntent struct {
	ID, Level, Action, Reason string
	Words, Negations          []string
	Note                      string
}

type EligibilityRule struct {
	Business, Channel, Ticket, Payment, Reason string
}

type SafetyRule struct {
	Enabled      bool     `json:"enabled"`
	CrisisWords  []string `json:"crisis_words"`
	HazardWords  []string `json:"hazard_words"`
	CrisisReason string   `json:"crisis_reason"`
	HazardReason string   `json:"hazard_reason"`
	Note         string   `json:"note"`
}

// SLARule 是承诺时钟：把"什么时候到"从一句安慰变成一个有时间点、会到期、到期会主动开口的承诺。
// demo_due_in_minutes 只影响沙箱演示节奏（路演现场几分钟就能看到"到期→主动触达"全过程），
// 不影响真实性：承诺时长、文案与提醒口径全部写在规则文件里，代码不硬编码。
type SLARule struct {
	Enabled        bool   `json:"enabled"`
	PromiseHours   int    `json:"promise_hours"`
	DemoDueMinutes int    `json:"demo_due_in_minutes"`
	SoonHours      int    `json:"soon_hours"`
	PromiseText    string `json:"promise_text"`
	SoonText       string `json:"soon_text"`
	DueText        string `json:"due_text"`
	Note           string `json:"note"`
}

// promiseMinutes 承诺时长（分钟）。演示开关优先，未开则用真实小时数。
func (rules *RuleSet) promiseMinutes() int {
	if rules.SLA.DemoDueMinutes > 0 {
		return rules.SLA.DemoDueMinutes
	}
	return rules.SLA.PromiseHours * 60
}

type RuleSet struct {
	Version, Note  string
	Intents        []IntentRule
	SpecialIntents struct {
		HumanRequest SpecialIntent `json:"human_request"`
		Tracking     SpecialIntent `json:"tracking"`
		ReadOnly     SpecialIntent `json:"read_only"`
	} `json:"special_intents"`
	Eligibility        EligibilityRule
	Safety             SafetyRule
	EvidenceTTLMinutes int      `json:"evidence_ttl_minutes"`
	SLA                SLARule  `json:"sla"`
	WorkflowSteps      []string `json:"workflow_steps"`
	StopConditions     []string `json:"stop_conditions"`
}

// loadRules 读规则文件并做严格校验。校验失败直接返回错误，由 main 终止启动。
func loadRules(path string) (*RuleSet, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读规则文件失败（%s）：%w", path, err)
	}
	var rules RuleSet
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rules); err != nil {
		return nil, fmt.Errorf("规则文件解析失败：%w", err)
	}
	if err := rules.validate(); err != nil {
		return nil, fmt.Errorf("规则文件校验失败：%w", err)
	}
	return &rules, nil
}

// validate：规则是裁决的依据，形状不对就不能跑。
func (rules *RuleSet) validate() error {
	if strings.TrimSpace(rules.Version) == "" {
		return errors.New("缺少 version")
	}
	if len(rules.Intents) == 0 {
		return errors.New("缺少 intents")
	}
	// 同一意图允许多条触发条件（句子的说法不同、优先级位置不同），但动作与等级必须一致；
	// 否则「同一句话换个说法就走不同闸门」这种事会悄悄发生。
	seen := map[string]IntentRule{}
	for _, intent := range rules.Intents {
		if intent.ID == "" {
			return errors.New("intents 里有缺 ID 的条目")
		}
		if previous, exists := seen[intent.ID]; exists {
			if previous.Action != intent.Action || previous.Level != intent.Level {
				return errors.New("intent " + intent.ID + " 的多条触发条件动作或等级不一致")
			}
		}
		seen[intent.ID] = intent
		if intent.Action == "" {
			return errors.New("intent " + intent.ID + " 缺少 action")
		}
		// 没有关键词就必须声明匹配器，否则这一条永远命中不了，等于悄悄少了一条规则。
		if len(intent.Words) == 0 && intent.Matcher == "" {
			return errors.New("intent " + intent.ID + " 既无关键词也无匹配器")
		}
		switch intent.Matcher {
		case "", "human_request", "tracking":
		default:
			return errors.New("intent " + intent.ID + " 的匹配器未知：" + intent.Matcher)
		}
	}
	if rules.Eligibility.Business == "" || rules.Eligibility.Channel == "" ||
		rules.Eligibility.Ticket == "" || rules.Eligibility.Payment == "" {
		return errors.New("eligibility 四元组不能为空（业务/渠道/退票/支付）")
	}
	if len(rules.WorkflowSteps) == 0 {
		return errors.New("缺少 workflow_steps")
	}
	if len(rules.StopConditions) == 0 {
		return errors.New("缺少 stop_conditions：禁用条件不能被删掉")
	}
	if rules.EvidenceTTLMinutes <= 0 {
		return errors.New("evidence_ttl_minutes 必须为正")
	}
	if err := rules.SLA.validate(); err != nil {
		return err
	}
	return nil
}

// validate：承诺时钟一旦启用，时长与三种文案就不能为空——
// 否则系统会在没有承诺口径的情况下"主动"开口，那比不说话更糟。
func (sla *SLARule) validate() error {
	if !sla.Enabled {
		return nil
	}
	if sla.PromiseHours <= 0 {
		return errors.New("sla.promise_hours 必须为正")
	}
	if sla.DemoDueMinutes < 0 {
		return errors.New("sla.demo_due_in_minutes 不能为负")
	}
	if sla.SoonHours < 0 {
		return errors.New("sla.soon_hours 不能为负")
	}
	if strings.TrimSpace(sla.PromiseText) == "" || !strings.Contains(sla.PromiseText, "{due}") {
		return errors.New("sla.promise_text 不能为空，且必须包含 {due} 占位符")
	}
	if strings.TrimSpace(sla.DueText) == "" {
		return errors.New("sla.due_text 不能为空：到期必须说清《未到账》，不能暗示已到账")
	}
	if strings.TrimSpace(sla.SoonText) == "" {
		return errors.New("sla.soon_text 不能为空")
	}
	return nil
}

// classify 按规则文件的顺序分类：自上而下第一条命中即返回。
// 顺序写在规则文件里，代码不写死优先级。
func (rules *RuleSet) classify(text string) string {
	for _, intent := range rules.Intents {
		switch intent.Matcher {
		case "human_request":
			if rules.humanRequest(text) {
				return intent.ID
			}
		case "tracking":
			if rules.wantsTracking(text) {
				return intent.ID
			}
		default:
			if len(intent.Words) > 0 && containsAny(text, intent.Words...) {
				return intent.ID
			}
		}
	}
	return "unknown"
}

// intentRule 取某意图的规则行，供 assess 查 action / level / reason。
func (rules *RuleSet) intentRule(id string) (IntentRule, bool) {
	for _, intent := range rules.Intents {
		if intent.ID == id {
			return intent, true
		}
	}
	if id == "human" {
		return IntentRule{ID: "human", Level: "-", Action: "handoff", Reason: rules.SpecialIntents.HumanRequest.Reason}, true
	}
	return IntentRule{}, false
}

// humanRequest：先剔除否定句再匹配。抱怨里带这几个字不算要求转接。
func (rules *RuleSet) humanRequest(text string) bool {
	spec := rules.SpecialIntents.HumanRequest
	clean := text
	for _, negation := range spec.Negations {
		clean = strings.ReplaceAll(clean, negation, "")
	}
	return containsAny(clean, spec.Words...)
}

// withoutHumanRequest 把人工诉求词从文本里剥掉，用于看这句话里还有没有别的业务意图。
func (rules *RuleSet) withoutHumanRequest(text string) string {
	spec := rules.SpecialIntents.HumanRequest
	replacements := make([]string, 0, len(spec.Negations)+len(spec.Words)*2)
	for _, negation := range spec.Negations {
		replacements = append(replacements, negation, "")
	}
	for _, word := range spec.Words {
		replacements = append(replacements, word, "")
	}
	return strings.NewReplacer(replacements...).Replace(text)
}

func (rules *RuleSet) wantsTracking(text string) bool {
	if rules.readOnlyRequest(text) {
		return false
	}
	return containsAny(text, rules.SpecialIntents.Tracking.Words...)
}

func (rules *RuleSet) readOnlyRequest(text string) bool {
	return containsAny(text, rules.SpecialIntents.ReadOnly.Words...)
}

// safetyCheck 是安全闸（P1-4）。只加闸：命中即转人工，永远不会让系统放行。
// 顺序：危机先行，其次安全事件。
func (rules *RuleSet) safetyCheck(text string) (hit bool, reason string) {
	if !rules.Safety.Enabled {
		return false, ""
	}
	if containsAny(text, rules.Safety.CrisisWords...) {
		return true, rules.Safety.CrisisReason
	}
	if containsAny(text, rules.Safety.HazardWords...) {
		return true, rules.Safety.HazardReason
	}
	return false, ""
}

func (rules *RuleSet) evidenceTTL() time.Duration {
	return time.Duration(rules.EvidenceTTLMinutes) * time.Minute
}

// ── 接线层 ──────────────────────────────────────────────────
//
// activeRules 是当前生效的规则。启动时由 loadRules + applyRules 换成规则文件的内容；
// 在那之前（以及 go test 直接跑单个函数时）用内置兜底，保证引擎永远有依据可依，不会 nil。
// 引擎各处只认 activeRules，不认硬编码常量。

// 内置兜底与规则文件同形：只用于「规则还没加载」的空档（如单跑某个测试函数），
// 启动路径一定会被 fixtures/rules.json 覆盖，打印的版本号可以核对。
var activeRules = &RuleSet{
	Version: "builtin-fallback",
	Intents: []IntentRule{
		{ID: "dispute", Level: "L3", Action: "handoff", Words: []string{"投诉", "追究责任", "纠纷", "曝光", "律师"}, Reason: "L3 投诉争议，停止自动处理"},
		{ID: "compensation", Level: "L2", Action: "handoff", Words: []string{"补偿", "赔偿", "差价", "会员权益", "退差额"}, Reason: "L2 资金或权益诉求，需要人工复核"},
		{ID: "unauthorized", Level: "L2", Action: "handoff", Words: []string{"改成已退款", "忽略规则", "绕过规则", "跳过确认"}, Reason: "拒绝越权资金动作与无依据到账承诺"},
		{ID: "human", Level: "-", Action: "handoff", Matcher: "human_request", Reason: "用户明确要求人工"},
		{ID: "ticket_change", Level: "-", Action: "handoff", Words: []string{"车票退掉", "帮我退票", "办理退票", "我要退票", "改签", "退了这张票"}, Reason: "本期未开放该业务执行接口；不能以流程学习绕过"},
		{ID: "invoice", Level: "-", Action: "handoff", Words: []string{"发票", "开票"}, Reason: "本期未开放该业务执行接口；不能以流程学习绕过"},
		{ID: "qualification", Level: "-", Action: "handoff", Words: []string{"儿童票", "学生", "资质"}, Reason: "本期未开放该业务执行接口；不能以流程学习绕过"},
		{ID: "unauthorized", Level: "L2", Action: "handoff", Words: []string{"直接退款", "直接给我退款", "保证今天到账", "保证到账"}, Reason: "拒绝越权资金动作与无依据到账承诺"},
		{ID: "refund_progress", Level: "L1", Action: "process", Matcher: "tracking", Reason: "申请追踪退款进度"},
		{ID: "policy_query", Level: "L0", Action: "static_consultation", Words: []string{"规则", "政策"}, Reason: "读取版本化演示政策，不执行任务"},
		{ID: "refund_progress", Level: "L1", Action: "process", Words: []string{"退款", "退票", "到账", "钱还没", "钱咋还没", "卡里还没", "票退了", "票都退了"}, Reason: "退款进度查询"},
	},
	SpecialIntents: struct {
		HumanRequest SpecialIntent `json:"human_request"`
		Tracking     SpecialIntent `json:"tracking"`
		ReadOnly     SpecialIntent `json:"read_only"`
	}{
		HumanRequest: SpecialIntent{ID: "human", Words: []string{"转人工", "人工客服", "找真人", "人工处理"}, Negations: []string{"不要转人工", "别转人工", "不是要人工"}},
		Tracking:     SpecialIntent{ID: "refund_progress", Words: []string{"申请追踪", "提交追踪", "创建追踪", "帮我追", "帮我催", "催一下", "催退款", "继续跟进"}},
		ReadOnly:     SpecialIntent{Words: []string{"只查", "仅查", "只想知道", "只问进度", "不要追踪", "不申请追踪", "不提交", "不用办理", "取消追踪"}},
	},
	Eligibility:        EligibilityRule{Business: "rail", Channel: "sandbox", Ticket: "SUCCESS", Payment: "PROCESSING", Reason: "只适用于铁路退票成功、支付处理中、沙箱渠道"},
	Safety:             SafetyRule{Enabled: true, CrisisWords: []string{"不想活", "自杀", "伤害自己", "同归于尽", "轻生"}, HazardWords: []string{"起火", "冒烟", "燃烧", "爆炸", "漏电", "伤人", "受伤流血"}, CrisisReason: "人身安全表达，立即转人工，不由自动流程继续", HazardReason: "疑似安全事件描述，立即转人工，不由自动流程继续"},
	EvidenceTTLMinutes: 5,
}

// applyRules 换上规则文件的内容，并同步进程内的步骤与禁用条件。
func applyRules(rules *RuleSet) {
	activeRules = rules
	workflowSteps = rules.WorkflowSteps
	stopConditions = rules.StopConditions
}

// rulesVersion 供启动日志与状态接口显示当前规则版本。
func rulesVersion() string {
	if activeRules == nil {
		return "未加载"
	}
	return activeRules.Version
}
