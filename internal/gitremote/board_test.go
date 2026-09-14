package gitremote

import (
	"context"
	"testing"
)

func TestBoardRepository(t *testing.T) {
	for _, value := range []string{"prosus/prosus-user-096/workshop", "yolomancer://prosus/prosus-user-096/workshop"} {
		got, err := boardRepository(context.Background(), value)
		if err != nil || got != "prosus/prosus-user-096/workshop" {
			t.Fatalf("%q: %s %v", value, got, err)
		}
	}
	for _, value := range []string{"https://github.com/owner/repo", "prosus/prosus-user-096/../../other"} {
		if _, err := boardRepository(context.Background(), value); err == nil {
			t.Fatal("invalid repository accepted")
		}
	}
}
