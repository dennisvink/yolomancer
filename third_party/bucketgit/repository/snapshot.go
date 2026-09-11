package repository

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

type File struct {
	Path string
	Mode string
	OID  OID
	Data []byte
}

type ChangeStatus string

const (
	Added    ChangeStatus = "added"
	Modified ChangeStatus = "modified"
	Deleted  ChangeStatus = "deleted"
)

type Change struct {
	Path   string
	Status ChangeStatus
	Old    *File
	New    *File
}

// Snapshot returns all blobs reachable from a revision, keyed by canonical
// repository-relative path.
func (r *Repository) Snapshot(ctx context.Context, revision string) (map[string]File, error) {
	oid, err := r.Resolve(ctx, revision)
	if err != nil {
		return nil, err
	}
	object, err := r.Object(ctx, oid)
	if err != nil {
		return nil, err
	}
	if object.Type == CommitObject || object.Type == TagObject {
		commit, err := r.Commit(ctx, oid)
		if err != nil {
			return nil, err
		}
		oid = commit.Tree
	}
	files := map[string]File{}
	if err := r.walkTree(ctx, oid, "", files); err != nil {
		return nil, err
	}
	return files, nil
}

func (r *Repository) walkTree(ctx context.Context, tree OID, prefix string, files map[string]File) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := r.TreeEntries(ctx, tree)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name, err := snapshotPath(prefix, entry.Name)
		if err != nil {
			return err
		}
		if entry.Type == TreeObject || strings.HasPrefix(entry.Mode, "04") || entry.Mode == "40000" {
			if err := r.walkTree(ctx, entry.OID, name, files); err != nil {
				return err
			}
			continue
		}
		if entry.Type != BlobObject {
			continue
		}
		object, err := r.Object(ctx, entry.OID)
		if err != nil {
			return err
		}
		files[name] = File{Path: name, Mode: entry.Mode, OID: entry.OID, Data: append([]byte(nil), object.Data...)}
	}
	return nil
}

func snapshotPath(prefix, name string) (string, error) {
	if name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
		return "", fmt.Errorf("invalid tree entry name %q", name)
	}
	result := path.Join(prefix, name)
	if result == "." || strings.HasPrefix(result, "../") || strings.HasPrefix(result, "/") {
		return "", fmt.Errorf("invalid tree path %q", result)
	}
	return result, nil
}

// Diff compares two repository snapshots without imposing a presentation
// format. Changes are sorted by path.
func (r *Repository) Diff(ctx context.Context, leftRevision, rightRevision string) ([]Change, error) {
	left, err := r.Snapshot(ctx, leftRevision)
	if err != nil {
		return nil, err
	}
	right, err := r.Snapshot(ctx, rightRevision)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(left)+len(right))
	seen := map[string]struct{}{}
	for name := range left {
		seen[name] = struct{}{}
		paths = append(paths, name)
	}
	for name := range right {
		if _, ok := seen[name]; !ok {
			paths = append(paths, name)
		}
	}
	sort.Strings(paths)
	changes := make([]Change, 0)
	for _, name := range paths {
		oldFile, oldOK := left[name]
		newFile, newOK := right[name]
		if oldOK && newOK && oldFile.OID == newFile.OID && oldFile.Mode == newFile.Mode {
			continue
		}
		change := Change{Path: name}
		if oldOK {
			copy := oldFile
			change.Old = &copy
		}
		if newOK {
			copy := newFile
			change.New = &copy
		}
		switch {
		case !oldOK:
			change.Status = Added
		case !newOK:
			change.Status = Deleted
		default:
			change.Status = Modified
		}
		changes = append(changes, change)
	}
	return changes, nil
}

// ArchiveTar writes a deterministic tar archive for a revision.
func (r *Repository) ArchiveTar(ctx context.Context, revision string, output io.Writer) error {
	files, err := r.Snapshot(ctx, revision)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	writer := tar.NewWriter(output)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			_ = writer.Close()
			return err
		}
		file := files[name]
		header := &tar.Header{Name: file.Path, Mode: archiveMode(file.Mode), Size: int64(len(file.Data)), ModTime: time.Unix(0, 0), Format: tar.FormatPAX}
		if file.Mode == "120000" {
			header.Typeflag = tar.TypeSymlink
			header.Linkname = string(file.Data)
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			_ = writer.Close()
			return err
		}
		if header.Typeflag != tar.TypeSymlink {
			if _, err := writer.Write(file.Data); err != nil {
				_ = writer.Close()
				return err
			}
		}
	}
	return writer.Close()
}

func archiveMode(mode string) int64 {
	if mode == "100755" {
		return 0o755
	}
	return 0o644
}
