package harness

import (
	"encoding/json"
	"testing"
)

func TestReplayPairsToolResultsAndSplitsMessages(t *testing.T) {
	var r Replay
	for _, line := range []string{
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"one"},{"type":"tool_use","id":"a","name":"mcp__rux__spawn_agent","input":{"name":"w1"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","is_error":true,"content":[{"type":"text","text":"boom"}]}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"two"},{"type":"tool_use","id":"b","name":"Bash","input":{"command":"ls"}}]}}`,
	} {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		r.Event(ev)
	}
	got := r.Entries
	if len(got) != 5 {
		t.Fatalf("entries %+v", got)
	}
	if got[0].Kind != "thinking" || got[1].Kind != "text" || got[1].Text != "one" {
		t.Errorf("first message %+v", got[:2])
	}
	if e := got[2]; e.Tool != "spawn_agent" || e.Label != "w1" || e.Result != "boom" || !e.IsError || e.Running {
		t.Errorf("tool %+v", e)
	}
	if got[3].Text != "two" || got[3].Kind != "text" {
		t.Errorf("second message %+v", got[3])
	}
	if e := got[4]; !e.Running || e.Tool != "Bash" {
		t.Errorf("unfinished tool %+v", e)
	}
	r.Settle()
	if r.Entries[4].Running {
		t.Error("Settle left a tool running")
	}
}

func TestStreamParserSkipsBlocksItAlreadyStreamed(t *testing.T) {
	var r Replay
	for _, line := range []string{
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hel"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
	} {
		var ev map[string]any
		_ = json.Unmarshal([]byte(line), &ev)
		r.Event(ev)
	}
	if len(r.Entries) != 1 || r.Entries[0].Text != "hello" {
		t.Errorf("entries %+v", r.Entries)
	}
}
