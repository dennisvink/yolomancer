// Package store defines provider-neutral storage contracts used by BucketGit
// repositories and brokers.
package store

import (
	"context"
	"errors"
	"io/fs"
)

var (
	ErrConflict     = errors.New("bucketgit store conflict")
	ErrInvalidPath  = errors.New("bucketgit invalid store path")
	ErrReadOnly     = errors.New("bucketgit store is read-only")
	ErrNotSupported = errors.New("bucketgit store operation is not supported")
)

var ErrNotFound = fs.ErrNotExist

type Reader interface {
	Read(ctx context.Context, path string) ([]byte, error)
	List(ctx context.Context, prefix string) ([]string, error)
}

type Writer interface {
	Reader
	Write(ctx context.Context, path string, data []byte) error
	Delete(ctx context.Context, path string) error
}

// ObjectState is the expected value supplied to an object compare-and-swap.
// Exists distinguishes a missing object from an existing zero-byte object.
type ObjectState struct {
	Exists bool
	Data   []byte
}

// CompareAndSwapper conditionally replaces an object when its current value
// still matches the caller's observed state.
type CompareAndSwapper interface {
	CompareAndSwap(ctx context.Context, path string, expected ObjectState, replacement []byte) error
}

type RefStore interface {
	ListRefs(ctx context.Context) (map[string]string, error)
	CompareAndSwapRef(ctx context.Context, ref, oldOID, newOID string) error
}
