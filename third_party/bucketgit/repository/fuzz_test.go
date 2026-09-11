package repository

import "testing"

func FuzzParseTree(f *testing.F) {
	f.Add([]byte("100644 README.md\x0001234567890123456789"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = parseTree(data) })
}

func FuzzParsePackIndex(f *testing.F) {
	f.Add([]byte{0xff, 't', 'O', 'c', 0, 0, 0, 2})
	f.Fuzz(func(t *testing.T, data []byte) { _, _, _ = parsePackIndex(data) })
}

func FuzzApplyDelta(f *testing.F) {
	f.Add([]byte("base"), []byte{4, 4, 4, 't', 'e', 's', 't'})
	f.Fuzz(func(t *testing.T, base, delta []byte) { _, _ = applyDelta(base, delta) })
}
