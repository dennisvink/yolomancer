// Package fs implements a BucketGit object store rooted in a local directory.
package fs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bucketgit/bgit/store"
)

type Store struct {
	root         string
	resolvedRoot string
}

func New(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("filesystem store root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return &Store{root: abs, resolvedRoot: resolved}, nil
}

func (s *Store) Read(ctx context.Context, objectPath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := s.safePath(objectPath, false, true)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(target)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, iofs.ErrNotExist
	}
	return data, err
}

func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := s.safePath(prefix, true, true)
	if err != nil {
		return nil, err
	}
	var paths []string
	err = filepath.WalkDir(target, func(current string, entry iofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, iofs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink %s", store.ErrInvalidPath, current)
		}
		rel, err := filepath.Rel(s.root, current)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func (s *Store) Write(ctx context.Context, objectPath string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.safePath(objectPath, false, false)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := s.ensureResolvedWithinRoot(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bgit-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceFile(tmpName, target)
}

func (s *Store) Delete(ctx context.Context, objectPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.safePath(objectPath, false, true)
	if err != nil {
		return err
	}
	err = os.Remove(target)
	if errors.Is(err, iofs.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) ListRefs(ctx context.Context) (map[string]string, error) {
	return store.ReadRefs(ctx, s)
}

func (s *Store) CompareAndSwapRef(ctx context.Context, ref, oldOID, newOID string) error {
	if err := store.ValidateRefUpdate(ref, oldOID, newOID); err != nil {
		return err
	}
	target, err := s.safePath(ref, false, false)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	release, err := acquireFileLease(ctx, target+".bgit-lock")
	if err != nil {
		return err
	}
	defer release()
	current, err := s.Read(ctx, ref)
	if err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return err
	}
	currentOID := strings.TrimSpace(string(current))
	if errors.Is(err, iofs.ErrNotExist) {
		currentOID = ""
	}
	if !(store.IsZeroOID(oldOID) && currentOID == "") && currentOID != oldOID {
		return store.ErrConflict
	}
	if store.IsZeroOID(newOID) {
		return s.Delete(ctx, ref)
	}
	return s.Write(ctx, ref, []byte(newOID+"\n"))
}

func (s *Store) CompareAndSwap(ctx context.Context, objectPath string, expected store.ObjectState, replacement []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.safePath(objectPath, false, false)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	release, err := acquireFileLease(ctx, target+".bgit-cas-lock")
	if err != nil {
		return err
	}
	defer release()
	current, readErr := os.ReadFile(target)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, iofs.ErrNotExist) {
		return readErr
	}
	if exists != expected.Exists || exists && !bytes.Equal(current, expected.Data) {
		return store.ErrConflict
	}
	return s.Write(ctx, objectPath, replacement)
}

const fileLeaseLifetime = 2 * time.Minute

func acquireFileLease(ctx context.Context, path string) (func(), error) {
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := fmt.Fprintf(lock, "%d\n", time.Now().UTC().Unix()); writeErr != nil {
				_ = lock.Close()
				_ = os.Remove(path)
				return nil, writeErr
			}
			if closeErr := lock.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, closeErr
			}
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(path)
		if statErr != nil || time.Since(info.ModTime()) <= fileLeaseLifetime {
			return nil, store.ErrConflict
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, store.ErrConflict
		}
	}
	return nil, store.ErrConflict
}

var _ store.RefStore = (*Store)(nil)
var _ store.CompareAndSwapper = (*Store)(nil)

func (s *Store) safePath(value string, allowEmpty, resolveTarget bool) (string, error) {
	rel, err := store.ValidatePath(value, allowEmpty)
	if err != nil {
		return "", err
	}
	target := s.root
	if rel != "" {
		target = filepath.Join(s.root, filepath.FromSlash(rel))
	}
	if err := ensureWithinRoot(s.root, target); err != nil {
		return "", err
	}
	if resolveTarget {
		resolved, err := filepath.EvalSymlinks(target)
		if err == nil {
			if err := ensureWithinRoot(s.resolvedRoot, resolved); err != nil {
				return "", err
			}
		} else if !errors.Is(err, iofs.ErrNotExist) {
			return "", err
		}
	}
	return target, nil
}

func (s *Store) ensureResolvedWithinRoot(dir string) error {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	return ensureWithinRoot(s.resolvedRoot, resolved)
}

func ensureWithinRoot(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: path escapes store root", store.ErrInvalidPath)
	}
	return nil
}
