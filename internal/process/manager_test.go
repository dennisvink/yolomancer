package process

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestExecAndBackgroundPolling(t *testing.T) {
	m := New()
	raw, err := m.Exec(context.Background(), ExecOptions{Command: "printf hello", Shell: "/bin/sh", Yield: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if json.Unmarshal([]byte(raw), &v) != nil || v["output"] != "hello" {
		t.Fatal(raw)
	}
	if v["ok"] != true || v["session_id"] != nil || v["command"] != "printf hello" || v["chunk_id"] == "" {
		t.Fatalf("Required result fields missing: %s", raw)
	}
	raw, err = m.Exec(context.Background(), ExecOptions{Command: "sleep 1; printf done", Shell: "/bin/sh", Yield: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(raw), &v)
	if v["running"] != true {
		t.Fatal(raw)
	}
	id := int32(v["session_id"].(float64))
	raw, err = m.Write(context.Background(), id, "", 2*time.Second, 1000)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(raw), &v)
	if v["output"] != "done" || v["running"] != false {
		t.Fatal(raw)
	}
}

func TestPipeSessionRejectsStdin(t *testing.T) {
	m := New()
	raw, err := m.Exec(context.Background(), ExecOptions{Command: "sleep 1", Shell: "/bin/sh", Yield: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	_ = json.Unmarshal([]byte(raw), &value)
	id := int32(value["session_id"].(float64))
	if _, err := m.Write(context.Background(), id, "x", time.Millisecond, 10); err == nil {
		t.Fatal("pipe stdin was accepted")
	}
	m.StopAll()
}
