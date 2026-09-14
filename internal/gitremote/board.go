package gitremote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// RunBoard uses the same enrolled SSH identity and controller as native Git.
func RunBoard(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" {
		fmt.Println("Usage: yolomancer board <list|get|create|edit|comment|move|assign|take|archive|unarchive|reset> [--repo URL] [--id N] [--version N] [--title TEXT] [--body TEXT] [--comment TEXT] [--lane LANE] [--assignee USER] [--confirm]\nUse --json to read action fields from stdin. Repository defaults to origin. Updates require the story version from list/get. Reset is owner-only and requires --confirm.")
		return nil
	}
	op := args[0]
	if !strings.Contains("|list|get|create|edit|comment|move|assign|take|archive|unarchive|reset|", "|"+op+"|") {
		return errors.New("unknown board command")
	}
	f := flag.NewFlagSet("board "+op, flag.ContinueOnError)
	repo := f.String("repo", "", "Repository URL or logical name (defaults to origin)")
	id := f.Int("id", 0, "Story ID")
	version := f.Int("version", 0, "Observed story version")
	title := f.String("title", "", "Story title")
	body := f.String("body", "", "Story description")
	comment := f.String("comment", "", "Comment text")
	lane := f.String("lane", "", "Target lane")
	assignee := f.String("assignee", "", "Assigned participant, or empty to unassign")
	after := f.Int("after", 0, "Place after story ID; 0 places first")
	confirm := f.Bool("confirm", false, "Confirm destructive board reset")
	agent := f.Bool("agent", false, "Label this action as agent-initiated")
	requestID := f.String("request-id", "", "Idempotency key (32 hex characters)")
	input := f.Bool("json", false, "Read action fields as JSON from stdin")
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected board arguments")
	}
	fields := map[string]any{}
	if *input {
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 16385)).Decode(&fields); err != nil {
			return err
		}
	}
	values := map[string]any{"id": *id, "version": *version, "title": *title, "body": *body, "comment": *comment, "lane": *lane, "assignee": *assignee, "after": *after, "confirm": *confirm, "agent": *agent, "request-id": *requestID}
	f.Visit(func(v *flag.Flag) {
		if value, ok := values[v.Name]; ok {
			name := v.Name
			if name == "after" {
				name = "afterId"
			}
			if name == "request-id" {
				name = "requestId"
			}
			fields[name] = value
		}
	})
	fields["op"] = op
	if op != "list" && op != "get" && fields["requestId"] == nil {
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return err
		}
		fields["requestId"] = hex.EncodeToString(token)
	}
	logical, err := boardRepository(ctx, *repo)
	if err != nil {
		return err
	}
	c, err := Load()
	if err != nil {
		return err
	}
	var result any
	if err = c.Call(ctx, map[string]any{"action": "board", "repo": logical, "board": fields}, &result); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
func boardRepository(ctx context.Context, address string) (string, error) {
	if address == "" {
		b, err := exec.CommandContext(ctx, "git", "remote", "get-url", "origin").Output()
		if err != nil {
			return "", errors.New("provide --repo or run inside a checkout with a Yolomancer origin")
		}
		address = strings.TrimSpace(string(b))
	}
	if strings.HasPrefix(address, "prosus/") {
		address = "yolomancer://" + address
	}
	return Logical(address)
}
