package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"time"
)

type Message struct{ Role, Text, At string }
type Event struct{ Kind, Text, At string }
type Evidence struct {
	ID, Tool, Input, Output, At string
	Success                     bool
}
type Order struct{ ID, Business, Customer, Route, Ticket, Payment, Channel, Amount string }
type Category struct{ ID, Scenario, Level, Action, Boundary string }
type Sample struct{ ID, Group, Text, OrderID, Intent, BaseExpected, CandidateExpected string }
type Policy struct {
	ID, Version, Success    string
	AllowedSteps, Forbidden []string
}
type Dataset struct {
	Version, Source string
	Policy          Policy
	Categories      []Category
	Orders          []Order
	Samples         []Sample
}
type Procedure struct {
	ID, Title, Source, Intent, Business, Ticket, Payment, Channel, Success string
	Steps, StopConditions                                                  []string
	Support                                                                int
}
type Plan struct {
	ID, ArtifactID, Title, Description, Clause, ProcedureID, Token string
	Steps, Benefits, Notes                                         []string
}
type Case struct {
	LLMReplies                                                                              []LLMReply
	Intake                                                                                  *IntakeDecision
	Route                                                                                   string
	TrackingRequested, HumanRequested                                                       bool
	Owner                                                                                   string
	ID, State, Snapshot, OrderID, Intent, Risk, Receipt, Selected, Updated, Decision, Fault string
	HumanHandled, HumanPrepared                                                             bool
	Facts                                                                                   map[string]string
	Messages                                                                                []Message
	Events                                                                                  []Event
	Evidence                                                                                []Evidence
	Plans                                                                                   []Plan
	ActualSteps                                                                             []string
	QueryOrder                                                                              *Order
	QueryAt, Consent, PlanActor                                                             string
	ProposalID                                                                              string
	Judgments                                                                               []JEVObservation
}
type Proposal struct {
	ID, CaseID, OrderID, PolicyVersion, Snapshot, ProcedureID, Status, CreatedAt, ConfirmedAt, Receipt string
	Plan                                                                                               Plan
}
type Task struct{ ID, CaseID, OrderID, Key, State, At, Snapshot, ProcedureID, Consent, Actor, ProposalID string }
type ReplayRow struct {
	Name, Before, After, ExpectedBefore, ExpectedAfter string
	Passed                                             bool
	BeforeSteps, AfterSteps                            []string
}
type Change struct {
	ID, Source, Title, Note, Status, Hash, ReplayHash, Base, Snapshot, Binding, ReportHash string
	Procedure                                                                              Procedure
	Replay                                                                                 []ReplayRow
	Events                                                                                 []Event
	SourcePlan, SourceTask                                                                 string
}
type Snapshot struct {
	ID, Parent, Change, PolicyVersion, PolicyHash, DatasetHash string
	Procedure                                                  *Procedure
}

// JudgeLedger 是判定账本的视图：判定发生过的证据，独立于会话状态。
type JudgeLedger struct {
	Total, OK, Limit int
	MedianMS, P95MS  int64
	Recent           []JEVObservation
}

type Store struct {
	LLM            LLMStatus
	Schema, Active string
	RulesVersion   string
	JudgeLedger    JudgeLedger
	Dataset        Dataset
	Cases          map[string]*Case
	Tasks          map[string]*Task
	Changes        []*Change
	Snapshots      map[string]Snapshot
	Proposals      map[string]*Proposal
	JEV            JEVStatus
	JudgeReport    []JEVObservation
}
type Request struct {
	prepared                                                   *preparedJudge
	Action, CaseID, OrderID, Text, PlanID, PlanToken, ChangeID string
	Confirmed                                                  bool
	Procedure                                                  *Procedure
}

func freshStore(dataset Dataset) Store {
	return Store{Schema: "ota-1", Dataset: dataset, Active: "v1", Cases: map[string]*Case{}, Tasks: map[string]*Task{}, Proposals: map[string]*Proposal{}, JudgeReport: []JEVObservation{}, Changes: []*Change{}, Snapshots: map[string]Snapshot{"v1": {ID: "v1", PolicyVersion: dataset.Policy.Version, PolicyHash: digest(dataset.Policy), DatasetHash: digest(dataset)}}}
}
func cloneStore(state Store) Store {
	content, _ := json.Marshal(state)
	var result Store
	if err := json.Unmarshal(content, &result); err != nil {
		panic(err)
	}
	return result
}
func digest(value any) string {
	content, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:])
}
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func uniqueID(prefix string) string {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		panic(err)
	}
	return prefix + strings.ToUpper(hex.EncodeToString(buffer))
}
func containsAny(text string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(strings.ToLower(text), strings.ToLower(word)) {
			return true
		}
	}
	return false
}
func (record *Case) event(kind, text string) {
	record.Events = append(record.Events, Event{kind, text, stamp()})
	record.Updated = stamp()
}
func (record *Case) say(role, text string) {
	record.Messages = append(record.Messages, Message{role, text, stamp()})
	record.Updated = stamp()
}
func (record *Case) step(name string) { record.ActualSteps = append(record.ActualSteps, name) }
func (dataset Dataset) order(id string) (Order, bool) {
	for _, order := range dataset.Orders {
		if order.ID == id {
			return order, true
		}
	}
	return Order{}, false
}
func loadDataset(path string) (Dataset, error) {
	var dataset Dataset
	content, err := os.ReadFile(path)
	if err != nil {
		return dataset, err
	}
	if err = json.Unmarshal(content, &dataset); err != nil {
		return dataset, err
	}
	if dataset.Version == "" || dataset.Policy.Version == "" || len(dataset.Orders) == 0 || len(dataset.Samples) == 0 {
		return dataset, errors.New("演示数据缺少版本、政策、订单或用例")
	}
	ids := map[string]bool{}
	for _, order := range dataset.Orders {
		if !strings.HasPrefix(order.ID, "DEMO-") || order.Customer != "DEMO-USER" || ids[order.ID] {
			return dataset, errors.New("只允许唯一且属于 DEMO-USER 的合成订单")
		}
		ids[order.ID] = true
	}
	for _, sample := range dataset.Samples {
		if sample.ID == "" || sample.Text == "" || !slices.Contains([]string{"plan", "handoff", "clarify", "answered"}, sample.CandidateExpected) {
			return dataset, errors.New("合成用例缺少人工预设的期望路由")
		}
	}
	return dataset, nil
}
