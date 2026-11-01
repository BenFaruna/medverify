// Package storage wraps the S3-compatible object store behind a small
// interface. Scans, leaflet PDFs, and other artefacts live here; the rest of
// the code never talks to the provider directly. Locally this is a SeaweedFS S3
// gateway; production uses any S3-compatible service.
//
// Phase 1 only needs connectivity, but the interface is where later phases add
// Put/Get. Keep the surface small until a phase needs more.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ErrBucketMissing is returned when the configured bucket does not exist.
var ErrBucketMissing = errors.New("storage bucket does not exist")

// PutOptions describes one object write.
type PutOptions struct {
	// ContentType is stored with the object. Empty means application/octet-stream.
	ContentType string
	// Size is the exact number of bytes in the reader. Use -1 when unknown.
	Size int64
}

// Store is the object storage contract.
type Store interface {
	// Ping reports whether the object store is reachable and the bucket exists.
	Ping(ctx context.Context) error
	// EnsureBucket creates the bucket when it is missing. Safe to call repeatedly.
	EnsureBucket(ctx context.Context) error
	// Put streams an object under key. The reader is consumed once.
	Put(ctx context.Context, key string, r io.Reader, opts PutOptions) error
}

// Options configures the S3-compatible store.
type Options struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
}

// S3 is an S3-compatible implementation of Store, built on the MinIO Go client.
type S3 struct {
	client *minio.Client
	bucket string
}

// NewS3 builds a client. It does not make a network call.
func NewS3(opts Options) (*S3, error) {
	client, err := minio.New(opts.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(opts.AccessKey, opts.SecretKey, ""),
		Secure: opts.UseSSL,
		Region: opts.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create object storage client: %w", err)
	}
	return &S3{client: client, bucket: opts.Bucket}, nil
}

// Ping checks that the bucket exists.
func (s *S3) Ping(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %q: %w", s.bucket, err)
	}
	if !exists {
		return fmt.Errorf("%w: %s", ErrBucketMissing, s.bucket)
	}
	return nil
}

// EnsureBucket creates the bucket when it is missing.
func (s *S3) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %q: %w", s.bucket, err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: ""}); err != nil {
		return fmt.Errorf("create bucket %q: %w", s.bucket, err)
	}
	return nil
}

// Put streams an object into the configured bucket.
func (s *S3) Put(ctx context.Context, key string, r io.Reader, opts PutOptions) error {
	contentType := opts.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if opts.Size < 0 {
		// MinIO needs a size; buffer only when the caller cannot provide one.
		data, err := io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("read object %q: %w", key, err)
		}
		_, err = s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: contentType})
		if err != nil {
			return fmt.Errorf("put object %q: %w", key, err)
		}
		return nil
	}
	if _, err := s.client.PutObject(ctx, s.bucket, key, r, opts.Size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return fmt.Errorf("put object %q: %w", key, err)
	}
	return nil
}

// Fake is an in-memory Store for unit tests.
type Fake struct {
	// Err, when set, is returned by Ping and EnsureBucket.
	Err error
	// BucketExists controls the Ping result when Err is nil.
	BucketExists bool
	// Ensured records that EnsureBucket was called.
	Ensured bool
	// Objects holds everything written through Put.
	Objects map[string][]byte
}

// Ping implements Store.
func (f *Fake) Ping(context.Context) error {
	if f.Err != nil {
		return f.Err
	}
	if !f.BucketExists {
		return ErrBucketMissing
	}
	return nil
}

// EnsureBucket implements Store.
func (f *Fake) EnsureBucket(context.Context) error {
	if f.Err != nil {
		return f.Err
	}
	f.Ensured = true
	f.BucketExists = true
	return nil
}

// Put implements Store.
func (f *Fake) Put(_ context.Context, key string, r io.Reader, _ PutOptions) error {
	if f.Err != nil {
		return f.Err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if f.Objects == nil {
		f.Objects = make(map[string][]byte)
	}
	f.Objects[key] = data
	return nil
}

var _ Store = (*S3)(nil)
var _ Store = (*Fake)(nil)
