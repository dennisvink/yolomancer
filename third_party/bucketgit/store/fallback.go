package store

import (
	"context"
	"errors"
	"io/fs"
)

type FallbackReader struct {
	Primary  Reader
	Fallback Reader
}

func (s FallbackReader) Read(ctx context.Context, path string) ([]byte, error) {
	data, err := s.Primary.Read(ctx, path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return data, err
	}
	return s.Fallback.Read(ctx, path)
}

func (s FallbackReader) List(ctx context.Context, prefix string) ([]string, error) {
	paths, err := s.Primary.List(ctx, prefix)
	if err == nil {
		return paths, nil
	}
	return s.Fallback.List(ctx, prefix)
}
