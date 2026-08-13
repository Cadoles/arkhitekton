package blob

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// newPutRequest construit un PUT sur une URL pré-signée, sans aucun en-tête
// d'authentification : la signature est portée par l'URL, et c'est précisément
// ce que le test doit exercer.
func newPutRequest(ctx context.Context, presignedURL, body string) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, strings.NewReader(body))
}

func httpDo(request *http.Request) (*http.Response, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(request)
}
