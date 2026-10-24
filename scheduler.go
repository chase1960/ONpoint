package main

import (
	"log"
	"time"
)

// 反馈时钟的扫描间隔。15 秒足够让路演演示（demo_due_in_minutes=1）在一分钟内看到到期触达，
// 同时对 72 小时的真实承诺没有精度影响。
const promiseScanInterval = 15 * time.Second

// startPromiseScan 在同进程起一个定时扫描。
// 承诺的意义是"到点了系统先开口"；如果只有用户发消息才检查，那就又退回成"等人来问"，
// 前端 20 秒轮询拿到的也只是旧状态。
func (app *App) startPromiseScan(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			app.scanPromises()
		}
	}()
}

// scanPromises 扫一遍所有带反馈时钟的任务。没到点的不动；到点的记事件并发消息。
// 去重交给 checkPromise 里的 seen()，所以重启后读回同一份状态也不会重复通知。
func (app *App) scanPromises() {
	if !activeRules.SLA.Enabled {
		return
	}
	app.mu.Lock()
	defer app.mu.Unlock()
	changed := false
	for _, task := range app.store.Tasks {
		if task.DueAt == "" {
			continue
		}
		record := app.store.Cases[task.CaseID]
		if record == nil {
			continue
		}
		before := len(record.Events)
		app.checkPromise(record)
		if len(record.Events) != before {
			record.Updated = stamp()
			changed = true
		}
	}
	if changed && app.db != nil {
		if err := app.db.save(app.store); err != nil {
			log.Printf("反馈时钟事件落盘失败：%v", err)
		}
	}
}
