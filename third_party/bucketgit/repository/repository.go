// Package repository implements provider-neutral Git repository reads over a
// BucketGit object store.
package repository

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bucketgit/bgit/store"
)

type OID string

func ParseOID(value string) (OID, error) {
	value = strings.TrimSpace(value)
	if len(value) != sha1.Size*2 {
		return "", fmt.Errorf("invalid SHA-1 object id %q", value)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("invalid SHA-1 object id %q: %w", value, err)
	}
	return OID(strings.ToLower(value)), nil
}

func (o OID) String() string { return string(o) }

type ObjectType string

const (
	CommitObject ObjectType = "commit"
	TreeObject   ObjectType = "tree"
	BlobObject   ObjectType = "blob"
	TagObject    ObjectType = "tag"
)

type Object struct {
	OID  OID
	Type ObjectType
	Data []byte
}

type Signature struct {
	Name      string
	Email     string
	Timestamp int64
	Timezone  string
}

type Commit struct {
	OID       OID
	Tree      OID
	Parents   []OID
	Author    Signature
	Committer Signature
	Subject   string
	Body      string
}

type TreeEntry struct {
	Mode string
	Name string
	OID  OID
	Type ObjectType
}

type Option func(*Repository)

const DefaultMaxObjectSize int64 = 128 << 20

// WithObjectVerification controls verification of loose-object hashes. It is
// enabled by default.
func WithObjectVerification(enabled bool) Option {
	return func(r *Repository) { r.verifyObjects = enabled }
}

// WithMaxObjectSize limits decompressed loose, packed, and delta-expanded Git
// objects. Values <= 0 restore DefaultMaxObjectSize.
func WithMaxObjectSize(bytes int64) Option {
	return func(r *Repository) {
		if bytes <= 0 {
			bytes = DefaultMaxObjectSize
		}
		r.maxObjectSize = bytes
	}
}

type Repository struct {
	objects       store.Reader
	refs          store.RefStore
	verifyObjects bool
	maxObjectSize int64
	mu            sync.RWMutex
	cache         map[OID]Object
	packsLoaded   bool
	packs         []packIndex
	offsetCache   map[string]Object
}

func Open(objects store.Reader, refs store.RefStore, options ...Option) *Repository {
	r := &Repository{objects: objects, refs: refs, verifyObjects: true, maxObjectSize: DefaultMaxObjectSize, cache: map[OID]Object{}, offsetCache: map[string]Object{}}
	for _, option := range options {
		option(r)
	}
	return r
}

func (r *Repository) Resolve(ctx context.Context, revision string) (OID, error) {
	if oid, err := ParseOID(revision); err == nil {
		return oid, nil
	}
	base, distance := ancestorRevision(revision)
	if distance > 0 {
		oid, err := r.Resolve(ctx, base)
		if err != nil {
			return "", err
		}
		for range distance {
			commit, err := r.Commit(ctx, oid)
			if err != nil {
				return "", err
			}
			if len(commit.Parents) == 0 {
				return "", fs.ErrNotExist
			}
			oid = commit.Parents[0]
		}
		return oid, nil
	}
	refs, err := r.ListRefs(ctx)
	if err != nil {
		return "", err
	}
	for _, candidate := range revisionCandidates(revision) {
		if oid, ok := refs[candidate]; ok {
			return oid, nil
		}
	}
	return "", fs.ErrNotExist
}

