// Package schedule carries the declaration "at these times, run this plan".
//
// It knows a schedule's shape, reads its cron expression and says when it next fires. What its
// plan's steps point at is checked when saving, by the layer that has the catalogue and the
// registry at hand, as for a reaction.
package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	// The image carries no zone database: a schedule written for Europe/Paris must not fire at the
	// UTC hour because a file was missing.
	_ "time/tzdata"
)

/*
 * A cron expression: the five fields everybody writes, and nothing more.
 *
 * Minute, hour, day of the month, month, day of the week, each a `*`, a value, a range, a list,
 * with a step (`*\/15`, `8-18/2`). Months and days of the week also take their English
 * abbreviations, and Sunday is 0 or 7. `@hourly`, `@daily`, `@weekly` and `@monthly` stand for
 * the usual expressions.
 *
 * The day of the month and the day of the week combine as cron has always combined them: when both
 * are restricted, either one matching is enough. `0 3 1 * MON` fires on the first of the month and
 * on every Monday.
 *
 * A local time that does not exist, on the night the clocks go forward, is skipped; a local time
 * that happens twice, on the night they go back, fires once.
 */

// ErrInvalidCron reports an expression that cannot be read.
var ErrInvalidCron = errors.New("invalid cron expression")

// Cron is a read cron expression.
type Cron struct {
	minutes, hours, days, months, weekdays uint64
	// anyDay and anyWeekday record a `*`: it is what decides how the two day fields combine.
	anyDay, anyWeekday bool
}

var macros = map[string]string{
	"@hourly": "0 * * * *", "@daily": "0 0 * * *", "@midnight": "0 0 * * *",
	"@weekly": "0 0 * * 0", "@monthly": "0 0 1 * *",
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var weekdayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseCron reads a five-field expression or one of the macros.
func ParseCron(spec string) (Cron, error) {
	spec = strings.TrimSpace(spec)
	if expanded, found := macros[strings.ToLower(spec)]; found {
		spec = expanded
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return Cron{}, fmt.Errorf("%w: %q has %d fields, five are expected "+
			"(minute hour day month weekday)", ErrInvalidCron, spec, len(fields))
	}

	var cron Cron
	var err error
	parts := []struct {
		name     string
		into     *uint64
		min, max int
		names    map[string]int
	}{
		{"minute", &cron.minutes, 0, 59, nil},
		{"hour", &cron.hours, 0, 23, nil},
		{"day of the month", &cron.days, 1, 31, nil},
		{"month", &cron.months, 1, 12, monthNames},
		{"day of the week", &cron.weekdays, 0, 7, weekdayNames},
	}
	for i, part := range parts {
		if *part.into, err = parseField(fields[i], part.min, part.max, part.names); err != nil {
			return Cron{}, fmt.Errorf("%w: %s %q: %v", ErrInvalidCron, part.name, fields[i], err)
		}
	}
	// Sunday is written 0 or 7.
	if cron.weekdays&(1<<7) != 0 {
		cron.weekdays |= 1
	}
	cron.anyDay = fields[2] == "*"
	cron.anyWeekday = fields[4] == "*"
	return cron, nil
}

func parseField(field string, min, max int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, item := range strings.Split(field, ",") {
		span, stepText, stepped := strings.Cut(item, "/")
		step := 1
		if stepped {
			var err error
			if step, err = strconv.Atoi(stepText); err != nil || step < 1 {
				return 0, fmt.Errorf("step %q is not a positive number", stepText)
			}
		}

		low, high := min, max
		switch {
		case span == "*":
		case strings.Contains(span, "-"):
			from, to, _ := strings.Cut(span, "-")
			var err error
			if low, err = value(from, min, max, names); err != nil {
				return 0, err
			}
			if high, err = value(to, min, max, names); err != nil {
				return 0, err
			}
			if low > high {
				return 0, fmt.Errorf("range %q goes backwards", span)
			}
		default:
			var err error
			if low, err = value(span, min, max, names); err != nil {
				return 0, err
			}
			// `5/15` reads as cron reads it: from 5 to the end, every 15.
			if !stepped {
				high = low
			}
		}
		for v := low; v <= high; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func value(text string, min, max int, names map[string]int) (int, error) {
	if named, found := names[strings.ToLower(text)]; found {
		return named, nil
	}
	v, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%q is neither a number nor a name", text)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("%d is outside %d-%d", v, min, max)
	}
	return v, nil
}

// horizon bounds the search for the next time. Five years covers a 29 February that must also be a
// given weekday; an expression that never fires within it never fires.
const horizon = 5 * 366

// Next returns the first time strictly after `after` that the expression names, read in `location`.
func (c Cron) Next(after time.Time, location *time.Location) (time.Time, error) {
	local := after.In(location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	for i := 0; i < horizon; i++ {
		date := day.AddDate(0, 0, i)
		if !c.dayMatches(date) {
			continue
		}
		for hour := 0; hour < 24; hour++ {
			if c.hours&(1<<uint(hour)) == 0 {
				continue
			}
			for minute := 0; minute < 60; minute++ {
				if c.minutes&(1<<uint(minute)) == 0 {
					continue
				}
				candidate := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, location)
				// A local time the clocks skip is normalised to another hour: it is not this one.
				if candidate.Hour() != hour || candidate.Minute() != minute || candidate.Day() != date.Day() {
					continue
				}
				if candidate.After(after) {
					return candidate, nil
				}
			}
		}
	}
	return time.Time{}, fmt.Errorf("%w: it never fires", ErrInvalidCron)
}

func (c Cron) dayMatches(date time.Time) bool {
	if c.months&(1<<uint(date.Month())) == 0 {
		return false
	}
	day := c.days&(1<<uint(date.Day())) != 0
	weekday := c.weekdays&(1<<uint(date.Weekday())) != 0
	switch {
	case c.anyDay && c.anyWeekday:
		return true
	case c.anyDay:
		return weekday
	case c.anyWeekday:
		return day
	default:
		return day || weekday
	}
}
