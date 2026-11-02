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
	"sync"

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
	// Get reads an object. Callers own the returned bytes.
	Get(ctx context.Context, key string) ([]byte, error)
	// Delete removes an object. Deleting a missing object is not an error.
	Delete(ctx context.Context, key string) error
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

// Get reads an object from the configured bucket.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object %q: %w", key, err)
	}
	defer func() { _ = object.Close() }()

	data, err := io.ReadAll(object)
	if err != nil {
		return nil, fmt.Errorf("read object %q: %w", key, err)
	}
	return data, nil
}

// Delete removes an object. A missing object is treated as success so cleanup
// can be retried safely.
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete object %q: %w", key, err)
	}
	return nil
}

// Fake is an in-memory Store for unit tests.
type Fake struct {
	mu sync.Mutex

	// Err, when set, is returned by every method.
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
	f.mu.Lock()
	defer f.mu.Unlock()
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
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.Ensured = true
	f.BucketExists = true
	return nil
}

// Put implements Store.
func (f *Fake) Put(_ context.Context, key string, r io.Reader, _ PutOptions) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	if f.Objects == nil {
		f.Objects = make(map[string][]byte)
	}
	f.Objects[key] = data
	return nil
}

// Get implements Store.
func (f *Fake) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	data, ok := f.Objects[key]
	if !ok {
		return nil, fmt.Errorf("object %q not found", key)
	}
	return append([]byte(nil), data...), nil
}

// Delete implements Store.
func (f *Fake) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	delete(f.Objects, key)
	return nil
}

var _ Store = (*S3)(nil)
var _ Store = (*Fake)(nil)
