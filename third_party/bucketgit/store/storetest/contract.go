// Package storetest provides reusable conformance tests for BucketGit stores.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"reflect"
	"sync"
	"testing"

	"github.com/bucketgit/bgit/store"
)

type Factory func(t *testing.T) store.Writer

func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("round-trip", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		if err := s.Write(ctx, "objects/ab/cdef", []byte("payload")); err != nil {
			t.Fatal(err)
		}
		got, err := s.Read(ctx, "objects/ab/cdef")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "payload" {
			t.Fatalf("read = %q", got)
		}
		if err := s.Write(ctx, "objects/ab/cdef", []byte("replacement")); err != nil {
			t.Fatal(err)
		}
		got, err = s.Read(ctx, "objects/ab/cdef")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "replacement" {
			t.Fatalf("replacement read = %q", got)
		}
		paths, err := s.List(ctx, "objects")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"objects/ab/cdef"}; !reflect.DeepEqual(paths, want) {
			t.Fatalf("paths = %#v, want %#v", paths, want)
		}
		if err := s.Delete(ctx, "objects/ab/cdef"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Read(ctx, "objects/ab/cdef"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("read deleted error = %v", err)
		}
	})

	t.Run("zero-byte", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		if err := s.Write(ctx, "HEAD", nil); err != nil {
			t.Fatal(err)
		}
		got, err := s.Read(ctx, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("zero-byte object length = %d", len(got))
		}
	})

	t.Run("large-object", func(t *testing.T) {
		s := factory(t)
		payload := bytes.Repeat([]byte("bucketgit"), 256*1024)
		if err := s.Write(context.Background(), "objects/large", payload); err != nil {
			t.Fatal(err)
		}
		got, err := s.Read(context.Background(), "objects/large")
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("large read length=%d err=%v", len(got), err)
		}
	})

	t.Run("concurrent-complete-writes", func(t *testing.T) {
		s := factory(t)
		payloads := make([][]byte, 6)
		var wait sync.WaitGroup
		errorsByWriter := make([]error, len(payloads))
		for i := range payloads {
			payloads[i] = bytes.Repeat([]byte{byte('a' + i)}, 128*1024)
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				errorsByWriter[index] = s.Write(context.Background(), "objects/concurrent", payloads[index])
			}(i)
		}
		wait.Wait()
		for _, err := range errorsByWriter {
			if err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.Read(context.Background(), "objects/concurrent")
		if err != nil {
			t.Fatal(err)
		}
		complete := false
		for _, payload := range payloads {
			if bytes.Equal(got, payload) {
				complete = true
				break
			}
		}
		if !complete {
			t.Fatal("concurrent write produced torn content")
		}
	})

	t.Run("invalid-paths", func(t *testing.T) {
		s := factory(t)
		ctx := context.Background()
		for _, value := range []string{"../escape", "/absolute", "a/../../escape", "a\\escape", "%2e%2e/escape", "safe%2f..%2fescape"} {
			if err := s.Write(ctx, value, []byte("bad")); !errors.Is(err, store.ErrInvalidPath) {
				t.Fatalf("Write(%q) error = %v", value, err)
			}
		}
	})

	t.Run("cancelled-context", func(t *testing.T) {
		s := factory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.Write(ctx, "HEAD", nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled write error = %v", err)
		}
	})
}

func RunCompareAndSwap(t *testing.T, factory Factory) {
	t.Helper()
	backend := factory(t)
	cas, ok := backend.(store.CompareAndSwapper)
	if !ok {
		t.Fatalf("%T does not implement store.CompareAndSwapper", backend)
	}
	ctx := context.Background()
	path := ".bucketgit/broker-state/v1/contract.json"
	if err := cas.CompareAndSwap(ctx, path, store.ObjectState{}, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := cas.CompareAndSwap(ctx, path, store.ObjectState{}, []byte("stale")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("missing-state stale update error = %v", err)
	}
	if err := cas.CompareAndSwap(ctx, path, store.ObjectState{Exists: true, Data: []byte("wrong")}, []byte("stale")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong-value stale update error = %v", err)
	}
	if err := cas.CompareAndSwap(ctx, path, store.ObjectState{Exists: true, Data: []byte("one")}, []byte("two")); err != nil {
		t.Fatal(err)
	}
	data, err := backend.Read(ctx, path)
	if err != nil || string(data) != "two" {
		t.Fatalf("read after CAS = %q, %v", data, err)
	}
}
