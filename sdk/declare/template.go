package declare

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

/*
 * The template language: literal text and `{field | filter | ...}` expressions.
 *
 * Closed on purpose. An expression reads one field of a row and passes it through filters; there
 * is no loop, no condition and no call. A need that does not fit belongs to an interpreter written
 * in code, not to a new keyword here.
 *
 *   firstline            the first non-empty line, trimmed
 *   trim                 surrounding spaces removed
 *   lower                lower case
 *   or: <field|'text'>   the value, or this field or text when it is empty
 *   replace: 're' 'to'   every match of the regular expression replaced
 *
 * A display template, the one a screen declares (sdk/ui), reads a dotted path in the data the
 * browser loaded rather than a field of a row, and renders there. It knows the filters above but
 * `replace` (a regular expression would not read the same in Go and in the browser), plus:
 *
 *   upper                upper case
 *   date, datetime       an RFC 3339 instant, as a date or a date and time
 *   age                  an RFC 3339 instant, as the time elapsed since
 *   bytes                a size, in a human unit
 *   count                the number of elements of a list
 *   join: 'sep'          the elements of a list, separated (`, ` by default)
 */

// Template is a parsed template.
type Template struct {
	source string
	parts  []part
}

type part struct {
	literal string
	field   string
	filters []filter
}

type filter struct {
	name string
	// args are field names, or literals when quoted.
	args    []argument
	pattern *regexp.Regexp
}

type argument struct {
	value   string
	literal bool
}

