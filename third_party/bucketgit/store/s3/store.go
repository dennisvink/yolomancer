// Package s3 implements a BucketGit object store backed by AWS S3.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/bucketgit/bgit/store"
)

type API interface {
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	ListObjectsV2(context.Context, *awss3.ListObjectsV2Input, ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
	PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	DeleteObject(context.Context, *awss3.DeleteObjectInput, ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
}

type Store struct {
	client API
	bucket string
	prefix string
}

type Options struct {
	Bucket  string
	Prefix  string
	Profile string
	Region  string
}

// Load constructs an S3 store using the standard AWS credential chain.
func Load(ctx context.Context, options Options) (*Store, error) {
	var loaders []func(*awsconfig.LoadOptions) error
	if region := strings.TrimSpace(options.Region); region != "" {
		loaders = append(loaders, awsconfig.WithRegion(region))
	}
	if profile := strings.TrimSpace(options.Profile); profile != "" {
		loaders = append(loaders, awsconfig.WithSharedConfigProfile(profile))
	}
	configuration, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return New(awss3.NewFromConfig(configuration), options.Bucket, options.Prefix)
}

func New(client API, bucket, prefix string) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("S3 client is required")
	}
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return nil, fmt.Errorf("S3 bucket is required")
	}
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	if prefix != "" {
		if _, err := store.ValidatePath(prefix, false); err != nil {
			return nil, fmt.Errorf("S3 prefix: %w", err)
		}
	}
	return &Store{client: client, bucket: bucket, prefix: prefix}, nil
}

func (s *Store) Read(ctx context.Context, objectPath string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := s.objectName(objectPath, false)
	if err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		if isNotFound(err) {
			return nil, fs.ErrNotExist
		}
		return nil, s.accessError("read", key, err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	queryPrefix, err := s.objectName(prefix, true)
	if err != nil {
		return nil, err
	}
	if queryPrefix != "" && !strings.HasSuffix(queryPrefix, "/") {
		queryPrefix += "/"
	}
	var paths []string
	var token *string
	for {
		out, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(queryPrefix),
			ContinuationToken: token,
		})
		if err != nil {
			return nil, s.accessError("list", queryPrefix, err)
		}
		for _, object := range out.Contents {
			key := aws.ToString(object.Key)
			rel := strings.TrimPrefix(key, objectPrefix(s.prefix))
			if rel != "" && !strings.HasSuffix(rel, "/") {
				paths = append(paths, rel)
			}
		}
		if !aws.ToBool(out.IsTruncated) || out.NextContinuationToken == nil {
			break
		}
		token = out.NextContinuationToken
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *Store) Write(ctx context.Context, objectPath string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := s.objectName(objectPath, false)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return s.accessError("write", key, err)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, objectPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := s.objectName(objectPath, false)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil && !isNotFound(err) {
		return s.accessError("delete", key, err)
	}
	return nil
}

func (s *Store) ListRefs(ctx context.Context) (map[string]string, error) {
	return store.ReadRefs(ctx, s)
}

func (s *Store) CompareAndSwapRef(ctx context.Context, ref, oldOID, newOID string) error {
	if err := store.ValidateRefUpdate(ref, oldOID, newOID); err != nil {
		return err
	}
	key, err := s.objectName(ref, false)
	if err != nil {
		return err
	}
	current, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	notFound := isNotFound(err)
	if err != nil && !notFound {
		return s.accessError("read ref", key, err)
	}
	currentOID, etag := "", ""
	if !notFound {
		data, readErr := io.ReadAll(current.Body)
		closeErr := current.Body.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		currentOID, etag = strings.TrimSpace(string(data)), aws.ToString(current.ETag)
	}
	if !(store.IsZeroOID(oldOID) && notFound) && currentOID != oldOID {
		return store.ErrConflict
	}
	if store.IsZeroOID(newOID) {
		if notFound {
			return nil
		}
		_, err = s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), IfMatch: aws.String(etag)})
	} else {
		input := &awss3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader([]byte(newOID + "\n"))}
		if notFound {
			input.IfNoneMatch = aws.String("*")
		} else {
			input.IfMatch = aws.String(etag)
		}
		_, err = s.client.PutObject(ctx, input)
	}
	if isConflict(err) {
		return store.ErrConflict
	}
	if err != nil {
		return s.accessError("update ref", key, err)
	}
	return nil
}

func (s *Store) CompareAndSwap(ctx context.Context, objectPath string, expected store.ObjectState, replacement []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := s.objectName(objectPath, false)
	if err != nil {
		return err
	}
	current, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	notFound := isNotFound(err)
	if err != nil && !notFound {
		return s.accessError("read conditional object", key, err)
	}
	var data []byte
	etag := ""
	if !notFound {
		data, err = io.ReadAll(current.Body)
		closeErr := current.Body.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		etag = aws.ToString(current.ETag)
	}
	if (!notFound) != expected.Exists || !notFound && !bytes.Equal(data, expected.Data) {
		return store.ErrConflict
	}
	input := &awss3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(replacement)}
	if notFound {
		input.IfNoneMatch = aws.String("*")
	} else {
		input.IfMatch = aws.String(etag)
	}
	if _, err := s.client.PutObject(ctx, input); isConflict(err) {
		return store.ErrConflict
	} else if err != nil {
		return s.accessError("replace conditional object", key, err)
	}
	return nil
}

func (s *Store) objectName(value string, allowEmpty bool) (string, error) {
	rel, err := store.ValidatePath(value, allowEmpty)
	if err != nil {
		return "", err
	}
	if s.prefix == "" {
		return rel, nil
	}
	if rel == "" {
		return s.prefix, nil
	}
	return s.prefix + "/" + rel, nil
}

func (s *Store) accessError(action, key string, err error) error {
	return fmt.Errorf("%s s3://%s/%s: %w", action, s.bucket, key, err)
}

func objectPrefix(value string) string {
	value = strings.Trim(value, "/")
	if value == "" {
		return ""
	}
	return value + "/"
}

func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchBucket", "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

func isConflict(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "PreconditionFailed" || apiErr.ErrorCode() == "ConditionalRequestConflict"
	}
	return false
}

var _ store.RefStore = (*Store)(nil)
var _ store.CompareAndSwapper = (*Store)(nil)
