package schedule

import (
	"strings"
	"testing"
	"time"
)

var paris = func() *time.Location {
	location, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		panic(err)
	}
	return location
}()

func at(location *time.Location, text string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04", text, location)
	if err != nil {
		panic(err)
	}
	return parsed
}

// The three CronJobs a schedule replaces, read where they were written: Paris.
func TestTheExpressionsTheCronJobsUsed(t *testing.T) {
	cases := []struct {
		spec, after, next string
	}{
		// Integration tests, three times a working day.
		{"0 8,12,16 * * 1-5", "2026-10-30 08:00", "2026-10-30 12:00"},
		{"0 8,12,16 * * 1-5", "2026-10-30 16:00", "2026-11-02 08:00"}, // Friday evening, then Monday
		// End-to-end tests, every night.
		{"0 3 * * *", "2026-10-30 14:00", "2026-10-31 03:00"},
		// Security audit, on Monday morning.
		{"0 5 * * 1", "2026-10-30 14:00", "2026-11-02 05:00"},
		{"0 5 * * MON", "2026-11-02 05:00", "2026-11-09 05:00"},
	}
	for _, c := range cases {
		cron, err := ParseCron(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		next, err := cron.Next(at(paris, c.after), paris)
		if err != nil || !next.Equal(at(paris, c.next)) {
			t.Errorf("%s after %s: %v, %v; expected %s", c.spec, c.after, next.In(paris), err, c.next)
		}
	}
}

func TestStepsRangesListsAndMacros(t *testing.T) {
	cases := []struct {
		spec, after, next string
	}{
		{"*/15 * * * *", "2026-10-30 10:07", "2026-10-30 10:15"},
		{"5/20 * * * *", "2026-10-30 10:26", "2026-10-30 10:45"},
		{"0 9-17/4 * * *", "2026-10-30 13:00", "2026-10-30 17:00"},
		{"30 6 1,15 * *", "2026-10-02 00:00", "2026-10-15 06:30"},
		{"0 0 1 jan *", "2026-10-30 00:00", "2027-01-01 00:00"},
		{"0 0 * * 7", "2026-10-30 00:00", "2026-11-01 00:00"}, // Sunday written 7
		{"@daily", "2026-10-30 10:00", "2026-10-31 00:00"},
		{"@weekly", "2026-10-30 10:00", "2026-11-01 00:00"},
		{"0 0 29 2 *", "2026-10-30 00:00", "2028-02-29 00:00"},
	}
	for _, c := range cases {
		cron, err := ParseCron(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		next, err := cron.Next(at(time.UTC, c.after), time.UTC)
		if err != nil || !next.Equal(at(time.UTC, c.next)) {
			t.Errorf("%s after %s: %v, %v; expected %s", c.spec, c.after, next, err, c.next)
		}
	}
}

// Both day fields restricted: either one matching is enough, as cron has always read them.
func TestTheTwoDayFieldsCombineByEither(t *testing.T) {
	cron, err := ParseCron("0 3 1 * MON")
	if err != nil {
		t.Fatal(err)
	}
	next, _ := cron.Next(at(time.UTC, "2026-10-27 12:00"), time.UTC) // a Tuesday
	if !next.Equal(at(time.UTC, "2026-11-01 03:00")) {
		t.Errorf("first of the month missed: %v", next)
	}
	next, _ = cron.Next(next, time.UTC)
	if !next.Equal(at(time.UTC, "2026-11-02 03:00")) {
		t.Errorf("Monday missed: %v", next)
	}
}

// The night the clocks go forward has no 02:30, and the schedule waits for the next one; the night
// they go back has two, and it fires once.
func TestDaylightSavingTime(t *testing.T) {
	cron, err := ParseCron("30 2 * * *")
	if err != nil {
		t.Fatal(err)
	}
	next, _ := cron.Next(at(paris, "2027-03-27 12:00"), paris)
	if !next.Equal(at(paris, "2027-03-29 02:30")) {
		t.Errorf("spring forward: %v", next.In(paris))
	}
	first, _ := cron.Next(at(paris, "2026-10-24 12:00"), paris)
	second, _ := cron.Next(first, paris)
	if second.Sub(first) < 23*time.Hour {
		t.Errorf("fired twice on the night the clocks go back: %v then %v", first, second)
	}
}

func TestAnUnreadableExpressionSaysWhy(t *testing.T) {
	for spec, reason := range map[string]string{
		"0 8 * *":      "five are expected",
		"60 * * * *":   "minute",
		"0 24 * * *":   "hour",
		"0 0 0 * *":    "day of the month",
		"0 0 * 13 *":   "month",
		"0 0 * * 8":    "day of the week",
		"0 0 * * fun":  "neither a number nor a name",
		"*/0 * * * *":  "step",
		"0 17-9 * * *": "backwards",
		"0 0 31 2 *":   "never fires",
	} {
		cron, err := ParseCron(spec)
		if err == nil {
			_, err = cron.Next(time.Now(), time.UTC)
		}
		if err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("%q: %v, expected %q", spec, err, reason)
		}
	}
}
