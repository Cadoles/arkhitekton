package parse

// PHPStanJSON parses PHPStan's JSON output into a [PHPStan].
const PHPStanJSON ID = "phpstan-json"

// PHPStan is PHPStan's report.
type PHPStan struct {
	// Errors are outside any file: unreadable configuration, a missing extension. Failures of the
	// tool, not of the code.
	Errors []string
	// Files are sorted by path, so that two reads of one report give one order.
	Files []PHPStanFile
}

// PHPStanFile is one analysed file and its messages.
type PHPStanFile struct {
	Path     string
	Messages []PHPStanMessage
}

// PHPStanMessage is one violation.
type PHPStanMessage struct {
	Message string
	Line    int
	// Identifier is the rule; older versions emit none.
	Identifier string
	Ignorable  bool
}

// CSFixerJSON parses PHP-CS-Fixer's JSON output into a [CSFixer].
const CSFixerJSON ID = "cs-fixer-json"

// CSFixer is PHP-CS-Fixer's report. In a dry run, every file listed is one that would have been
// modified.
type CSFixer struct {
	Files []CSFixerFile
}

// CSFixerFile is a file and the fixers that would change it.
type CSFixerFile struct {
	Name          string
	AppliedFixers []string
	Diff          string
}
