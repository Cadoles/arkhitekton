package blob

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// newPutRequest builds a PUT on a presigned URL, with no authentication header: the
// signature is carried by the URL, and that is precisely what the test must
// exercise.
func newPutRequest(ctx context.Context, presignedURL, body string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, strings.NewReader(body))
}

func httpDo(request *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(request)
}
