package transport

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bucketgit/bgit/repository"
	"github.com/bucketgit/bgit/store"
	fsstore "github.com/bucketgit/bgit/store/fs"
)

type receiveMemoryStore struct {
	objects map[string][]byte
	refs    map[string]string
}

func (s *receiveMemoryStore) Read(_ context.Context, path string) ([]byte, error) {
	data, ok := s.objects[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return data, nil
}
func (s *receiveMemoryStore) List(context.Context, string) ([]string, error) { return nil, nil }
func (s *receiveMemoryStore) Write(_ context.Context, path string, data []byte) error {
	s.objects[path] = append([]byte(nil), data...)
	return nil
}
func (s *receiveMemoryStore) Delete(_ context.Context, path string) error {
	delete(s.objects, path)
	return nil
}
func (s *receiveMemoryStore) ListRefs(context.Context) (map[string]string, error) {
	result := map[string]string{}
	for k, v := range s.refs {
		result[k] = v
	}
	return result, nil
}
func (s *receiveMemoryStore) CompareAndSwapRef(_ context.Context, ref, oldOID, newOID string) error {
	if s.refs[ref] != oldOID {
		return store.ErrConflict
	}
	if newOID == "" {
		delete(s.refs, ref)
	} else {
		s.refs[ref] = newOID
	}
	return nil
}

func TestServeReceivePackDeletesRef(t *testing.T) {
	oid := "0123456789abcdef0123456789abcdef01234567"
	target := &receiveMemoryStore{objects: map[string][]byte{}, refs: map[string]string{"refs/heads/old": oid}}
	var input bytes.Buffer
	if err := WriteString(&input, oid+" "+zeroOID+" refs/heads/old\x00report-status\n"); err != nil {
		t.Fatal(err)
	}
	if err := WriteFlush(&input); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	err := ServeReceivePack(context.Background(), repository.Open(target, nil), target, &input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := target.refs["refs/heads/old"]; ok {
		t.Fatal("ref was not deleted")
	}
	if !strings.Contains(output.String(), "unpack ok") || !strings.Contains(output.String(), "ok refs/heads/old") {
		t.Fatalf("report=%q", output.String())
	}
}

func TestServeReceivePackAcceptsThinPackWithExistingBase(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGitTest(t, "", "init", "--bare", remote)
	worktree := filepath.Join(t.TempDir(), "work")
	runGitTest(t, "", "clone", remote, worktree)
	runGitTest(t, worktree, "config", "user.name", "Ada")
	runGitTest(t, worktree, "config", "user.email", "ada@example.com")
	readme := filepath.Join(worktree, "README.md")
	if err := os.WriteFile(readme, []byte(strings.Repeat("alpha\n", 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, worktree, "add", "README.md")
	runGitTest(t, worktree, "commit", "-m", "Initial")
	runGitTest(t, worktree, "push", "origin", "HEAD:refs/heads/main")
	old := strings.TrimSpace(runGitTest(t, worktree, "rev-parse", "HEAD"))
	if err := os.WriteFile(readme, []byte(strings.Repeat("alpha\n", 180)+strings.Repeat("beta\n", 20)), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, worktree, "add", "README.md")
	runGitTest(t, worktree, "commit", "-m", "Update")
	newOID := strings.TrimSpace(runGitTest(t, worktree, "rev-parse", "HEAD"))
	command := exec.Command("git", "-C", worktree, "pack-objects", "--stdout", "--thin", "--revs")
	command.Stdin = strings.NewReader(newOID + "\n^" + old + "\n")
	pack, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	target, err := fsstore.New(remote)
	if err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	if err := WriteString(&input, old+" "+newOID+" refs/heads/main\x00report-status\n"); err != nil {
		t.Fatal(err)
	}
	if err := WriteFlush(&input); err != nil {
		t.Fatal(err)
	}
	_, _ = input.Write(pack)
	var output bytes.Buffer
	if err := ServeReceivePack(t.Context(), repository.Open(target, target), target, &input, &output); err != nil {
		t.Fatal(err)
	}
	refs, err := target.ListRefs(t.Context())
	if err != nil || refs["refs/heads/main"] != newOID {
		t.Fatalf("refs=%#v err=%v", refs, err)
	}
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

var _ ReceiveStore = (*receiveMemoryStore)(nil)
