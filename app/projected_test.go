package app

import (
	"context"
	"errors"
	"testing"

	"github.com/Cadoles/arkhitekton/internal/deploykeys"
	"github.com/Cadoles/arkhitekton/internal/store"
)

type recordedSecrets struct {
	data    map[string]map[string][]byte
	mounted []string
}

func (r *recordedSecrets) Put(_ context.Context, kind deploykeys.Kind, uid, _ string, data map[string][]byte) (string, error) {
	name := kind.Name(uid)
	r.data[name] = data
	return name, nil
}

func (r *recordedSecrets) AllowKind(_ context.Context, kind deploykeys.Kind, uid string) error {
	r.mounted = append(r.mounted, kind.Name(uid))
	return nil
}

type slugs map[int64]string

func (s slugs) GetProject(_ context.Context, id int64) (store.Project, error) {
	slug, found := s[id]
	if !found {
		return store.Project{}, store.ErrProjectNotFound
	}
	return store.Project{ID: id, Slug: slug, UID: "uid-" + slug}, nil
}

// The review's key, opened for the project, is written into the project's Secret and made
// mountable; with no key typed, the Secret exists and carries none.
func TestASealedKeyIsWrittenIntoTheProjectsSecret(t *testing.T) {
	recorded := &recordedSecrets{data: map[string]map[string][]byte{}}
	opened := map[int64]string{7: "sk-project", 8: ""}
	projector := settingSecrets{
		resolve: func(_ context.Context, projectID *int64, reference string) (string, error) {
			if reference != "review.apiKey" {
				return "", errors.New("another setting opened")
			}
			return opened[*projectID], nil
		},
		projects: slugs{7: "shop", 8: "blog"}, owner: recorded, mounter: recorded,
	}

	name, err := projector.Project(context.Background(), "project.reviewSecret", 7)
	if err != nil || name != "arkhitekton-project-uid-shop-review" {
		t.Fatalf("%q, %v", name, err)
	}
	if got := string(recorded.data[name]["api-key"]); got != "sk-project" {
		t.Errorf("api-key = %q", got)
	}
	if len(recorded.mounted) != 1 || recorded.mounted[0] != name {
		t.Errorf("mounted = %v", recorded.mounted)
	}

	name, err = projector.Project(context.Background(), "project.reviewSecret", 8)
	if err != nil || len(recorded.data[name]) != 0 {
		t.Errorf("no key typed: %v, %v", recorded.data[name], err)
	}

	if _, err := projector.Project(context.Background(), "project.otherSecret", 7); err == nil {
		t.Error("a key no setting feeds was written")
	}
}
