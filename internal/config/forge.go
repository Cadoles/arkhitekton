package config

import (
	"fmt"
	"net/url"
	"strings"
)

/*
 * Deriving the clone URL from the forge configuration.
 *
 * The application's repository is named in two places that must agree: FORGE_REPO,
 * which the dashboard queries to offer branches, and the clone URL the Tasks
 * receive as `repo-url`. Keeping them separate let one be switched without the
 * other — pointing the form at a test repository while the pipelines kept building
 * the real application, with nothing to say so.
 */

// DefaultForgeSSHPort is the forge's SSH port. It is not derivable from an HTTPS
// forge URL, hence the constant.
const DefaultForgeSSHPort = "2222"

// CloneURL returns the SSH clone URL of the instance's repository, built from the
// host of FORGE_URL and the path of FORGE_REPO, so that naming a repository in the
// configuration is enough for the pipelines to build it.
//
// Empty when the forge is not configured: the caller then falls back to its own
// default, which keeps a deployment that never set the forge working.
func (c ForgeConfig) CloneURL(sshPort string) string {
	if !c.Configured() {
		return ""
	}
	return CloneURLFor(c.URL, c.Repository, sshPort)
}

// CloneURLFor returns the SSH clone URL of a given repository.
//
// Separate from [ForgeConfig.CloneURL] because each project carries its own forge
// and repository in the database: that is where what its pipelines clone comes
// from, not an instance-wide configuration key.
//
// Empty when either is missing: the caller then falls back to the instance value,
// which keeps a project declared before its forge was set working.
func CloneURLFor(forgeURL, repository, sshPort string) string {
	repo := strings.TrimSuffix(strings.Trim(strings.TrimSpace(repository), "/"), ".git")
	if strings.TrimSpace(forgeURL) == "" || repo == "" {
		return ""
	}
	if sshPort == "" {
		sshPort = DefaultForgeSSHPort
	}

	parsed, err := url.Parse(forgeURL)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}

	return fmt.Sprintf("ssh://git@%s:%s/%s.git", parsed.Hostname(), sshPort, repo)
}

// RepositoryFromCloneURL recovers `owner/name` and the forge from a clone URL.
//
// The inverse of CloneURL, serving the same concern: pipelines clone repositories
// written as SSH URLs, while the forge API and the projects screen name them
// `owner/name`. Retyping them by hand would let the two forms diverge — declaring
// one repository while cloning another.
//
// Both strings are empty when the URL carries no repository: the caller then drops
// the entry rather than declaring one that points at nothing.
func RepositoryFromCloneURL(clone string) (repository, forgeURL string) {
	clone = strings.TrimSpace(clone)
	if clone == "" {
		return "", ""
	}

	// Two forms circulate: `ssh://git@host:2222/owner/name.git`, which the
	// configuration uses, and `git@host:owner/name.git`, which Git also accepts.
	// The second is not a URL as far as net/url is concerned — its colon separates
	// host from path, not a port — hence the separate handling.
	if !strings.Contains(clone, "://") {
		_, rest, found := strings.Cut(clone, "@")
		if !found {
			return "", ""
		}
		host, path, found := strings.Cut(rest, ":")
		if !found {
			return "", ""
		}
		return normaliseRepository(path), "https://" + host
	}

	parsed, err := url.Parse(clone)
	if err != nil || parsed.Hostname() == "" {
		return "", ""
	}

	repository = normaliseRepository(parsed.Path)
	if repository == "" {
		return "", ""
	}
	return repository, "https://" + parsed.Hostname()
}

// normaliseRepository reduces a clone path to `owner/name`. A deeper path is
// dropped rather than truncated: fabricating one would give an access check that
// fails without saying the input was wrong.
func normaliseRepository(path string) string {
	trimmed := strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	owner, name, found := strings.Cut(trimmed, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return owner + "/" + name
}
