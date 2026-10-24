package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

type Repository struct{ database *sql.DB }

func openRepository(path string) (*Repository, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	_, err = database.Exec(`PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS metadata (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS cases (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS proposals (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, case_id TEXT NOT NULL, order_id TEXT NOT NULL, idempotency_key TEXT NOT NULL UNIQUE, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS changes (id TEXT PRIMARY KEY, position INTEGER NOT NULL, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS snapshots (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS judgments (id INTEGER PRIMARY KEY AUTOINCREMENT, case_id TEXT NOT NULL, question_version TEXT NOT NULL, model TEXT, mode TEXT, status TEXT, sample_id TEXT, local_intent TEXT, expected_intent TEXT, latency_ms INTEGER NOT NULL, at TEXT NOT NULL, payload TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS judgments_at ON judgments(at);`)
	if err != nil {
		database.Close()
		return nil, err
	}
	return &Repository{database: database}, nil
}
func (repository *Repository) close() { repository.database.Close() }

// ── 判定账本（P0-3）────────────────────────────────────────
//
// judgments 表只追加，不随 save() 清空——判定记录是审计资产，不是会话状态。
// 有了独立的行，才能回答「JEV 判了多少次、采用率多少、哪一代题面判的」，
// 而不是只能把整个案件翻出来看。

func (repository *Repository) saveJudgment(caseID string, observation JEVObservation) error {
	if repository == nil || repository.database == nil {
		return nil
	}
	payload, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	_, err = repository.database.Exec(
		`INSERT INTO judgments (case_id, question_version, model, mode, status, sample_id, local_intent, expected_intent, latency_ms, at, payload) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		caseID, observation.QuestionVersion, observation.Model, observation.Mode, observation.Status,
		observation.SampleID, observation.LocalIntent, observation.ExpectedIntent, observation.LatencyMS, observation.At, string(payload))
	return err
}

// loadJudgeLedger 汇总账本：总量、成功率、时延分位，外加最近若干条明细。
func (repository *Repository) loadJudgeLedger(limit int) (JudgeLedger, error) {
	ledger := JudgeLedger{Limit: limit}
	if repository == nil || repository.database == nil {
		return ledger, nil
	}
	if limit <= 0 {
		limit = 200
	}
	var total, okCount int
	if err := repository.database.QueryRow(`SELECT COUNT(*), COALESCE(SUM(status='ok'),0) FROM judgments`).Scan(&total, &okCount); err != nil {
		return ledger, err
	}
	ledger.Total, ledger.OK = total, okCount
	rows, err := repository.database.Query(`SELECT payload FROM judgments ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return ledger, err
	}
	defer rows.Close()
	latencies := []int64{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return ledger, err
		}
		var observation JEVObservation
		if err := json.Unmarshal([]byte(payload), &observation); err != nil {
			continue
		}
		ledger.Recent = append(ledger.Recent, observation)
		if observation.Status == "ok" {
			latencies = append(latencies, observation.LatencyMS)
		}
	}
	if err := rows.Err(); err != nil {
		return ledger, err
	}
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		ledger.MedianMS = latencies[len(latencies)/2]
		ledger.P95MS = latencies[min(len(latencies)-1, int(float64(len(latencies))*0.95))]
	}
	return ledger, nil
}

