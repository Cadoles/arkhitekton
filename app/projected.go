package app

import (
	"context"
	"fmt"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/deploykeys"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The Secrets a project's sealed settings feed.
 *
 * A key typed into a plugin's settings is sealed in the database, and serves a pod: as a
 * parameter it would land in the PipelineRun's spec. The worker opens it right before a run that
 * mounts it, and writes it into a Secret of the project's, which the run binds by a configuration
 * key. Written again at each run, the Secret carries what the screen holds at that moment.
 */

// projectedSecret is one Secret a sealed setting feeds.
type projectedSecret struct {
	// key is the configuration key a binding names.
	key string
	// reference is the sealed setting, `plugin.field`, the project's over the instance's.
	reference string
	kind      deploykeys.Kind
	// dataKey is where the value goes in the Secret. An empty value leaves it out: the Secret still
	// exists, for a workspace to mount it, and says that no key was typed.
	dataKey string
}

// projectedSecrets are the Secrets written from settings.
var projectedSecrets = []projectedSecret{
	{key: "project.reviewSecret", reference: "review.apiKey", kind: deploykeys.KindReview, dataKey: "api-key"},
}

type secretWriter interface {
	Put(ctx context.Context, kind deploykeys.Kind, uid, slug string, data map[string][]byte) (string, error)
}

type secretMounter interface {
	AllowKind(ctx context.Context, kind deploykeys.Kind, uid string) error
}

// settingSecrets writes them, through the same owner and mounter as the deploy keys.
type settingSecrets struct {
	resolve  func(ctx context.Context, projectID *int64, reference string) (string, error)
	projects interface {
		GetProject(ctx context.Context, id int64) (store.Project, error)
	}
	owner   secretWriter
	mounter secretMounter
}

func (s settingSecrets) Keys() []string {
	keys := make([]string, 0, len(projectedSecrets))
	for _, projected := range projectedSecrets {
		keys = append(keys, projected.key)
	}
	return keys
}

func (s settingSecrets) Project(ctx context.Context, key string, projectID int64) (string, error) {
	for _, projected := range projectedSecrets {
		if projected.key != key {
			continue
		}
		project, err := s.projects.GetProject(ctx, projectID)
		if err != nil {
			return "", err
		}
		value, err := s.resolve(ctx, &projectID, projected.reference)
		if err != nil {
			// The value is never quoted: it is the one that could not be opened.
			return "", fmt.Errorf("opening %s: %w", projected.reference, err)
		}
		data := map[string][]byte{}
		if value != "" {
			data[projected.dataKey] = []byte(value)
		}
		// Named from the project's id: a slug renamed or reused would point at another project's.
		name, err := s.owner.Put(ctx, projected.kind, project.UID, project.Slug, data)
		if err != nil {
			return "", err
		}
		if err := s.mounter.AllowKind(ctx, projected.kind, project.UID); err != nil {
			return "", err
		}
		return name, nil
	}
	return "", fmt.Errorf("no setting feeds the secret %s", key)
}