func (r *Repository) Object(ctx context.Context, oid OID) (Object, error) {
	parsed, err := ParseOID(oid.String())
	if err != nil {
		return Object{}, err
	}
	r.mu.RLock()
	if object, ok := r.cache[parsed]; ok {
		r.mu.RUnlock()
		return cloneObject(object), nil
	}
	r.mu.RUnlock()
	if r.objects == nil {
		return Object{}, errors.New("repository object reader is nil")
	}
	compressed, err := r.objects.Read(ctx, "objects/"+parsed.String()[:2]+"/"+parsed.String()[2:])
	var object Object
	var raw []byte
	if err == nil {
		object, raw, err = decodeLooseObject(parsed, compressed, r.maxObjectSize)
	} else if errors.Is(err, fs.ErrNotExist) {
		object, err = r.packedObject(ctx, parsed)
		if err == nil {
			raw = objectBytes(object.Type, object.Data)
		}
	}
	if err != nil {
		return Object{}, err
	}
	if r.verifyObjects {
		hash := sha1.Sum(raw)
		if hex.EncodeToString(hash[:]) != parsed.String() {
			return Object{}, fmt.Errorf("object %s hash mismatch", parsed)
		}
	}
	r.mu.Lock()
	r.cache[parsed] = object
	r.mu.Unlock()
	return cloneObject(object), nil
}

func objectBytes(typ ObjectType, data []byte) []byte {
	return append([]byte(string(typ)+" "+strconv.Itoa(len(data))+"\x00"), data...)
}

func (r *Repository) Commit(ctx context.Context, oid OID) (Commit, error) {
	object, err := r.Object(ctx, oid)
	if err != nil {
		return Commit{}, err
	}
	if object.Type == TagObject {
		target, err := tagTarget(object.Data)
		if err != nil {
			return Commit{}, err
		}
		return r.Commit(ctx, target)
	}
	if object.Type != CommitObject {
		return Commit{}, fmt.Errorf("%s is not a commit", oid)
	}
	return parseCommit(object.OID, object.Data)
}

func (r *Repository) Tree(ctx context.Context, revision string) ([]TreeEntry, error) {
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
	return r.TreeEntries(ctx, oid)
}

func (r *Repository) TreeEntries(ctx context.Context, oid OID) ([]TreeEntry, error) {
	object, err := r.Object(ctx, oid)
	if err != nil {
		return nil, err
	}
	if object.Type != TreeObject {
		return nil, fmt.Errorf("%s is not a tree", oid)
	}
	return parseTree(object.Data)
}

func (r *Repository) ListRefs(ctx context.Context) (map[string]OID, error) {
	if r.refs != nil {
		values, err := r.refs.ListRefs(ctx)
		if err != nil {
			return nil, err
		}
		return parseRefs(values), nil
	}
	if r.objects == nil {
		return nil, errors.New("repository object reader is nil")
	}
	values := map[string]string{}
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		paths, err := r.objects.List(ctx, prefix)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			data, err := r.objects.Read(ctx, path)
			if err != nil {
				return nil, err
			}
			values[path] = strings.TrimSpace(string(data))
		}
	}
	packed, err := r.objects.Read(ctx, "packed-refs")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		for _, line := range strings.Split(string(packed), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && !strings.HasPrefix(line, "^") && !strings.HasPrefix(line, "#") {
				values[fields[1]] = fields[0]
			}
		}
	}
	return parseRefs(values), nil
}

func (r *Repository) IsAncestor(ctx context.Context, ancestor, descendant OID) (bool, error) {
	seen := map[OID]struct{}{}
	stack := []OID{descendant}
	for len(stack) > 0 {
		oid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if oid == ancestor {
			return true, nil
		}
		if _, ok := seen[oid]; ok {
			continue
		}
		seen[oid] = struct{}{}
		commit, err := r.Commit(ctx, oid)
		if err != nil {
			return false, err
		}
		stack = append(stack, commit.Parents...)
	}
	return false, nil
}

func (r *Repository) MergeBase(ctx context.Context, a, b OID) (OID, error) {
	aDepth, err := r.ancestorDepths(ctx, a)
	if err != nil {
		return "", err
	}
	bDepth, err := r.ancestorDepths(ctx, b)
	if err != nil {
		return "", err
	}
	best := OID("")
	bestDistance := int(^uint(0) >> 1)
	for oid, left := range aDepth {
		if right, ok := bDepth[oid]; ok && left+right < bestDistance {
			best, bestDistance = oid, left+right
		}
	}
	if best == "" {
		return "", fs.ErrNotExist
	}
	return best, nil
}

