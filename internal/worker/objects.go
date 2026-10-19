package worker

import (
	"context"
	"io"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/blob"
)

// maxArtifactBytes bounds what a report may weigh once decompressed.
//
// An archive published by a pipeline is produced in the cluster and is not hostile, but a JUnit
// report of several gigabytes — a test loop gone wild — would bring the worker to its knees
// instead of reporting it. The bound turns an out-of-memory failure into a message.
const maxArtifactBytes = 64 << 20

// Objects is what the ingester needs from storage.
type Objects interface {
	List(ctx context.Context, prefix string) ([]ObjectRef, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// ObjectRef is the bare minimum needed to find a summary again.
type ObjectRef struct {
	Key  string
	Size int64
}

// objectStore adapts the MinIO client to what the ingester needs.
//
// The adapter exists so that the ingester stays testable without MinIO: what it needs fits in
// two methods, and requiring them on the whole client would force twelve to be simulated.
type objectStore struct {
	client interface {
		List(ctx context.Context, prefix string) ([]blob.ObjectInfo, error)
		Get(ctx context.Context, key string) (io.ReadCloser, error)
	}
}

// NewObjectStore returns the adapter the ingester consumes.
func NewObjectStore(client *blob.Client) Objects { return objectStore{client: client} }

func (o objectStore) List(ctx context.Context, prefix string) ([]ObjectRef, error) {
	objects, err := o.client.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	refs := make([]ObjectRef, 0, len(objects))
	for _, object := range objects {
		refs = append(refs, ObjectRef{Key: object.Key, Size: object.Size})
	}
	return refs, nil
}

func (o objectStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return o.client.Get(ctx, key)
}
