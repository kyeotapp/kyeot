package fakeagent

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/kyeotapp/kyeot/internal/protocol"
)

func needsApproval(text string) bool {
	for _, w := range []string{"삭제", "결제", "보내"} {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// demoAnswer pauses as if working, then sends two tool calls and a markdown answer.
func demoAnswer(ctx context.Context, send func(protocol.AgentMessage), task, text string) {
	ok := true
	pause := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	send(protocol.AgentMessage{Type: "status", State: "working"})
	send(protocol.AgentMessage{Type: "tool", Task: task, Call: "d1", Phase: "started", Name: "web_search", Summary: text})
	if !pause(600 * time.Millisecond) {
		return
	}
	send(protocol.AgentMessage{Type: "tool", Task: task, Call: "d1", Phase: "completed", Name: "web_search", Summary: "결과 8건", OK: &ok})
	send(protocol.AgentMessage{Type: "tool", Task: task, Call: "d2", Phase: "started", Name: "memory", Summary: "관련 기억 찾기"})
	if !pause(400 * time.Millisecond) {
		return
	}
	send(protocol.AgentMessage{Type: "tool", Task: task, Call: "d2", Phase: "completed", Name: "memory", Summary: "2건", OK: &ok})
	send(protocol.AgentMessage{Type: "message", Task: task, Text: "알아봤어요. **세 가지**로 정리했어요.\n\n" +
		"1. 첫 번째 항목은 `내일 오전`에 처리하면 돼요\n2. 두 번째는 조금 더 확인이 필요해요\n3. 세 번째는 이미 끝났어요\n\n" +
		"> 참고: 데모 에이전트의 답이에요. 실제 에이전트를 붙이면 이 자리에 진짜 답이 와요."})
	send(protocol.AgentMessage{Type: "task.end", Task: task, Result: protocol.TaskDone})
	send(protocol.AgentMessage{Type: "status", State: "idle"})
}

// demoOpening is the messages that fill the routine screen, cost screen, and room feed so they aren't empty.
func demoOpening(now time.Time) []protocol.AgentMessage {
	ms := func(t time.Time) *int64 { v := t.UnixMilli(); return &v }
	f := func(v float64) *float64 { return &v }
	day := func(n int, h int) time.Time {
		d := now.AddDate(0, 0, n)
		return time.Date(d.Year(), d.Month(), d.Day(), h, 0, 0, 0, d.Location())
	}
	report := map[string]any{
		"generatedAt": now.UnixMilli(),
		"routines": []map[string]any{
			{"id": "contest", "name": "대회-컨퍼런스-알림", "schedule": "매주 월, 목 09:00", "state": "정상", "nextRun": ms(day(1, 9)),
				"lastRun": ms(day(-2, 9)), "lastStatus": "ok", "deliver": "디스코드", "about": "대회와 컨퍼런스 일정을 모아 알린다.", "noAgent": true, "runs": 4, "avgCost": f(0.0031), "monthCost": f(0.139), "monthRuns": 12,
				"on": true, "completed": 12, "script": "~/.hermes/scripts/event_digest.py",
				"info": map[string]any{"summary": "대회와 컨퍼런스 일정을 모아 알린다.", "body": "수집한 행사를 Haiku 로 골라 정해진 틀로 디스코드에 보낸다.",
					"sources": []string{"https://api.lu.ma/discover", "https://github.com/brave-people/Dev-Event"},
					"modules": []map[string]any{{"name": "event_sources.py", "summary": "행사 목록 수집 모듈"}}, "files": []string{"event-digest-state.json"}}},
			{"id": "jobs", "name": "채용-개발-알림", "schedule": "평일 18:00", "state": "정상", "nextRun": ms(day(0, 18)),
				"lastRun": ms(day(-1, 18)), "lastStatus": "ok", "deliver": "디스코드", "about": "새 개발 채용 공고를 골라 알린다.", "runs": 5, "avgCost": f(0.0124)},
			{"id": "daily", "name": "하루-요약", "schedule": "매일 23:50", "state": "오류", "nextRun": ms(day(0, 23)),
				"lastRun": ms(day(-1, 23)), "lastStatus": "error", "deliver": "봇 안에만 저장", "about": "하루 대화를 요약해 일지에 남긴다.", "runs": 6, "avgCost": f(0.0042), "error": "API 응답 시간 초과"},
			{"id": "cost", "name": "일주일-비용점검", "schedule": "11월 13일 10:00, 한 번", "state": "정상", "nextRun": ms(day(3, 10)), "once": true, "on": true, "deliver": "디스코드", "about": "한 주 비용을 점검한다.", "runs": 0},
		},
		"periods": []map[string]any{
			{"key": "week", "name": "최근 7일", "compareName": "그 전 7일", "cost": 0.412, "before": 0.355, "sessions": 63, "tokens": 1254000, "unpriced": 2,
				"bySource": []map[string]any{{"label": "디스코드", "cost": 0.231}, {"label": "루틴", "cost": 0.139}, {"label": "곁", "cost": 0.042}},
				"days":     []float64{0.051, 0.072, 0.033, 0.064, 0.081, 0.058, 0.053}},
			{"key": "month", "name": "이번 달", "compareName": "지난달 같은 기간", "cost": 0.684, "before": 0.512, "sessions": 98, "tokens": 2011000, "unpriced": 3,
				"bySource":  []map[string]any{{"label": "디스코드", "cost": 0.388}, {"label": "루틴", "cost": 0.231}, {"label": "곁", "cost": 0.065}},
				"byRoutine": []map[string]any{{"name": "대회-컨퍼런스-알림", "runs": 12, "cost": 0.139}, {"name": "하루-요약", "runs": 14, "cost": 0.069}, {"name": "AI-대회-알림", "runs": 6, "cost": 0.023, "gone": true}}},
			{"key": "last", "name": "지난달", "compareName": "그 전 달", "cost": 1.207, "before": 0.0, "sessions": 171, "tokens": 3720000, "unpriced": 0,
				"bySource": []map[string]any{{"label": "디스코드", "cost": 0.802}, {"label": "루틴", "cost": 0.405}}},
		},
		"forecast": map[string]any{"days": 30, "routines": 0.311, "other": 1.17, "unknown": []string{},
			"rows": []map[string]any{{"name": "채용-개발-알림", "runs": 22, "perRun": 0.0124, "total": 0.273}, {"name": "하루-요약", "runs": 30, "perRun": 0.0042, "total": 0.126}, {"name": "대회-컨퍼런스-알림", "runs": 8, "perRun": 0.0031, "total": 0.025}}},
	}
	var dailyRows []map[string]any
	for i := 119; i >= 0; i-- {
		d := now.AddDate(0, 0, -i)
		c := 0.02 + 0.06*float64((i*37)%11)/10
		dailyRows = append(dailyRows, map[string]any{"date": d.Format("2006-01-02"), "cost": c, "sessions": 5 + i%7, "tokens": 90000 + i*1000, "unpriced": 0,
			"bySource":  map[string]float64{"디스코드": c * 0.55, "루틴": c * 0.35, "곁": c * 0.10},
			"byRoutine": []map[string]any{{"name": "대회-컨퍼런스-알림", "runs": 1, "cost": c * 0.21}, {"name": "하루-요약", "runs": 1, "cost": c * 0.105}, {"name": "AI-대회-알림", "runs": 1, "cost": c * 0.035, "gone": true}}})
	}
	report["daily"] = dailyRows
	raw, _ := json.Marshal(report)
	out := []protocol.AgentMessage{{Type: "report", Report: raw}}
	for i, run := range []struct {
		routine, name, text string
		at                  time.Time
	}{
		{"contest", "대회-컨퍼런스-알림", "**대회 · 이번 주**\n\n1. 국부펀드 AI 에이전트 공모전 — 마감 11/20\n2. 데이터 분석 리더보드 — 마감 11/28\n\n**컨퍼런스**\n- 서울 개발자 밋업 · 11/22 (토)", day(-5, 9)},
		{"contest", "대회-컨퍼런스-알림", "**대회 · 이번 주**\n\n1. 국부펀드 AI 에이전트 공모전 — 마감 11/20\n\n새로 올라온 컨퍼런스는 없어요.", day(-2, 9)},
		{"jobs", "채용-개발-알림", "오늘 새 공고 **2건**이에요.\n\n- 서버 개발자 (C++) · 경력 1~3년\n- 백엔드 개발자 (Go) · 경력 무관", day(-1, 18)},
	} {
		out = append(out, protocol.AgentMessage{Type: "routine.run", Routine: run.routine, RoutineName: run.name,
			Run: "demo-" + string(rune('a'+i)), Text: run.text, At: run.at.UnixMilli()})
	}
	return out
}
