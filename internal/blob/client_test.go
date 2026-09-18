package blob

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// These tests require a real MinIO: what they check — the SigV4 signature, ranged
// reads, writing through a presigned URL — only means something against a server
// that implements them.
//
// Without TEST_MINIO_ENDPOINT they are visibly skipped.
func testStorage(t *testing.T) config.StorageConfig {
	t.Helper()

	endpoint := os.Getenv("TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_MINIO_ENDPOINT not set: integration test skipped")
	}
	return config.StorageConfig{
		Endpoint:   endpoint,
		Bucket:     envOr("TEST_MINIO_BUCKET", "arkhitekton-test"),
		AccessKey:  envOr("TEST_MINIO_ACCESS_KEY", "minioadmin"),
		SecretKey:  envOr("TEST_MINIO_SECRET_KEY", "minioadmin"),
		Region:     envOr("TEST_MINIO_REGION", "us-east-1"),
		PresignTTL: 2 * time.Hour,
		UseSSL:     os.Getenv("TEST_MINIO_SSL") == "true",
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func testClient(t *testing.T) *Client {
	t.Helper()

	cfg := testStorage(t)
	ensureBucket(t, cfg)

	client, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ouverture: %v", err)
	}
	return client
}

// ensureBucket creates the test bucket if it is missing.
//
// The harness builds its own fixtures: without that, every execution environment —
// a workstation, a throwaway container, CI — would have to create it its own way,
// and forgetting would show up as a failure hard to tell from a real regression.
func ensureBucket(t *testing.T, cfg config.StorageConfig) {
	t.Helper()

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Region: cfg.Region,
		Secure: cfg.UseSSL,
	})
	if err != nil {
		t.Fatalf("client MinIO: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		t.Fatalf("access to %q on %q: %v", cfg.Bucket, cfg.Endpoint, err)
	}
	if exists {
		return
	}
	if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
		t.Fatalf("creating bucket %q: %v", cfg.Bucket, err)
	}
}

// A missing bucket must fail start-up. Without this check, minio-go opening no
// connection when constructed, the fault would appear at the first artifact upload
// — that is, at the end of a pipeline, after the expensive work.
func TestOpenRejectsMissingBucket(t *testing.T) {
	cfg := testStorage(t)
	cfg.Bucket = "bucket-that-does-not-exist-" + time.Now().Format("20060102150405")

	if _, err := Open(context.Background(), cfg); err == nil {
		t.Fatal("a missing bucket must fail Open")
	}
}

func TestPutGetRemove(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	key, err := client.Keys.RawArtifact("run-test-putget", "sonde.txt")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	content := []byte("probe content")

	if err := client.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = client.Remove(ctx, key) })

	reader, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content = %q, expected %q", got, content)
	}
}

// GetObject is lazy: without Get's Stat, the missing-object error would appear at
// the first read, far from the offending call.
func TestGetReportsMissingObjectImmediately(t *testing.T) {
	client := testClient(t)

	if _, err := client.Get(context.Background(), "raw/missing-run/nothing.txt"); err == nil {
		t.Fatal("a missing object must fail Get")
	}
}

// An integration pipeline's logs weigh tens of megabytes and the interface shows
// only the end: loading the whole object would be ruinous.
func TestGetRangeReadsOnlyRequestedBytes(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	key, err := client.Keys.TaskLog("run-test-range", "task", "step-0")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	content := []byte("0123456789abcdefghij")

	if err := client.Put(ctx, key, bytes.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Cleanup(func() { _ = client.Remove(ctx, key) })

	reader, err := client.GetRange(ctx, key, 10, 5)
	if err != nil {
		t.Fatalf("GetRange: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if string(got) != "abcde" {
		t.Errorf("range read = %q, expected %q", got, "abcde")
	}
}

// The heart of publishing: the pipeline receives a URL and writes holding no
// credential. The test does exactly what the Task does — a PUT on the URL, nothing
// else.
func TestPresignedPutAcceptsUploadWithoutCredentials(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	key, err := client.Keys.RawArtifact("run-test-presign", "junit.xml")
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	presigned, err := client.PresignPut(ctx, key, 10*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	t.Cleanup(func() { _ = client.Remove(ctx, key) })

	// The signature must be carried by the request itself: no authentication header is
	// added here, as in the Task.
	body := "<testsuites/>"
	request, err := newPutRequest(ctx, presigned.String(), body)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	response, err := httpDo(request)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		detail, _ := io.ReadAll(response.Body)
		t.Fatalf("PUT = %d, expected 200: %s", response.StatusCode, detail)
	}

	reader, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	defer reader.Close()

	got, _ := io.ReadAll(reader)
	if string(got) != body {
		t.Errorf("content read back = %q, expected %q", got, body)
	}
}

// The URL must carry a SigV4 signature in its query string: that is the form
// `curl -T` consumes, not a signed POST form.
func TestPresignedURLCarriesSigV4QueryParameters(t *testing.T) {
	client := testClient(t)

	presigned, err := client.PresignPut(context.Background(), "raw/run/objet.txt", time.Hour)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	query := presigned.Query()
	for _, param := range []string{
		"X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date",
		"X-Amz-Expires", "X-Amz-SignedHeaders", "X-Amz-Signature",
	} {
		if query.Get(param) == "" {
			t.Errorf("parameter %s missing from the presigned URL", param)
		}
	}
	if algo := query.Get("X-Amz-Algorithm"); algo != "AWS4-HMAC-SHA256" {
		t.Errorf("algorithm = %q, expected AWS4-HMAC-SHA256", algo)
	}
}

// An expired URL must be refused by the server: that is what bounds the write
// capability handed to the pipeline.
func TestPresignedPutExpires(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	presigned, err := client.PresignPut(ctx, "raw/run-test-expire/objet.txt", time.Second)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	time.Sleep(2 * time.Second)

	request, err := newPutRequest(ctx, presigned.String(), "trop tard")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	response, err := httpDo(request)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode == 200 {
		t.Error("an expired URL was accepted")
	}
}

func TestListReturnsObjectsUnderPrefix(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()

	prefix, err := client.Keys.RunPrefix("run-test-list")
	if err != nil {
		t.Fatalf("prefix: %v", err)
	}

	for _, name := range []string{"a.xml", "b.json"} {
		key, err := client.Keys.RawArtifact("run-test-list", name)
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		content := strings.NewReader("x")
		if err := client.Put(ctx, key, content, 1, "text/plain"); err != nil {
			t.Fatalf("Put: %v", err)
		}
		t.Cleanup(func() { _ = client.Remove(ctx, key) })
	}

	objects, err := client.List(ctx, prefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) != 2 {
		t.Errorf("%d object(s) listed, expected 2: %+v", len(objects), objects)
	}
}
