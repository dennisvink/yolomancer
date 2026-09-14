package session

import (
	"testing"

	"github.com/dennisvink/yolomancer/internal/goal"
	"github.com/dennisvink/yolomancer/internal/model"
)

func TestGoalSidecarOverridesStaleSnapshotIncludingClear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &model.SessionSnapshot{SessionID: "goal-test", Goal: &goal.State{ID: "old", Status: goal.Active}}
	if err := Write(s); err != nil {
		t.Fatal(err)
	}
	fresh := &goal.State{ID: "new", Objective: "Finish it", Status: goal.Paused, TokensUsed: 123}
	if err := WriteGoal(s.SessionID, fresh); err != nil {
		t.Fatal(err)
	}
	if err := Write(s); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(s.SessionID)
	if err != nil || loaded.Goal.ID != "new" || loaded.Goal.TokensUsed != 123 {
		t.Fatalf("%+v %v", loaded, err)
	}
	if err := WriteGoal(s.SessionID, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(s.SessionID)
	if err != nil || loaded.Goal != nil {
		t.Fatalf("clear resurrected goal: %+v %v", loaded, err)
	}
	list, err := List(true, "")
	if err != nil || len(list) != 1 {
		t.Fatalf("sidecar appeared as session: %+v %v", list, err)
	}
}
