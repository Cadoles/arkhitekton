package config

import "testing"

// Naming a repository in the configuration must be enough for the pipelines to
// build it. This is the property that was missing: FORGE_REPO could be switched to
// a test repository while the Tasks kept cloning the real application.
func TestCloneURLFollowsTheConfiguredRepository(t *testing.T) {
	cases := []struct {
		url, repo, port, want string
	}{
		{
			"https://forge.cadoles.com", "vcarroy/mse-test-project", "",
			"ssh://git@forge.cadoles.com:2222/vcarroy/mse-test-project.git",
		},
		{
			"https://forge.cadoles.com", "CNOUS/mse", "",
			"ssh://git@forge.cadoles.com:2222/CNOUS/mse.git",
		},
		// A trailing slash and a `.git` suffix are tolerated: they come naturally
		// when copying a repository URL, and refusing them would produce a double
		// `.git` that the clone rejects without explaining.
		{
			"https://forge.cadoles.com/", "vcarroy/mse-test-project.git", "",
			"ssh://git@forge.cadoles.com:2222/vcarroy/mse-test-project.git",
		},
		// The port is configurable: another forge need not expose it on 2222.
		{
			"https://git.example.org", "equipe/appli", "22",
			"ssh://git@git.example.org:22/equipe/appli.git",
		},
	}

	for _, c := range cases {
		cfg := ForgeConfig{URL: c.url, Token: "x", Repository: c.repo}
		if got := cfg.CloneURL(c.port); got != c.want {
			t.Errorf("CloneURL(%q, %q, port=%q) = %q, expected %q",
				c.url, c.repo, c.port, got, c.want)
		}
	}
}

// With no forge configured, derivation returns an empty string rather than a
// malformed URL: the caller falls back to its default, and a deployment that never
// set the forge keeps working.
func TestWithoutForgeTheDerivationAbstains(t *testing.T) {
	for _, cfg := range []ForgeConfig{
		{},
		{URL: "https://forge.cadoles.com"},
		{Token: "x", Repository: "a/b"},
		{URL: "://broken", Token: "x", Repository: "a/b"},
	} {
		if got := cfg.CloneURL(""); got != "" {
			t.Errorf("%+v: CloneURL returns %q, expected empty", cfg, got)
		}
	}
}

// The inverse: pipelines clone repositories written as SSH URLs, while the forge
// API and the projects screen name them `owner/name`.
func TestTheRepositoryIsFoundFromItsCloneURL(t *testing.T) {
	cases := []struct {
		clone      string
		repository string
		forge      string
	}{
		{"ssh://git@forge.cadoles.com:2222/CNOUS/mse.git", "CNOUS/mse", "https://forge.cadoles.com"},
		{"ssh://git@forge.cadoles.com:2222/vcarroy/mse-test-project.git",
			"vcarroy/mse-test-project", "https://forge.cadoles.com"},
		// The short form Git also accepts: its colon separates host from path, not
		// a port, and net/url gets it wrong.
		{"git@forge.cadoles.com:CNOUS/mse.git", "CNOUS/mse", "https://forge.cadoles.com"},
		{"https://forge.cadoles.com/CNOUS/mse", "CNOUS/mse", "https://forge.cadoles.com"},
		// Dropped rather than truncated: fabricating a repository would give an
		// access check that fails without saying the input was wrong.
		{"ssh://git@forge.cadoles.com:2222/mse.git", "", ""},
		{"ssh://git@forge.cadoles.com:2222/a/b/c.git", "", ""},
		{"", "", ""},
	}

	for _, tc := range cases {
		repository, forge := RepositoryFromCloneURL(tc.clone)
		if repository != tc.repository || forge != tc.forge {
			t.Errorf("%q → %q / %q, expected %q / %q",
				tc.clone, repository, forge, tc.repository, tc.forge)
		}
	}
}

// A project's repository gives its clone URL: that is where what its pipelines
// clone comes from, not an instance-wide key.
func TestCloneURLForFollowsAProjectsRepository(t *testing.T) {
	got := CloneURLFor("https://forge.example.fr", "vcarroy/mse-kustom", "2222")
	if got != "ssh://git@forge.example.fr:2222/vcarroy/mse-kustom.git" {
		t.Errorf("URL = %q", got)
	}

	// The SSH port cannot be derived from the HTTPS URL: without it, the constant.
	if got := CloneURLFor("https://forge.example.fr", "vcarroy/mse.git", ""); got !=
		"ssh://git@forge.example.fr:"+DefaultForgeSSHPort+"/vcarroy/mse.git" {
		t.Errorf("URL with no port = %q", got)
	}

	// A project declared before its forge was set yields nothing: the caller then
	// falls back to the instance repository.
	for _, testCases := range [][2]string{{"", "vcarroy/mse"}, {"https://forge.example.fr", "  "}} {
		if got := CloneURLFor(testCases[0], testCases[1], "2222"); got != "" {
			t.Errorf("forge %q repository %q: %q, expected no URL", testCases[0], testCases[1], got)
		}
	}
}
