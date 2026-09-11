package repository

import (
	"archive/tar"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"io"
	"io/fs"
	"sort"
	"testing"
)

type memoryStore map[string][]byte

func (m memoryStore) Read(_ context.Context, path string) ([]byte, error) {
	data, ok := m[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (m memoryStore) List(_ context.Context, prefix string) ([]string, error) {
	var paths []string
	for path := range m {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func putObject(t *testing.T, store memoryStore, typ ObjectType, data []byte) OID {
	t.Helper()
	raw := append([]byte(string(typ)+" "+itoa(len(data))+"\x00"), data...)
	hash := sha1.Sum(raw)
	oid := hex.EncodeToString(hash[:])
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	store["objects/"+oid[:2]+"/"+oid[2:]] = compressed.Bytes()
	return OID(oid)
}

func TestRepositoryResolveCommitAndTree(t *testing.T) {
	objects := memoryStore{}
	blob := putObject(t, objects, BlobObject, []byte("hello\n"))
	treeData := append([]byte("100644 README.md\x00"), mustOIDBytes(t, blob)...)
	tree := putObject(t, objects, TreeObject, treeData)
	commit := putObject(t, objects, CommitObject, []byte("tree "+tree.String()+"\nauthor A <a@example.com> 1 +0000\ncommitter A <a@example.com> 1 +0000\n\nInitial\n"))
	objects["refs/heads/main"] = []byte(commit.String() + "\n")

	repo := Open(objects, nil)
	resolved, err := repo.Resolve(context.Background(), "main")
	if err != nil || resolved != commit {
		t.Fatalf("Resolve(main) = %s, %v", resolved, err)
	}
	parsed, err := repo.Commit(context.Background(), commit)
	if err != nil || parsed.Tree != tree || parsed.Subject != "Initial" {
		t.Fatalf("Commit = %#v, %v", parsed, err)
	}
	entries, err := repo.Tree(context.Background(), "main")
	if err != nil || len(entries) != 1 || entries[0].OID != blob || entries[0].Name != "README.md" {
		t.Fatalf("Tree = %#v, %v", entries, err)
	}
}

func TestRepositoryRejectsHashMismatch(t *testing.T) {
	objects := memoryStore{}
	oid := putObject(t, objects, BlobObject, []byte("hello"))
	objects["objects/"+oid.String()[:2]+"/"+oid.String()[2:]][5] ^= 1
	if _, err := Open(objects, nil).Object(context.Background(), oid); err == nil {
		t.Fatal("Object accepted corrupted data")
	}
}

func TestParsePackIndexV2Offsets(t *testing.T) {
	const fanoutBytes = 256 * 4
	data := make([]byte, 8+fanoutBytes+20+4+4+8)
	copy(data[:4], []byte{0xff, 't', 'O', 'c'})
	binary.BigEndian.PutUint32(data[4:8], 2)
	for i := 0; i < 256; i++ {
		binary.BigEndian.PutUint32(data[8+i*4:12+i*4], 1)
	}
	oidBytes := bytes.Repeat([]byte{0xab}, 20)
	position := 8 + fanoutBytes
	copy(data[position:position+20], oidBytes)
	position += 20 + 4
	binary.BigEndian.PutUint32(data[position:position+4], 0x80000000)
	position += 4
	binary.BigEndian.PutUint64(data[position:position+8], 1<<33)
	hashes, offsets, err := parsePackIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 1 || hashes[0].String() != hex.EncodeToString(oidBytes) || len(offsets) != 1 || offsets[0] != 1<<33 {
		t.Fatalf("hashes=%v offsets=%v", hashes, offsets)
	}
	if _, _, err := parsePackIndex(data[:len(data)-1]); err == nil {
		t.Fatal("truncated large offset was accepted")
	}
}

func TestSnapshotDiffAndArchive(t *testing.T) {
	objects := memoryStore{}
	oldBlob := putObject(t, objects, BlobObject, []byte("old\n"))
	oldTree := putObject(t, objects, TreeObject, append([]byte("100644 README.md\x00"), mustOIDBytes(t, oldBlob)...))
	oldCommit := putObject(t, objects, CommitObject, []byte("tree "+oldTree.String()+"\nauthor A <a@example.com> 1 +0000\ncommitter A <a@example.com> 1 +0000\n\nOld\n"))
	newBlob := putObject(t, objects, BlobObject, []byte("new\n"))
	newTree := putObject(t, objects, TreeObject, append([]byte("100755 README.md\x00"), mustOIDBytes(t, newBlob)...))
	newCommit := putObject(t, objects, CommitObject, []byte("tree "+newTree.String()+"\nparent "+oldCommit.String()+"\nauthor A <a@example.com> 2 +0000\ncommitter A <a@example.com> 2 +0000\n\nNew\n"))
	objects["refs/heads/old"] = []byte(oldCommit.String() + "\n")
	objects["refs/heads/main"] = []byte(newCommit.String() + "\n")
	repo := Open(objects, nil)
	snapshot, err := repo.Snapshot(t.Context(), "main")
	if err != nil || string(snapshot["README.md"].Data) != "new\n" || snapshot["README.md"].Mode != "100755" {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
	changes, err := repo.Diff(t.Context(), "old", "main")
	if err != nil || len(changes) != 1 || changes[0].Status != Modified || changes[0].Path != "README.md" {
		t.Fatalf("changes=%#v err=%v", changes, err)
	}
	var archive bytes.Buffer
	if err := repo.ArchiveTar(t.Context(), "main", &archive); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(&archive)
	header, err := reader.Next()
	if err != nil || header.Name != "README.md" || header.Mode != 0o755 {
		t.Fatalf("header=%#v err=%v", header, err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "new\n" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func mustOIDBytes(t *testing.T, oid OID) []byte {
	t.Helper()
	data, err := hex.DecodeString(oid.String())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}
