package main

import (
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type App struct {
	mu      sync.Mutex
	store   Store
	dataset Dataset
	db      *Repository
	judge   *JEVClient
	llm     *LLMClient
}

func (app *App) api(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	if origin := request.Header.Get("Origin"); origin != "" && origin != "http://"+request.Host {
		writeError(writer, 403, "不接受其他网站发起的操作")
		return
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	if request.Method == http.MethodGet {
		app.store.LLM = app.llm.Status()
		app.store.JEV = app.judge.Status()
		json.NewEncoder(writer).Encode(app.store)
		return
	}
	if request.Method != http.MethodPost {
		writeError(writer, 405, "仅支持读取和提交")
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 32<<10)
	var input Request
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(writer, 400, "请求格式不正确")
		return
	}
	pending := app.prepareJudge(input)
	if pending != nil {
		app.mu.Unlock()
		pending.collect()
		app.mu.Lock()
		app.validatePreparedJudge(input, pending)
		input.prepared = pending
	}
	before := cloneStore(app.store)
	beforeJudge := app.judge
	if err := app.apply(input); err != nil {
		app.store = before
		app.judge = beforeJudge
		writeError(writer, 409, err.Error())
		return
	}
	app.store.JEV = app.judge.Status()
	if app.db != nil {
		if err := app.db.save(app.store); err != nil {
			app.store = before
			app.judge = beforeJudge
			log.Printf("state commit failed: %v", err)
			writeError(writer, 500, "数据库事务失败；本次操作未提交，请勿视为办理成功")
			return
		}
	}
	// Commit the authoritative action before releasing the lock for optional prose.
	// Never roll back concurrent business actions if the explanation fails.
	if explanation := app.prepareLLM(input); explanation != nil {
		reply := func() LLMReply {
			app.mu.Unlock()
			defer app.mu.Lock()
			return explanation.client.generate(request.Context(), explanation.context, explanation.messageAt)
		}()
		beforeReply := cloneStore(app.store)
		if app.attachLLM(explanation, reply) && app.db != nil {
			if err := app.db.save(app.store); err != nil {
				app.store = beforeReply
				log.Print("LLM explanation could not be saved; authoritative action is already committed")
			}
		}
	}
	app.store.LLM = app.llm.Status()
	json.NewEncoder(writer).Encode(app.store)
}

func writeError(writer http.ResponseWriter, code int, message string) {
	writer.WriteHeader(code)
	json.NewEncoder(writer).Encode(map[string]string{"error": message})
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8088", "Local sandbox address")
	data := flag.String("data", "data/ota-routing.db", "SQLite database")
	fixtures := flag.String("fixtures", "fixtures/ota.json", "Synthetic OTA dataset")
	rulesPath := flag.String("rules", "fixtures/rules.json", "External business rules (intents / eligibility / safety / steps)")
	checkJEV := flag.Bool("check-jev", false, "Check JEV models/authentication without inference or database writes")
	replayJEV := flag.Bool("replay-jev", false, "Replay the JEV question-set baseline against dataset samples; non-zero exit on regression")
	flag.Parse()
	if *checkJEV {
		result, err := newJEVFromEnv().CheckModels()
		if err != nil {
			log.Fatal(err)
		}
		json.NewEncoder(os.Stdout).Encode(result)
		return
	}
	if !strings.HasPrefix(*addr, "127.0.0.1:") {
		log.Fatal("Synthetic sandbox must listen on 127.0.0.1")
	}
	dataset, err := loadDataset(*fixtures)
	if err != nil {
		log.Fatal(err)
	}
	rules, err := loadRules(*rulesPath)
	if err != nil {
		log.Fatal(err)
	}
	applyRules(rules)
	if *replayJEV {
		report := newJEVFromEnv().ReplayBaseline(dataset.Samples)
		if path := os.Getenv("JEV_REPLAY_OUT"); path != "" {
			if err := writeReplayReport(path, report); err != nil {
				log.Fatal(err)
			}
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(report)
		if report.FailedCount > 0 {
			log.Fatalf("回放基线退化：%d/%d 条与期望不符（题面 %s）", report.FailedCount, report.Total, report.SetVersion)
		}
		if report.SkippedCount == report.Total {
			log.Fatal("回放基线未能执行：JEV 未就绪，未做任何判定")
		}
		return
	}
	repository, err := openRepository(*data)
	if err != nil {
		log.Fatal(err)
	}
	defer repository.close()
	state, err := repository.load(dataset)
	if err != nil {
		log.Fatal(err)
	}
	ledger, err := repository.loadJudgeLedger(200)
	if err != nil {
		log.Fatal(err)
	}
	state.JudgeLedger = ledger
	app := &App{store: state, dataset: dataset, db: repository}
	app.store.RulesVersion = rules.Version
	app.llm = newLLMFromEnv()
	app.store.LLM = app.llm.Status()
	app.judge = newJEVFromEnv()
	app.store.JEV = app.judge.Status()
	if err = repository.save(state); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", app.api)
	mux.Handle("/", http.FileServer(http.Dir("dist")))
	server := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("OTA 售后履约 Agent: http://%s · 合成数据 / 本地沙箱 / SQLite", *addr)
	log.Printf("业务规则 %s：意图 %d 条 · 安全闸 %t · 证据有效期 %d 分钟 · 步骤 %d 步 / 禁用条件 %d 条",
		rules.Version, len(rules.Intents), rules.Safety.Enabled, rules.EvidenceTTLMinutes, len(rules.WorkflowSteps), len(rules.StopConditions))
	log.Printf("判定账本 %s：累计 %d 条 · 成功 %d 条 · 时延中位 %dms / p95 %dms",
		questionSetVersion(), ledger.Total, ledger.OK, ledger.MedianMS, ledger.P95MS)
	if rules.SLA.Enabled {
		log.Printf("承诺时钟：承诺 %s · 临近提前 %d 小时提醒", promiseLabel(rules.promiseMinutes()), rules.SLA.SoonHours)
	} else {
		log.Printf("承诺时钟：已关闭（规则文件 sla.enabled=false）")
	}
	if err = server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
