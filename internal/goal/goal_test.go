package goal

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestGoalLifecycle(t *testing.T) {
	var saved *State
	m := New(nil, func(s *State) error { saved = clone(s); return nil })
	if err := m.Create("Finish every requirement", 100, false); err != nil {
		t.Fatal(err)
	}
	id := m.Get().ID
	if err := m.Create("silently replace", 0, false); err == nil {
		t.Fatal("replaced unfinished objective")
	}
	if err := m.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := m.Account(70); err != nil {
		t.Fatal(err)
	}
	if err := m.End(""); err != nil {
		t.Fatal(err)
	}
	if saved.Status != Active || saved.TokensUsed != 70 || saved.Turns != 1 {
		t.Fatalf("%+v", saved)
	}
	if err := m.SetStatus(Paused); err != nil {
		t.Fatal(err)
	}
	if err := m.ModelStatus(Complete); err == nil {
		t.Fatal("model overwrote pause")
	}
	if err := m.SetStatus(Active); err != nil {
		t.Fatal(err)
	}
	if err := m.Begin(); err != nil {
		t.Fatal(err)
	}
	if err := m.Account(35); err != nil {
		t.Fatal(err)
	}
	if m.Get().Status != BudgetLimited || !strings.Contains(m.Context(), "budget is exhausted") {
		t.Fatal(m.Get())
	}
	if err := m.End(""); err != nil {
		t.Fatal(err)
	}
	if err := m.SetStatus(Active); err == nil {
		t.Fatal("resumed exhausted budget")
	}
	b := uint64(200)
	if err := m.Edit("Original objective plus tests", &b); err != nil {
		t.Fatal(err)
	}
	if m.Get().ID != id || m.Get().TokensUsed != 105 {
		t.Fatal("edit reset accounting")
	}
	if err := m.SetStatus(Active); err != nil {
		t.Fatal(err)
	}
	if err := m.ModelStatus(Complete); err != nil {
		t.Fatal(err)
	}
	if err := m.Create("Next objective", 0, false); err != nil {
		t.Fatal(err)
	}
	if m.Get().ID == id || m.Get().TokensUsed != 0 {
		t.Fatal("new goal retained counters")
	}
	if err := m.Clear(); err != nil || saved != nil || m.Get() != nil {
		t.Fatal("clear did not persist null")
	}
}

func TestConcurrentAccountingAndSnapshots(t *testing.T) {
	m := New(nil, nil)
	_ = m.Create("count", 0, false)
	_ = m.Begin()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.Account(1); snapshot := m.Get(); snapshot.Objective = "not shared" }()
	}
	wg.Wait()
	_ = m.End("")
	if g := m.Get(); g.TokensUsed != 100 || g.Objective != "count" {
		t.Fatal(g)
	}
}

func TestPersistenceFailureStopsContinuation(t *testing.T) {
	fail := false
	m := New(nil, func(*State) error {
		if fail {
			return errors.New("disk full")
		}
		return nil
	})
	_ = m.Create("work", 0, false)
	_ = m.Begin()
	fail = true
	if err := m.Account(5); err == nil {
		t.Fatal("lost persistence error")
	}
	if m.Get().Status != Paused {
		t.Fatal("would continue after failed write")
	}
	if err := m.End(""); err == nil {
		t.Fatal("end masked persistence failure")
	}
}

func TestMidTurnCreateAndStop(t *testing.T) {
	for _, status := range []Status{Paused, Blocked, UsageLimited} {
		m := New(nil, nil)
		_ = m.Begin()
		_ = m.Create("work", 0, false)
		_ = m.Account(4)
		_ = m.End(status)
		g := m.Get()
		if g.Status != status || g.Turns != 1 || g.TokensUsed != 4 {
			t.Fatal(g)
		}
		copy := New(g, nil)
		if !reflect.DeepEqual(copy.Get(), g) {
			t.Fatal("restoring changed state")
		}
	}
}

func TestExecutionFailureCircuitBreakerAndResume(t *testing.T) {
	m := New(nil, nil)
	_ = m.Create("work", 0, false)
	for i := 0; i < 3; i++ {
		_ = m.Begin()
		m.RecordToolOutcome(false, true)
		_ = m.End("")
	}
	if m.Get().Status != Blocked {
		t.Fatal("failed execution turns continued indefinitely")
	}
	_ = m.SetStatus(Active)
	_ = m.Begin()
	m.RecordToolOutcome(false, true)
	_ = m.End("")
	if m.Get().Status != Active {
		t.Fatal("resume did not reset failure audit")
	}
	_ = m.Begin()
	m.RecordToolOutcome(false, true)
	m.RecordToolOutcome(true, false)
	_ = m.End("")
	_ = m.Begin()
	m.RecordToolOutcome(false, true)
	_ = m.End("")
	if m.Get().Status != Active {
		t.Fatal("successful tool did not reset failure audit")
	}
}