func (r *Repository) ReachableObjects(ctx context.Context, wants, haves []OID) ([]OID, error) {
	excluded := map[OID]struct{}{}
	for _, oid := range haves {
		if err := r.collectReachable(ctx, oid, excluded); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	included := map[OID]struct{}{}
	for _, oid := range wants {
		if err := r.collectReachable(ctx, oid, included); err != nil {
			return nil, err
		}
	}
	result := make([]OID, 0, len(included))
	for oid := range included {
		if _, skip := excluded[oid]; !skip {
			result = append(result, oid)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (r *Repository) collectReachable(ctx context.Context, oid OID, seen map[OID]struct{}) error {
	if _, ok := seen[oid]; ok {
		return nil
	}
	object, err := r.Object(ctx, oid)
	if err != nil {
		return err
	}
	seen[oid] = struct{}{}
	switch object.Type {
	case CommitObject:
		commit, err := parseCommit(oid, object.Data)
		if err != nil {
			return err
		}
		if err := r.collectReachable(ctx, commit.Tree, seen); err != nil {
			return err
		}
		for _, parent := range commit.Parents {
			if err := r.collectReachable(ctx, parent, seen); err != nil {
				return err
			}
		}
	case TreeObject:
		entries, err := parseTree(object.Data)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := r.collectReachable(ctx, entry.OID, seen); err != nil {
				return err
			}
		}
	case TagObject:
		target, err := tagTarget(object.Data)
		if err != nil {
			return err
		}
		return r.collectReachable(ctx, target, seen)
	}
	return nil
}

func (r *Repository) ancestorDepths(ctx context.Context, head OID) (map[OID]int, error) {
	depths := map[OID]int{head: 0}
	queue := []OID{head}
	for len(queue) > 0 {
		oid := queue[0]
		queue = queue[1:]
		commit, err := r.Commit(ctx, oid)
		if err != nil {
			return nil, err
		}
		for _, parent := range commit.Parents {
			depth := depths[oid] + 1
			if current, ok := depths[parent]; ok && current <= depth {
				continue
			}
			depths[parent] = depth
			queue = append(queue, parent)
		}
	}
	return depths, nil
}

func decodeLooseObject(oid OID, compressed []byte, maxSize int64) (Object, []byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return Object{}, nil, err
	}
	raw, err := readBounded(reader, maxSize, "loose object")
	closeErr := reader.Close()
	if err != nil {
		return Object{}, nil, err
	}
	if closeErr != nil {
		return Object{}, nil, closeErr
	}
	nul := bytes.IndexByte(raw, 0)
	if nul < 0 {
		return Object{}, nil, errors.New("invalid git object header")
	}
	fields := strings.Fields(string(raw[:nul]))
	if len(fields) != 2 {
		return Object{}, nil, errors.New("invalid git object header")
	}
	size, err := strconv.Atoi(fields[1])
	if err != nil || size < 0 || size != len(raw)-nul-1 {
		return Object{}, nil, errors.New("invalid git object size")
	}
	typ := ObjectType(fields[0])
	if typ != CommitObject && typ != TreeObject && typ != BlobObject && typ != TagObject {
		return Object{}, nil, fmt.Errorf("unsupported git object type %q", typ)
	}
	data := append([]byte(nil), raw[nul+1:]...)
	return Object{OID: oid, Type: typ, Data: data}, raw, nil
}

func readBounded(reader io.Reader, maxSize int64, label string) ([]byte, error) {
	if maxSize <= 0 {
		maxSize = DefaultMaxObjectSize
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxSize)
	}
	return data, nil
}

func parseCommit(oid OID, data []byte) (Commit, error) {
	commit := Commit{OID: oid}
	header, message, _ := strings.Cut(string(data), "\n\n")
	for _, line := range strings.Split(header, "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		switch key {
		case "tree":
			commit.Tree, _ = ParseOID(value)
		case "parent":
			if parent, err := ParseOID(value); err == nil {
				commit.Parents = append(commit.Parents, parent)
			}
		case "author":
			commit.Author = parseSignature(value)
		case "committer":
			commit.Committer = parseSignature(value)
		}
	}
	if commit.Tree == "" {
		return Commit{}, errors.New("commit missing valid tree")
	}
	commit.Body = strings.TrimRight(message, "\n")
	for _, line := range strings.Split(message, "\n") {
		if strings.TrimSpace(line) != "" {
			commit.Subject = strings.TrimSpace(line)
			break
		}
	}
	return commit, nil
}

