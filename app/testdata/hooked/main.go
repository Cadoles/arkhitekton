// A native plugin answering every extension hook, for the dispatchers' tests.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc/extension"
)

func main() {
	extension.Serve(func(context.Context, extension.Runtime) (extension.Hooks, error) {
		return extension.Hooks{
			Deployments: func(context.Context, int64) (map[string][]string, error) {
				return map[string][]string{"1.0.0": {"staging"}}, nil
			},
			Promotions: func(_ context.Context, _, releaseID int64) ([]extension.Promotion, error) {
				return []extension.Promotion{{Environment: "staging", Tag: fmt.Sprintf("r%d", releaseID), PushedAt: time.Unix(0, 0)}}, nil
			},
			EnvironmentSuggestions: func(_ context.Context, slug string) []extension.Suggestion {
				return []extension.Suggestion{{Value: "staging", Label: "Staging de " + slug}}
			},
			CheckScheme: func(_ context.Context, scheme string) error {
				if scheme == "opaque" {
					return errors.New("an opaque version cannot be routed")
				}
				return nil
			},
			Promoted: func(at time.Time, linked extension.JobReleaseContext) (extension.Event, bool) {
				return extension.Event{Type: "release.promoted", Subject: linked.Tag, OccurredAt: at,
					Payload: map[string]string{"environment": linked.Environment}}, true
			},
			ProjectSettings: func(context.Context, int64) (map[string]string, error) {
				return map[string]string{"deploy.repoUrl": "ssh://deploy"}, nil
			},
			Module: func(ctx context.Context, state extension.ModuleState) error {
				if len(state.Projects) == 0 {
					return errors.New("a module started for no project")
				}
				<-ctx.Done()
				return ctx.Err()
			},
		}, nil
	})
}
