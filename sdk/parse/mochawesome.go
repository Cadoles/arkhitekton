package parse

/*
 * Mochawesome JSON, Cypress's detailed report.
 *
 * A test that passed after a retry carries `currentRetry > 0` and `pass: true`. The record keeps
 * both; reading it as a plain success would erase the campaign's only flakiness signal.
 */

// MochawesomeJSON parses a mochawesome report into a [Mochawesome].
const MochawesomeJSON ID = "mochawesome-json"

// Mochawesome is a mochawesome report.
type Mochawesome struct {
	DurationMS int64
	Tests      []MochaTest
}

// MochaTest is one test with the blocks enclosing it.
type MochaTest struct {
	// File is the spec's path, from the result the test belongs to.
	File string
	// Suites are the enclosing `describe` blocks, outermost first.
	Suites       []MochaSuite
	Title        string
	FullTitle    string
	DurationMS   int
	Pass         bool
	Fail         bool
	Pending      bool
	CurrentRetry int
	ErrMessage   string
	ErrStack     string
}

// MochaSuite is a `describe` block.
type MochaSuite struct {
	Title string
	File  string
}

// Suite is the usual identity of a test's suite: the spec file, extended by the blocks' titles. A
// block's own file wins over the enclosing path: renaming a `describe` must not make all its tests
// new.
func (t MochaTest) Suite() string {
	name := t.File
	for _, suite := range t.Suites {
		next := suite.File
		if next == "" {
			next = name
		}
		if suite.Title != "" && next != "" {
			next = next + " / " + suite.Title
		}
		name = next
	}
	return name
}

// Name is the test's full title, else its title.
func (t MochaTest) Name() string {
	if t.FullTitle != "" {
		return t.FullTitle
	}
	return t.Title
}