func ParseCommitData(oid OID, data []byte) (Commit, error) {
	if _, err := ParseOID(oid.String()); err != nil {
		return Commit{}, err
	}
	return parseCommit(oid, data)
}

func parseSignature(value string) Signature {
	start, end := strings.LastIndex(value, " <"), strings.LastIndex(value, ">")
	if start < 0 || end < start {
		return Signature{Name: strings.TrimSpace(value)}
	}
	result := Signature{Name: strings.TrimSpace(value[:start]), Email: value[start+2 : end]}
	fields := strings.Fields(value[end+1:])
	if len(fields) > 0 {
		result.Timestamp, _ = strconv.ParseInt(fields[0], 10, 64)
	}
	if len(fields) > 1 {
		result.Timezone = fields[1]
	}
	return result
}

func parseTree(data []byte) ([]TreeEntry, error) {
	var entries []TreeEntry
	for len(data) > 0 {
		space := bytes.IndexByte(data, ' ')
		if space <= 0 {
			return nil, errors.New("invalid tree entry mode")
		}
		nulRelative := bytes.IndexByte(data[space+1:], 0)
		if nulRelative < 0 {
			return nil, errors.New("invalid tree entry name")
		}
		nameEnd := space + 1 + nulRelative
		if len(data) < nameEnd+1+sha1.Size {
			return nil, errors.New("truncated tree entry object id")
		}
		mode := string(data[:space])
		oid, err := ParseOID(hex.EncodeToString(data[nameEnd+1 : nameEnd+1+sha1.Size]))
		if err != nil {
			return nil, err
		}
		typ := BlobObject
		if mode == "40000" || mode == "040000" {
			typ = TreeObject
		}
		entries = append(entries, TreeEntry{Mode: mode, Name: string(data[space+1 : nameEnd]), OID: oid, Type: typ})
		data = data[nameEnd+1+sha1.Size:]
	}
	return entries, nil
}

func tagTarget(data []byte) (OID, error) {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "object ") {
			return ParseOID(strings.TrimSpace(strings.TrimPrefix(line, "object ")))
		}
	}
	return "", errors.New("tag missing object")
}

func parseRefs(values map[string]string) map[string]OID {
	refs := make(map[string]OID, len(values))
	for ref, value := range values {
		if !strings.HasPrefix(ref, "refs/") {
			continue
		}
		if oid, err := ParseOID(value); err == nil {
			refs[ref] = oid
		}
	}
	return refs
}

func revisionCandidates(revision string) []string {
	if strings.HasPrefix(revision, "refs/") {
		return []string{revision}
	}
	return []string{revision, "refs/heads/" + revision, "refs/tags/" + revision}
}

func ancestorRevision(revision string) (string, int) {
	base, suffix, ok := strings.Cut(revision, "~")
	if !ok || base == "" || suffix == "" {
		return revision, 0
	}
	distance, err := strconv.Atoi(suffix)
	if err != nil || distance < 1 {
		return revision, 0
	}
	return base, distance
}

func cloneObject(object Object) Object {
	object.Data = append([]byte(nil), object.Data...)
	return object
}
