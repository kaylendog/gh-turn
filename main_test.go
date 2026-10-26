package main

import (
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"

	"github.com/kaylendog/gh-turn/internal/classify"
)

// now is a fixed point so relative ages are reproducible. It is deliberately
// the 31st, which is where month arithmetic goes wrong if it is naive.
var now = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

func TestWhenParse(t *testing.T) {
	cases := []struct {
		in   string
		want string // empty means "expect an error"
	}{
		{"2026-01-31", "2026-01-31"},
		{"1d", "2026-10-30"},
		{"7d", "2026-10-24"},
		{"1w", "2026-10-24"},
		{"3w", "2026-10-10"},
		{"1mo", "2026-09-30"}, // September has 30 days
		{"6mo", "2026-04-30"},
		{"1y", "2025-10-31"},
		{"0d", "2026-10-31"},

		{"lasttuesday", ""},
		{"6m", ""}, // minutes are not a sensible age here, so not accepted
		{"mo", ""},
		{"-3d", ""},
		{"3.5d", ""},
		{"2026-13-01", ""},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			var w When
			err := w.parse(tc.in, now)

			if tc.want == "" {
				if err == nil {
					t.Fatalf("parse(%q) = %s, want an error", tc.in, w.Format(time.DateOnly))
				}
				return
			}
			if err != nil {
				t.Fatalf("parse(%q): %v", tc.in, err)
			}
			if got := w.Format(time.DateOnly); got != tc.want {
				t.Errorf("parse(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// Month arithmetic has to clamp rather than overflow forwards, or stepping
// back from the end of a long month lands in the wrong month entirely.
func TestMonthsAgoClamps(t *testing.T) {
	cases := []struct {
		from   string
		months int
		want   string
	}{
		{"2026-10-31", 1, "2026-09-30"},  // September has 30 days
		{"2026-03-31", 1, "2026-02-28"},  // February, common year
		{"2028-03-31", 1, "2028-02-29"},  // February, leap year
		{"2028-02-29", 12, "2027-02-28"}, // a year before a leap day
		{"2026-10-15", 1, "2026-09-15"},  // mid-month needs no clamping
		{"2026-01-31", 1, "2025-12-31"},  // across a year boundary
	}

	for _, tc := range cases {
		t.Run(tc.from, func(t *testing.T) {
			from, err := time.Parse(time.DateOnly, tc.from)
			if err != nil {
				t.Fatalf("bad fixture: %v", err)
			}
			if got := monthsAgo(from, tc.months).Format(time.DateOnly); got != tc.want {
				t.Errorf("monthsAgo(%s, %d) = %s, want %s", tc.from, tc.months, got, tc.want)
			}
		})
	}
}

// An empty value clears the bound rather than meaning "the zero date", which
// is how an unset --before is told apart from a real one.
func TestWhenEmptyIsNoBound(t *testing.T) {
	var w When
	if err := w.parse("", now); err != nil {
		t.Fatalf("parse(\"\"): %v", err)
	}
	if !w.IsZero() {
		t.Errorf("parse(\"\") = %s, want the zero time", w.Format(time.DateOnly))
	}
}

// OSC 8 is zero width on screen, so anything measuring the reference for
// alignment must do so before the escape is wrapped around it.
func TestHyperlink(t *testing.T) {
	const (
		url  = "https://github.com/element-hq/element-web/pull/35404"
		text = "element-hq/element-web#35404"
	)
	got := hyperlink(url, text)

	want := "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
	if got != want {
		t.Errorf("hyperlink() = %q, want %q", got, want)
	}
	if !strings.Contains(got, text) {
		t.Error("the link text is not present, so nothing would be shown")
	}
	// Two terminators, so the link is opened and then closed again.
	if n := strings.Count(got, "\x1b]8;;"); n != 2 {
		t.Errorf("found %d OSC 8 introducers, want 2", n)
	}
}

func TestLinked(t *testing.T) {
	cases := []struct {
		links string
		want  bool
	}{
		{"always", true},
		{"never", false},
	}
	for _, tc := range cases {
		t.Run(tc.links, func(t *testing.T) {
			if got := (CLI{Links: tc.links}).linked(); got != tc.want {
				t.Errorf("linked() = %v, want %v", got, tc.want)
			}
		})
	}

	// auto follows the terminal, and the test binary's stdout is not one.
	if (CLI{Links: "auto"}).linked() {
		t.Error("auto linked when stdout is not a terminal")
	}
}

func TestWants(t *testing.T) {
	cases := []struct {
		name   string
		states []classify.Bucket
		bucket classify.Bucket
		want   bool
	}{
		{"no filter admits everything", nil, classify.Draft, true},
		{"listed bucket passes", []classify.Bucket{classify.Yours}, classify.Yours, true},
		{"unlisted bucket is dropped", []classify.Bucket{classify.Yours}, classify.Theirs, false},
		{"one of several", []classify.Bucket{classify.Yours, classify.Merge}, classify.Merge, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := CLI{State: tc.states}
			if got := cli.wants(tc.bucket); got != tc.want {
				t.Errorf("wants(%q) = %v, want %v", tc.bucket, got, tc.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	when := func(s string) When {
		var w When
		if err := w.parse(s, now); err != nil {
			t.Fatalf("bad fixture %q: %v", s, err)
		}
		return w
	}

	cases := []struct {
		name    string
		cli     CLI
		wantErr bool
	}{
		{"defaults are fine", CLI{Limit: 50}, false},
		{"limit at the floor", CLI{Limit: 1}, false},
		{"limit at the ceiling", CLI{Limit: 100}, false},
		{"limit below the floor", CLI{Limit: 0}, true},
		{"limit above the ceiling", CLI{Limit: 101}, true},
		{"ordered range", CLI{Limit: 50, After: when("6mo"), Before: when("1mo")}, false},
		{"inverted range", CLI{Limit: 50, After: when("1mo"), Before: when("6mo")}, true},
		{"only a lower bound", CLI{Limit: 50, After: when("6mo")}, false},
		{"only an upper bound", CLI{Limit: 50, Before: when("6mo")}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cli.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// The struct tags carry the enum and defaults, so a typo in them would only
// surface at runtime without this.
func TestCLIParses(t *testing.T) {
	parser := func(t *testing.T) (*CLI, *kong.Kong) {
		t.Helper()
		var cli CLI
		k, err := kong.New(&cli, kong.Name("gh turn"), kong.Vars{"version": "test"},
			kong.Exit(func(int) { t.Fatal("kong exited") }))
		if err != nil {
			t.Fatalf("building parser: %v", err)
		}
		return &cli, k
	}

	t.Run("defaults", func(t *testing.T) {
		cli, k := parser(t)
		if _, err := k.Parse(nil); err != nil {
			t.Fatalf("parsing no args: %v", err)
		}
		if cli.Limit != 50 {
			t.Errorf("Limit = %d, want 50", cli.Limit)
		}
		if cli.After.IsZero() {
			t.Error("After is unset, want the 6mo default applied")
		}
		if !cli.Before.IsZero() {
			t.Error("Before is set, want no upper bound by default")
		}
	})

	t.Run("state list", func(t *testing.T) {
		cli, k := parser(t)
		if _, err := k.Parse([]string{"--state", "yours,merge"}); err != nil {
			t.Fatalf("parsing --state: %v", err)
		}
		want := []classify.Bucket{classify.Yours, classify.Merge}
		if len(cli.State) != len(want) {
			t.Fatalf("State = %v, want %v", cli.State, want)
		}
		for i := range want {
			if cli.State[i] != want[i] {
				t.Errorf("State[%d] = %q, want %q", i, cli.State[i], want[i])
			}
		}
	})

	t.Run("unknown state is rejected", func(t *testing.T) {
		_, k := parser(t)
		if _, err := k.Parse([]string{"--state", "nonsense"}); err == nil {
			t.Error("parsing --state nonsense succeeded, want an error")
		}
	})

	// Every bucket the report can print must be accepted by --state, or a
	// section would be unreachable from the command line.
	t.Run("every section bucket is a valid state", func(t *testing.T) {
		for _, section := range sections {
			_, k := parser(t)
			if _, err := k.Parse([]string{"--state", string(section.bucket)}); err != nil {
				t.Errorf("--state %s rejected: %v", section.bucket, err)
			}
		}
	})
}
