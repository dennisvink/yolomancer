package transport

import (
	"bufio"
	"bytes"
	"context"
	"testing"

	"github.com/bucketgit/bgit/repository"
)

func FuzzReadPacket(f *testing.F) {
	f.Add([]byte("0008test"))
	f.Add([]byte("0000"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = ReadPacket(bufio.NewReader(bytes.NewReader(data))) })
}

func FuzzDecodeReceivedPack(f *testing.F) {
	f.Add([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = decodeReceivedPack(context.Background(), repository.Open(emptyReader{}, nil), data)
	})
}

type emptyReader struct{}

func (emptyReader) Read(context.Context, string) ([]byte, error)   { return nil, errMissing }
func (emptyReader) List(context.Context, string) ([]string, error) { return nil, nil }

type missingError struct{}

func (missingError) Error() string { return "missing" }

var errMissing missingError
