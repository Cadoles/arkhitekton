package release

import (
	"reflect"
	"testing"
)

// The convention as it is written in pull request titles and commit messages.
func TestIssueNumbersAreReadFromTheText(t *testing.T) {
	cases := map[string][]string{
		"#1234 fixes pagination":            {"1234"},
		"Fixes pagination (#1234)":          {"1234"},
		"fix: pagination\n\nRefs #1234 et #5678": {"1234", "5678"},
		"no number here":                       nil,
		// The same number cited twice names one issue.
		"#42 puis encore #42": {"42"},
	}

	for text, expected := range cases {
		if got := ExtractIssueRefs(text); !reflect.DeepEqual(got, expected) {
			t.Errorf("ExtractIssueRefs(%q) = %v, expected %v", text, got, expected)
		}
	}
}

// What the pattern must refuse, and the heart of the matter: a false release note is
// worse than an empty one, because it reads like a checked list.
func TestWhatLooksLikeANumberWithoutBeingOneIsRefused(t *testing.T) {
	refuses := []string{
		// A Markdown anchor, not an issue.
		"## 1234 Section heading",
		"##1234",
		// A hash glued to a word: an identifier for something else.
		"commit abc#1234",
		"branche feature#99",
		// A number followed by letters is not a number.
		"#1234abc",
		// Zero is not a tracker identifier.
		"#0",
	}

	for _, text := range refuses {
		if got := ExtractIssueRefs(text); len(got) != 0 {
			t.Errorf("ExtractIssueRefs(%q) = %v, expected none", text, got)
		}
	}
}

// An outsized number is not an issue: converting it would overflow, and looking it
// up would return nothing while giving the illusion of a link.
func TestAnOutsizedNumberIsIgnored(t *testing.T) {
	if got := ExtractIssueRefs("#123456789012345"); len(got) != 0 {
		t.Errorf("ExtractIssueRefs = %v, expected none", got)
	}
}

// Title and commits are read together: the convention allows either, and a pull
// request citing its issue in a single commit must count.
func TestTitleAndCommitsAreReadTogether(t *testing.T) {
	got := ExtractIssueRefs(
		"Fixes pagination",
		"wip",
		"fix(pagination): borne l'offset\n\nRefs #1234",
	)
	if !reflect.DeepEqual(got, []string{"1234"}) {
		t.Errorf("ExtractIssueRefs = %v, expected [1234]", got)
	}
}

// The order is the writing order: the one in which the author listed their issues,
// and sorting alphabetically or numerically would lose that intent for nothing.
func TestTheOrderOfAppearanceIsKept(t *testing.T) {
	got := ExtractIssueRefs("Refs #99, #12, #55")
	if !reflect.DeepEqual(got, []string{"99", "12", "55"}) {
		t.Errorf("ExtractIssueRefs = %v, expected [99 12 55]", got)
	}
}
