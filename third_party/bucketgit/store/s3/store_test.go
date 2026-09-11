package s3

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/bucketgit/bgit/store"
	"github.com/bucketgit/bgit/store/storetest"
)

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (f *fakeS3) GetObject(_ context.Context, in *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[aws.ToString(in.Key)]
	if !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}
	}
	return &awss3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(string(data))), ETag: aws.String(fakeETag(data))}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context, in *awss3.ListObjectsV2Input, _ ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for key := range f.objects {
		if strings.HasPrefix(key, aws.ToString(in.Prefix)) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := &awss3.ListObjectsV2Output{}
	for _, key := range keys {
		out.Contents = append(out.Contents, types.Object{Key: aws.String(key)})
	}
	return out, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, exists := f.objects[aws.ToString(in.Key)]
	if aws.ToString(in.IfNoneMatch) == "*" && exists || in.IfMatch != nil && (!exists || aws.ToString(in.IfMatch) != fakeETag(current)) {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "stale"}
	}
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.objects[aws.ToString(in.Key)] = data
	return &awss3.PutObjectOutput{}, nil
}

func fakeETag(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func (f *fakeS3) DeleteObject(_ context.Context, in *awss3.DeleteObjectInput, _ ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, aws.ToString(in.Key))
	return &awss3.DeleteObjectOutput{}, nil
}

func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Writer {
		t.Helper()
		s, err := New(&fakeS3{objects: map[string][]byte{}}, "bucket", "repo.git")
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestCompareAndSwapContract(t *testing.T) {
	storetest.RunCompareAndSwap(t, func(t *testing.T) store.Writer {
		backend, err := New(&fakeS3{objects: map[string][]byte{}}, "bucket", "repo.git")
		if err != nil {
			t.Fatal(err)
		}
		return backend
	})
}

func TestNewValidatesOptions(t *testing.T) {
	if _, err := New(nil, "bucket", "repo.git"); err == nil {
		t.Fatal("expected nil client error")
	}
	if _, err := New(&fakeS3{}, "", "repo.git"); err == nil {
		t.Fatal("expected empty bucket error")
	}
	if _, err := New(&fakeS3{}, "bucket", "../repo.git"); !errors.Is(err, store.ErrInvalidPath) {
		t.Fatalf("prefix error = %v", err)
	}
}
