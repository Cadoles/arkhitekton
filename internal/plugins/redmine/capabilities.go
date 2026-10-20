package redmine

import (
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

// tracker connects the dashboard to a Redmine instance.
type tracker struct{}

// Connect builds a read-only client from the project's resolved settings.
//
// A missing setting is named rather than left to the client: "invalid URL" would send someone
// hunting for a typo where it is a setting that was never filled in.
func (tracker) Connect(target capability.Target) (provider.Tracker, error) {
	url := strings.TrimSpace(target.Settings["url"])
	key := strings.TrimSpace(target.Settings["api_key"])
	if url == "" || key == "" {
		return nil, fmt.Errorf("tracker not configured: set the URL and the API key of plugin %q, "+
			"on the instance or on the project", ID)
	}
	return New(Options{URL: url, APIKey: key, Timeout: target.Timeout})
}

func (tracker) VersionURL(settings capability.Settings, id string) string {
	return link(settings, "/versions/", id)
}

func (tracker) IssueURL(settings capability.Settings, id string) string {
	return link(settings, "/issues/", id)
}

func link(settings capability.Settings, path, id string) string {
	base := strings.TrimRight(strings.TrimSpace(settings["url"]), "/")
	if base == "" || id == "" {
		return ""
	}
	return base + path + id
}
