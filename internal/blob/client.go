package blob

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// Client accesses the dashboard's bucket.
type Client struct {
	minio      *minio.Client
	bucket     string
	presignTTL time.Duration
	Keys       Keys
}

// Open builds the client and checks that the bucket is reachable.
//
// The check is not superfluous: minio-go opens no connection when constructed.
// Without it, a wrong credential or a missing bucket would pass start-up and show
// up at the first artifact upload — that is, at the end of a pipeline, after the
// expensive work is done.
func Open(ctx context.Context, cfg config.StorageConfig) (*Client, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		// The region is not decorative: it enters the SigV4 signature. A wrong region
		// produces a refusal whose message does not mention the region.
		Region: cfg.Region,
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("MinIO client for %q: %w", cfg.Endpoint, err)
	}

	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	exists, err := client.BucketExists(checkCtx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("access to bucket %q on %q: %w", cfg.Bucket, cfg.Endpoint, err)
	}
	if !exists {
		return nil, fmt.Errorf("bucket %q missing on %q", cfg.Bucket, cfg.Endpoint)
	}

	return &Client{
		minio:      client,
		bucket:     cfg.Bucket,
		presignTTL: cfg.PresignTTL,
	}, nil
}

// Ping feeds the /readyz probe.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.minio.BucketExists(ctx, c.bucket)
	return err
}

// PresignPut produces a single-use write URL for a given key.
//
// This is the core of publishing: the pipeline receives the URL as a parameter and
// writes with a plain `curl -T`, holding no store credential and choosing nothing
// about where the artifact lands.
//
// The URL is signed before the artifact exists, hence without knowing its digest:
// minio-go emits UNSIGNED-PAYLOAD, which the target instance accepts. Without that
// tolerance the whole arrangement falls.
//
// The value returned is a write capability: it must never be logged nor exposed
// outside the PipelineRun parameter that carries it.
func (c *Client) PresignPut(ctx context.Context, key string, ttl time.Duration) (*url.URL, error) {
	if ttl <= 0 {
		ttl = c.presignTTL
	}
	if key == "" {
		return nil, fmt.Errorf("empty key")
	}

	presigned, err := c.minio.PresignedPutObject(ctx, c.bucket, key, ttl)
	if err != nil {
		return nil, fmt.Errorf("signing the write URL for %q: %w", key, err)
	}
	return presigned, nil
}

// Put writes an object. Used for copies the dashboard makes itself — an artifact
// attached to a release, a sealed log — where pipelines go through a presigned URL.
func (c *Client) Put(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	_, err := c.minio.PutObject(ctx, c.bucket, key, reader, size,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("writing %q: %w", key, err)
	}
	return nil
}

// Get opens an object for reading. The caller closes it.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	object, err := c.minio.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", key, err)
	}
	// GetObject is lazy: the missing-object error only surfaces at the first read. An
	// immediate Stat raises it to the caller rather than wherever the stream is
	// consumed.
	if _, err := object.Stat(); err != nil {
		_ = object.Close()
		return nil, fmt.Errorf("reading %q: %w", key, err)
	}
	return object, nil
}

// Opened is an object open for reading at any offset: what serving a range needs, a video being
// sought into.
type Opened interface {
	io.ReadSeekCloser
}

// Open opens an object for reading at any offset, with its metadata. The caller closes it.
func (c *Client) Open(ctx context.Context, key string) (Opened, ObjectInfo, error) {
	object, err := c.minio.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("reading %q: %w", key, err)
	}
	info, err := object.Stat()
	if err != nil {
		_ = object.Close()
		return nil, ObjectInfo{}, fmt.Errorf("reading %q: %w", key, err)
	}
	return object, ObjectInfo{Key: info.Key, Size: info.Size, LastModified: info.LastModified}, nil
}

// GetRange reads a byte range.
//
// Indispensable for logs: an integration pipeline produces tens of megabytes, and
// the interface shows only the end before going back on demand. Loading the whole
// object to show the last fifty lines would be ruinous.
func (c *Client) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	options := minio.GetObjectOptions{}
	if err := options.SetRange(offset, offset+length-1); err != nil {
		return nil, fmt.Errorf("invalid range for %q: %w", key, err)
	}

	object, err := c.minio.GetObject(ctx, c.bucket, key, options)
	if err != nil {
		return nil, fmt.Errorf("partial read of %q: %w", key, err)
	}
	return object, nil
}

// ObjectInfo describes a listed object.
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Stat returns an object's metadata, without its content.
//
// It serves to learn the size before a ranged read: showing the end of a
// fifty-megabyte log means knowing where it begins.
func (c *Client) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := c.minio.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("metadata of %q: %w", key, err)
	}
	return ObjectInfo{Key: info.Key, Size: info.Size, LastModified: info.LastModified}, nil
}

// List enumerates the objects under a prefix.
func (c *Client) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var objects []ObjectInfo

	for object := range c.minio.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if object.Err != nil {
			return nil, fmt.Errorf("listing %q: %w", prefix, object.Err)
		}
		objects = append(objects, ObjectInfo{
			Key:          object.Key,
			Size:         object.Size,
			LastModified: object.LastModified,
		})
	}
	return objects, nil
}

// Remove deletes an object.
func (c *Client) Remove(ctx context.Context, key string) error {
	if err := c.minio.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("deleting %q: %w", key, err)
	}
	return nil
}

// Bucket returns the bucket name, for diagnostics.
func (c *Client) Bucket() string { return c.bucket }
