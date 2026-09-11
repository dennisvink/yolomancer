package fs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bucketgit/bgit/store"
	"github.com/bucketgit/bgit/store/storetest"
)

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Writer {
		t.Helper()
		s, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestCompareAndSwapRecoversStaleLease(t *testing.T) {
	root := t.TempDir()
	backend, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "objects", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".bgit-cas-lock"
	if err := os.WriteFile(lockPath, []byte("abandoned"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-fileLeaseLifetime - time.Minute)
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	if err := backend.CompareAndSwap(t.Context(), "objects/state.json", store.ObjectState{}, []byte("recovered")); err != nil {
		t.Fatal(err)
	}
}

func TestCompareAndSwapContract(t *testing.T) {
	storetest.RunCompareAndSwap(t, func(t *testing.T) store.Writer {
		backend, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return backend
	})
}

func TestCompareAndSwapRef(t *testing.T) {
	backend, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	one := "0123456789abcdef0123456789abcdef01234567"
	two := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := backend.CompareAndSwapRef(t.Context(), "refs/heads/main", "", one); err != nil {
		t.Fatal(err)
	}
	if err := backend.CompareAndSwapRef(t.Context(), "refs/heads/main", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", two); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	if err := backend.CompareAndSwapRef(t.Context(), "refs/heads/main", one, two); err != nil {
		t.Fatal(err)
	}
	refs, err := backend.ListRefs(t.Context())
	if err != nil || refs["refs/heads/main"] != two {
		t.Fatalf("refs = %#v, %v", refs, err)
	}
}

func TestStoreRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "objects")); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), "objects/ab/cdef", []byte("bad")); err == nil {
		t.Fatal("expected symlink escape write to fail")
	}
}