func (repository *Repository) save(state Store) error {
	transaction, err := repository.database.Begin()
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, table := range []string{"metadata", "cases", "proposals", "tasks", "changes", "snapshots"} {
		if _, err = transaction.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}
	metadata := map[string]string{"schema": state.Schema, "active": state.Active, "dataset_hash": digest(state.Dataset)}
	judgeReport, _ := json.Marshal(state.JudgeReport)
	metadata["judge_report"] = string(judgeReport)
	for key, value := range metadata {
		if _, err = transaction.Exec("INSERT INTO metadata VALUES (?,?)", key, value); err != nil {
			return err
		}
	}
	for key, record := range state.Cases {
		if err = insertPayload(transaction, "cases", key, record); err != nil {
			return err
		}
	}
	for key, record := range state.Proposals {
		if err = insertPayload(transaction, "proposals", key, record); err != nil {
			return err
		}
	}
	for key, record := range state.Tasks {
		payload, _ := json.Marshal(record)
		if _, err = transaction.Exec("INSERT INTO tasks VALUES (?,?,?,?,?)", key, record.CaseID, record.OrderID, record.Key, string(payload)); err != nil {
			return err
		}
	}
	for position, record := range state.Changes {
		payload, _ := json.Marshal(record)
		if _, err = transaction.Exec("INSERT INTO changes VALUES (?,?,?)", record.ID, position, string(payload)); err != nil {
			return err
		}
	}
	for key, record := range state.Snapshots {
		if err = insertPayload(transaction, "snapshots", key, record); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func insertPayload(transaction *sql.Tx, table, key string, record any) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = transaction.Exec("INSERT INTO "+table+" VALUES (?,?)", key, string(payload))
	return err
}

func (repository *Repository) load(dataset Dataset) (Store, error) {
	state := freshStore(dataset)
	var schema string
	err := repository.database.QueryRow("SELECT payload FROM metadata WHERE id='schema'").Scan(&schema)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if schema != "ota-1" {
		return state, errors.New("数据库不是当前OTA版本，不能自动混用历史演示数据")
	}
	var storedHash string
	if err = repository.database.QueryRow("SELECT payload FROM metadata WHERE id='dataset_hash'").Scan(&storedHash); err != nil {
		return state, err
	}
	if storedHash != digest(dataset) {
		return state, errors.New("合成数据或政策内容变化：请使用新的-data数据库，旧版只保留审计，不隐式迁移")
	}
	if err = repository.database.QueryRow("SELECT payload FROM metadata WHERE id='active'").Scan(&state.Active); err != nil {
		return state, err
	}
	state.Snapshots = map[string]Snapshot{}
	var report string
	if err = repository.database.QueryRow("SELECT payload FROM metadata WHERE id='judge_report'").Scan(&report); err == nil {
		if err = json.Unmarshal([]byte(report), &state.JudgeReport); err != nil {
			return state, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	for _, table := range []string{"cases", "proposals", "tasks", "changes", "snapshots"} {
		query := "SELECT payload FROM " + table
		if table == "changes" {
			query += " ORDER BY position"
		}
		rows, queryErr := repository.database.Query(query)
		if queryErr != nil {
			return state, queryErr
		}
		for rows.Next() {
			var payload string
			if err = rows.Scan(&payload); err != nil {
				rows.Close()
				return state, err
			}
			switch table {
			case "proposals":
				var record Proposal
				err = json.Unmarshal([]byte(payload), &record)
				state.Proposals[record.ID] = &record
			case "cases":
				var record Case
				err = json.Unmarshal([]byte(payload), &record)
				state.Cases[record.ID] = &record
			case "tasks":
				var record Task
				err = json.Unmarshal([]byte(payload), &record)
				state.Tasks[record.ID] = &record
			case "changes":
				var record Change
				err = json.Unmarshal([]byte(payload), &record)
				state.Changes = append(state.Changes, &record)
			case "snapshots":
				var record Snapshot
				err = json.Unmarshal([]byte(payload), &record)
				state.Snapshots[record.ID] = record
			}
			if err != nil {
				rows.Close()
				return state, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return state, err
		}
	}
	if _, exists := state.Snapshots[state.Active]; !exists {
		return state, fmt.Errorf("缺少激活快照 %s", state.Active)
	}
	for _, record := range state.Cases {
		if !strings.HasPrefix(record.ID, "CASE-") {
			return state, errors.New("发现非OTA历史案件，拒绝混用")
		}
		if _, exists := state.Snapshots[record.Snapshot]; !exists {
			return state, errors.New("案件引用了不存在的流程快照")
		}
	}
	return state, nil
}
