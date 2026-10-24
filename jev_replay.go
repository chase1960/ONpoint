package main

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"time"
)

// 回放基线（P0-2）。题面是判定依据，改一个词就可能改掉一批结论——
// 所以每次改题面都要拿同一批样本重跑一遍，退化要能被当场发现，而不是等演示翻车。
//
// 基线样本直接取合成数据集的 Samples（人工标注 Intent），不另建一份语料，
// 免得两份语料各自漂移。跑法：go run . -replay-jev

type JEVReplayRow struct {
	SampleID, Group, Text string
	Expected, Got         string
	Passed                bool
	LatencyMS             int64
	Skipped               bool
	Note                  string
}

type JEVReplayReport struct {
	RanAt            string            `json:"ran_at"`
	QuestionVersions map[string]string `json:"question_versions"`
	SetVersion       string            `json:"set_version"`
	Total, Passed    int               `json:"-"`
	PassedCount      int               `json:"passed"`
	FailedCount      int               `json:"failed"`
	SkippedCount     int               `json:"skipped"`
	MedianMS         int64             `json:"median_ms"`
	P95MS            int64             `json:"p95_ms"`
	Rows             []JEVReplayRow    `json:"rows"`
}

// expectedAnswer 把人工标注的意图翻成这道题该答什么。
// 只校验能确定的那一维：标了 human 就看 human_requested，标了 refund_progress 就看 refund_progress，
// 其余意图一律要求 refund_progress 答 no（不能把投诉或开票当成退款进度）。
func expectedAnswer(intent string) (question, choice string, ok bool) {
	switch intent {
	case "human":
		return "human_requested", "yes", true
	case "refund_progress":
		return "refund_progress", "yes", true
	case "dispute", "compensation", "unauthorized", "ticket_change", "invoice", "qualification", "policy_query":
		return "refund_progress", "no", true
	}
	return "", "", false
}

// ReplayBaseline 对每条例句跑一次判定，比对期望选项。
// JEV 不可用时整轮跳过并说明理由——影子/未配置状态下不假装通过。
func (client *JEVClient) ReplayBaseline(samples []Sample) JEVReplayReport {
	report := JEVReplayReport{
		RanAt:            time.Now().UTC().Format(time.RFC3339),
		QuestionVersions: jevVersionMap(),
		SetVersion:       questionSetVersion(),
	}
	status := client.Status()
	if !status.Ready {
		for _, sample := range samples {
			report.Rows = append(report.Rows, JEVReplayRow{SampleID: sample.ID, Group: sample.Group, Text: sample.Text, Skipped: true, Note: "JEV 未就绪：" + status.Reason})
			report.SkippedCount++
		}
		report.Total = len(samples)
		return report
	}
	latencies := []int64{}
	for _, sample := range samples {
		row := JEVReplayRow{SampleID: sample.ID, Group: sample.Group, Text: sample.Text}
		question, choice, ok := expectedAnswer(sample.Intent)
		if !ok {
			row.Skipped, row.Note = true, "该意图没有确定期望，跳过"
			report.SkippedCount++
			report.Rows = append(report.Rows, row)
			continue
		}
		observation := client.Judge(sample.Text)
		row.LatencyMS = observation.LatencyMS
		row.Expected = question + "=" + choice
		report.Total++
		switch {
		case observation.Status != "ok":
			row.Got, row.Note = observation.Status, "判定未成功："+observation.Error
		default:
			answer := observation.Answers[question]
			row.Got = question + "=" + answer.Choice
			row.Passed = answer.Choice == choice
			row.Note = "置信 " + formatFloat(answer.Confidence)
			latencies = append(latencies, observation.LatencyMS)
		}
		if row.Passed {
			report.PassedCount++
		} else if !row.Skipped {
			report.FailedCount++
		}
		report.Rows = append(report.Rows, row)
	}
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		report.MedianMS = latencies[len(latencies)/2]
		report.P95MS = latencies[min(len(latencies)-1, int(float64(len(latencies))*0.95))]
	}
	return report
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

// writeReplayReport 把报告写成 JSON，供归档与对比。
func writeReplayReport(path string, report JEVReplayReport) error {
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(content, '\n'), 0600)
}