var fieldName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var dataPath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)*$`)

// grammar is what a kind of template may name: its fields, and the filters it knows with how many
// arguments each takes.
type grammar struct {
	field   *regexp.Regexp
	filters map[string][2]int
}

var (
	readingGrammar = grammar{field: fieldName, filters: map[string][2]int{
		"firstline": {0, 0}, "trim": {0, 0}, "lower": {0, 0}, "or": {1, 1}, "replace": {2, 2},
	}}
	displayGrammar = grammar{field: dataPath, filters: map[string][2]int{
		"firstline": {0, 0}, "trim": {0, 0}, "lower": {0, 0}, "or": {1, 1}, "upper": {0, 0},
		"date": {0, 0}, "datetime": {0, 0}, "age": {0, 0}, "bytes": {0, 0}, "count": {0, 0},
		"join": {0, 1},
	}}
)

// ParseTemplate reads a template of a run reading.
func ParseTemplate(source string) (Template, error) {
	return parseTemplate(source, readingGrammar)
}

// ParseDisplayTemplate reads a template of a screen.
func ParseDisplayTemplate(source string) (Template, error) {
	return parseTemplate(source, displayGrammar)
}

func parseTemplate(source string, grammar grammar) (Template, error) {
	template := Template{source: source}
	rest := source
	for rest != "" {
		open := strings.IndexByte(rest, '{')
		closing := strings.IndexByte(rest, '}')
		if closing >= 0 && (open < 0 || closing < open) {
			return Template{}, fmt.Errorf("template %q: a `}` closes nothing", source)
		}
		if open < 0 {
			template.parts = append(template.parts, part{literal: rest})
			break
		}
		if open > 0 {
			template.parts = append(template.parts, part{literal: rest[:open]})
		}
		end := strings.IndexByte(rest[open:], '}')
		if end < 0 {
			return Template{}, fmt.Errorf("template %q: a `{` is not closed", source)
		}
		expression, err := parseExpression(rest[open+1:open+end], grammar)
		if err != nil {
			return Template{}, fmt.Errorf("template %q: %w", source, err)
		}
		template.parts = append(template.parts, expression)
		rest = rest[open+end+1:]
	}
	return template, nil
}

func parseExpression(source string, grammar grammar) (part, error) {
	segments := splitOutsideQuotes(source, '|')
	field := strings.TrimSpace(segments[0])
	if !grammar.field.MatchString(field) {
		return part{}, fmt.Errorf("%q is not a field name", field)
	}
	expression := part{field: field}
	for _, segment := range segments[1:] {
		name, rawArgs, _ := strings.Cut(strings.TrimSpace(segment), ":")
		name = strings.TrimSpace(name)
		args, err := parseArguments(rawArgs)
		if err != nil {
			return part{}, err
		}
		parsed := filter{name: name, args: args}
		arity, known := grammar.filters[name]
		if !known {
			return part{}, fmt.Errorf("unknown filter %q", name)
		}
		switch {
		case name == "or":
			if len(args) != 1 {
				return part{}, errors.New("filter or takes one field or one 'text'")
			}
			if !args[0].literal && !grammar.field.MatchString(args[0].value) {
				return part{}, fmt.Errorf("%q is not a field name", args[0].value)
			}
		case name == "replace":
			if len(args) != 2 || !args[0].literal || !args[1].literal {
				return part{}, errors.New("filter replace takes 'expression' 'replacement'")
			}
			pattern, err := regexp.Compile(args[0].value)
			if err != nil {
				return part{}, fmt.Errorf("filter replace: %w", err)
			}
			parsed.pattern = pattern
		case arity[1] == 0 && len(args) != 0:
			return part{}, fmt.Errorf("filter %s takes no argument", name)
		case len(args) > arity[1] || slices.ContainsFunc(args, func(arg argument) bool { return !arg.literal }):
			return part{}, fmt.Errorf("filter %s takes at most %d 'text'", name, arity[1])
		}
		expression.filters = append(expression.filters, parsed)
	}
	return expression, nil
}

// parseArguments reads space-separated arguments, single-quoted ones being literals.
func parseArguments(source string) ([]argument, error) {
	var args []argument
	rest := strings.TrimSpace(source)
	for rest != "" {
		if rest[0] == '\'' {
			end := strings.IndexByte(rest[1:], '\'')
			if end < 0 {
				return nil, errors.New("a quote is not closed")
			}
			args = append(args, argument{value: rest[1 : end+1], literal: true})
			rest = strings.TrimSpace(rest[end+2:])
			continue
		}
		word, after, _ := strings.Cut(rest, " ")
		args = append(args, argument{value: word})
		rest = strings.TrimSpace(after)
	}
	return args, nil
}

func splitOutsideQuotes(source string, separator byte) []string {
	var segments []string
	quoted := false
	start := 0
	for i := 0; i < len(source); i++ {
		switch {
		case source[i] == '\'':
			quoted = !quoted
		case source[i] == separator && !quoted:
			segments = append(segments, source[start:i])
			start = i + 1
		}
	}
	return append(segments, source[start:])
}

// Fields lists the fields the template reads, filters' arguments included.
func (t Template) Fields() []string {
	var fields []string
	for _, part := range t.parts {
		if part.field == "" {
			continue
		}
		fields = append(fields, part.field)
		for _, filter := range part.filters {
			for _, arg := range filter.args {
				if filter.name == "or" && !arg.literal {
					fields = append(fields, arg.value)
				}
			}
		}
	}
	return fields
}

// Render fills the template from a row. A field the row lacks reads empty.
func (t Template) Render(row map[string]string) string {
	var out strings.Builder
	for _, part := range t.parts {
		if part.field == "" {
			out.WriteString(part.literal)
			continue
		}
		value := row[part.field]
		for _, filter := range part.filters {
			value = filter.apply(value, row)
		}
		out.WriteString(value)
	}
	return out.String()
}

func (f filter) apply(value string, row map[string]string) string {
	switch f.name {
	case "firstline":
		for line := range strings.SplitSeq(value, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return trimmed
			}
		}
		return ""
	case "trim":
		return strings.TrimSpace(value)
	case "lower":
		return strings.ToLower(value)
	case "or":
		if value != "" {
			return value
		}
		if f.args[0].literal {
			return f.args[0].value
		}
		return row[f.args[0].value]
	case "replace":
		return f.pattern.ReplaceAllString(value, f.args[1].value)
	}
	return value
}

func (t Template) String() string { return t.source }

// MarshalText and UnmarshalText make a template a plain string in a declaration.
func (t Template) MarshalText() ([]byte, error) { return []byte(t.source), nil }

func (t *Template) UnmarshalText(text []byte) error {
	parsed, err := ParseTemplate(string(text))
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}
