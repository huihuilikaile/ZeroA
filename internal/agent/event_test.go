package agent

import (
	"encoding/json"
	"testing"
	"time"
)

// TestClassify 覆盖 Classify 的五种结局，每一种都对应界面上一条不同的处理路径：
//
//	response → 回填某条命令的结果
//	event    → 按 type 分发（message_update / agent_settled …）
//	stderr   → 诊断日志，不参与协议
//	system   → 客户端自己造的记录（进程启停）
//	非法 JSON → 标成 invalid_json，原始行仍然保留给观察器
//
// 断言里刻意锁死了 Kind/Type/Record/sessionId 四个字段：
// 前端的监听器就靠它们分流，任何一个变了都会让界面静默失灵。
func TestClassify(t *testing.T) {
	at := time.Date(2026, 10, 3, 22, 0, 0, 123_000_000, time.Local)
	cases := []struct {
		name      string
		direction string
		line      string
		wantKind  string
		wantType  string
		wantRaw   string
		wantRec   string // 期望的 Record；空串表示“不该有解析结果”
		wantSess  string
	}{
		{
			name:      "response",
			direction: DirectionStdout,
			line:      `{"id":"req-1","type":"response","command":"get_state","sessionId":"s-1","success":true,"data":{"isStreaming":false,"sessionId":"s-1"}}`,
			wantKind:  KindResponse,
			wantType:  "get_state", // response 的 Type 用 command 更贴近界面显示
			wantRaw:   `{"id":"req-1","type":"response","command":"get_state","sessionId":"s-1","success":true,"data":{"isStreaming":false,"sessionId":"s-1"}}`,
			wantRec:   `{"id":"req-1","type":"response","command":"get_state","sessionId":"s-1","success":true,"data":{"isStreaming":false,"sessionId":"s-1"}}`,
			wantSess:  "s-1",
		},
		{
			name:      "event",
			direction: DirectionStdout,
			line:      `{"type":"message_update","sessionId":"s-1","assistantMessageEvent":{"type":"text_delta","delta":"你"}}`,
			wantKind:  KindEvent,
			wantType:  "message_update",
			wantRaw:   `{"type":"message_update","sessionId":"s-1","assistantMessageEvent":{"type":"text_delta","delta":"你"}}`,
			wantRec:   `{"type":"message_update","sessionId":"s-1","assistantMessageEvent":{"type":"text_delta","delta":"你"}}`,
			wantSess:  "s-1",
		},
		{
			name:      "stderr",
			direction: DirectionStderr,
			line:      "Debugger listening on ws://127.0.0.1:9229",
			wantKind:  KindStderr,
			wantRaw:   "Debugger listening on ws://127.0.0.1:9229",
		},
		{
			// system 方向的记录不是从管道读来的，而是客户端自己生成的
			// （internal/pi 的 process_started / process_exit / invalid_json…）。
			name:      "system",
			direction: DirectionSystem,
			line:      "process_started",
			wantKind:  KindSystem,
			wantType:  "process_started",
			wantRaw:   "process_started",
		},
		{
			// 进程**异常**退出时 raw 带着错误细节（"process_exit: exit status 1"）。
			// Type 必须归一成纯类型：前端路由、退出日志、回合状态机都按
			// "process_exit" 精确匹配，带细节就等于崩溃退出被全员无视
			//（界面卡在"运行中"，abort/steer 全报"Pi 在响应前退出"）。
			name:      "system with detail",
			direction: DirectionSystem,
			line:      "process_exit: exit status 1",
			wantKind:  KindSystem,
			wantType:  "process_exit",
			wantRaw:   "process_exit: exit status 1",
		},
		{
			name:      "invalid json",
			direction: DirectionStdout,
			line:      `{"type":"message_update", broken`,
			wantKind:  KindSystem,
			wantType:  TypeInvalidJSON,
			wantRaw:   `{"type":"message_update", broken`,
		},
		{
			// 合法 JSON、但没有 type：仍然算事件（Type 为空串）。
			// 这条防的是“分类器顺手把不认识的记录丢掉”，
			// 观察器必须看到它，前端才发现协议变了。
			name:      "json without type",
			direction: DirectionStdout,
			line:      `{"hello":"world"}`,
			wantKind:  KindEvent,
			wantRaw:   `{"hello":"world"}`,
			wantRec:   `{"hello":"world"}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			envelope := Classify(testCase.direction, testCase.line, at)
			if envelope.Kind != testCase.wantKind {
				t.Errorf("Kind = %q, want %q", envelope.Kind, testCase.wantKind)
			}
			if envelope.Type != testCase.wantType {
				t.Errorf("Type = %q, want %q", envelope.Type, testCase.wantType)
			}
			if envelope.Raw != testCase.wantRaw {
				t.Errorf("Raw = %q, want %q", envelope.Raw, testCase.wantRaw)
			}
			if envelope.Record != testCase.wantRec {
				t.Errorf("Record = %q, want %q", envelope.Record, testCase.wantRec)
			}
			if envelope.SessionID != testCase.wantSess {
				t.Errorf("SessionID = %q, want %q", envelope.SessionID, testCase.wantSess)
			}
			if envelope.AgentID != "default" {
				t.Errorf("AgentID = %q, want default", envelope.AgentID)
			}
			if envelope.Time != "22:00:00.123" {
				t.Errorf("Time = %q, want 22:00:00.123", envelope.Time)
			}
		})
	}
}

// TestClassifyRecordIsParseable 守住 Envelope 里那条对前端最重要的隐含约定：
// Record 非空 ⇒ 一定能 JSON.parse。
// 前端因此可以写 `if (e.record) JSON.parse(e.record)` 而不需要 try/catch。
func TestClassifyRecordIsParseable(t *testing.T) {
	for _, line := range []string{
		`{"type":"agent_settled"}`,
		`{"type":"response","id":"req-9","command":"abort","success":true}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","delta":"嗯"}}`,
	} {
		envelope := Classify(DirectionStdout, line, time.Now())
		if envelope.Record == "" {
			t.Fatalf("Record 为空: %s", line)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(envelope.Record), &decoded); err != nil {
			t.Fatalf("Record 无法解析: %v (%s)", err, envelope.Record)
		}
	}
	// 反向约定：坏行的 Record 必须为空，否则前端会在解析时抛异常。
	for _, line := range []string{"", "{", "not json"} {
		if envelope := Classify(DirectionStdout, line, time.Now()); envelope.Record != "" {
			t.Errorf("坏行的 Record 应为空: %q → %q", line, envelope.Record)
		}
	}
}
