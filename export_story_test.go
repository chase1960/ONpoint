package main

import (
 "encoding/json"
 "os"
 "path/filepath"
 "testing"
)

// TestExportStoryReplay exports real engine states for the offline presentation.
// No credentials, external models, database or external business tools are used.
func TestExportStoryReplay(t *testing.T) {
 app:=slaApp(t)
 // Recording scope is a tracking request, not an unsupported arrival promise.
 activeRules.SLA.Enabled=false
 frames:=map[string]any{}
 capture:=func(key string, r *Case, action string) {
  frames[key]=map[string]any{"caseID":r.ID,"requestAction":action,"store":cloneStore(app.store)}
 }
 human:=app.newCase("DEMO-RAIL-001")
 capture("human_start",human,"new")
 mustApply(t,app,Request{Action:"message",CaseID:human.ID,Text:"票退了，钱咋还没回来？帮我查查"})
 capture("human_query",human,"message")
 savedHuman:=cloneStore(app.store)
 mustApply(t,app,Request{Action:"consultation_done",CaseID:human.ID})
 capture("human_consultation",human,"consultation_done")
 app.store=savedHuman;human=app.store.Cases[human.ID]
 mustApply(t,app,Request{Action:"request_tracking",CaseID:human.ID})
 capture("human_handoff",human,"request_tracking")
 mustApply(t,app,Request{Action:"accept",CaseID:human.ID})
 capture("human_accept",human,"accept")
 mustApply(t,app,Request{Action:"human_prepare",CaseID:human.ID})
 capture("human_plan",human,"human_prepare")
 confirmPlan(t,app,human)
 capture("human_receipt",human,"confirm")
 mustApply(t,app,Request{Action:"resolved",CaseID:human.ID})
 capture("human_resolved",human,"resolved")
 mustApply(t,app,Request{Action:"draft",CaseID:human.ID})
 capture("evo_draft",human,"draft")
 ch:=app.store.Changes[0]
 for _,pair:=range [][2]string{{"change_replay","evo_replayed"},{"change_approve","evo_approved"},{"change_publish","evo_active"}} {
  mustApply(t,app,Request{Action:pair[0],ChangeID:ch.ID})
  capture(pair[1],human,pair[0])
 }
 if app.store.Active=="v1" {t.Fatal("publication did not change active version")}
 r:=app.newCase("DEMO-RAIL-001")
 capture("start",r,"new")
 mustApply(t,app,Request{Action:"message",CaseID:r.ID,Text:"票退了，钱咋还没回来？帮我查查"})
 capture("query",r,"message")
 savedQuery:=cloneStore(app.store)
 mustApply(t,app,Request{Action:"consultation_done",CaseID:r.ID})
 capture("consultation",r,"consultation_done")
 app.store=savedQuery;r=app.store.Cases[r.ID]
 mustApply(t,app,Request{Action:"request_tracking",CaseID:r.ID})
 if r.State!="plan" {t.Fatal("published procedure did not create a plan")}
 capture("plan",r,"request_tracking")
 confirmPlan(t,app,r)
 capture("receipt",r,"confirm")
 mustApply(t,app,Request{Action:"resolved",CaseID:r.ID})
 capture("resolved",r,"resolved")
 risk:=app.newCase("DEMO-RAIL-001")
 capture("risk_start",risk,"new")
 mustApply(t,app,Request{Action:"message",CaseID:risk.ID,Text:"我要投诉退款并追究责任"})
 if risk.State!="handoff" || risk.Receipt!="" {t.Fatal("risk example violated handoff invariant")}
 capture("risk",risk,"message")
 for _,sample:=range []struct{key,order,text string}{
  {"paid","DEMO-RAIL-003","我想核对一下退款进度"},
  {"policy","DEMO-RAIL-001","退票退款规则是什么，能保证什么时候到账吗？"},
  {"unknown","DEMO-RAIL-004","帮我看一下退票退款进度"},
  {"invoice","DEMO-RAIL-001","火车票怎么开发票？能直接帮我开吗"},
 } {
  record:=app.newCase(sample.order)
  capture(sample.key+"_start",record,"new")
  mustApply(t,app,Request{Action:"message",CaseID:record.ID,Text:sample.text})
  capture(sample.key,record,"message")
  if record.State=="answered" {mustApply(t,app,Request{Action:"consultation_done",CaseID:record.ID});capture(sample.key+"_done",record,"consultation_done")}
  if record.State=="handoff" {mustApply(t,app,Request{Action:"accept",CaseID:record.ID});capture(sample.key+"_accepted",record,"accept")}
 }
 mustApply(t,app,Request{Action:"accept",CaseID:risk.ID})
 capture("risk_accepted",risk,"accept")
 output:=map[string]any{"repository":"chase1960/ONpoint","commit":"4ff3d8a724b5ef237ac8fbd47a62a8cd7085b73d","generatedAt":stamp(),"mode":"recorded_synthetic_engine_states","sla_enabled":false,"frames":frames}
 data,err:=json.Marshal(output);if err!=nil {t.Fatal(err)}
 target:=os.Getenv("ONPOINT_STORY_EXPORT")
 if target=="" {target=filepath.Join("dist","story","replay-data.js")}
 if err=os.MkdirAll(filepath.Dir(target),0700);err!=nil {t.Fatal(err)}
 if err=os.WriteFile(target,append([]byte("window.ONPOINT_REPLAY = "),append(data,[]byte(";\n")...)...),0600);err!=nil {t.Fatal(err)}
 if dbPath:=os.Getenv("ONPOINT_STORY_DB");dbPath!="" {
  repository,dbErr:=openRepository(dbPath);if dbErr!=nil {t.Fatal(dbErr)}
  defer repository.close()
  if dbErr=repository.save(app.store);dbErr!=nil {t.Fatal(dbErr)}
  rulesData,_:=json.MarshalIndent(activeRules,"","  ")
  if dbErr=os.WriteFile(filepath.Join(filepath.Dir(dbPath),"story-rules.json"),rulesData,0600);dbErr!=nil {t.Fatal(dbErr)}
 }
 t.Logf("exported %d frames to %s",len(frames),target)
}
