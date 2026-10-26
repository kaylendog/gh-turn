// Command gh-turn sorts your open pull requests by whose move it actually is.
package main

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/kong"

	"github.com/kaylendog/gh-turn/internal/classify"
	"github.com/kaylendog/gh-turn/internal/github"
)

// version is replaced at release time via -ldflags.
var version = "dev"

const titleWidth = 64

type row struct {
	verdict classify.Verdict
	ref     string
	title   string
	url     string
}

// Order matters: sections print top to bottom, most urgent first.
var sections = []struct {
	bucket  classify.Bucket
	heading string
	colour  string
}{
	{classify.NeedsReview, "Review requested of you", "33"},
	{classify.NeedsReReview, "You reviewed, check again", "33"},
	{classify.Yours, "Your move", "31"},
	{classify.Merge, "Ready to merge", "32"},
	{classify.Theirs, "Waiting on them", "34"},
	{classify.Draft, "Your drafts", "90"},
}

// CLI is the command line, as kong reads it.
type CLI struct {
	State []classify.Bucket `help:"Only show these buckets, comma separated: ${enum}." sep:"," enum:"review,rereview,yours,merge,theirs,draft" optional:""`

	After  When `help:"Only pull requests touched since this date or age." default:"6mo"`
	Before When `help:"Only pull requests untouched since this date or age." optional:""`

	Limit int    `help:"Most pull requests to ask for per search." default:"50"`
	TSV   bool   `name:"tsv" help:"Print tab separated rows instead of a report."`
	Links string `help:"Make references clickable: ${enum}. Auto means whenever the output is a terminal." enum:"auto,always,never" default:"auto"`

	Version kong.VersionFlag `help:"Print the version and exit."`
}

// Validate is called by kong once the flags are populated.
func (c CLI) Validate() error {
	// GitHub caps a search page at 100, and rejects more with a message that
	// says nothing about --limit.
	if c.Limit < 1 || c.Limit > 100 {
		return fmt.Errorf("--limit must be between 1 and 100, got %d", c.Limit)
	}
	if !c.Before.IsZero() && !c.After.IsZero() && c.Before.Before(c.After.Time) {
		return fmt.Errorf("--before %s is earlier than --after %s, which matches nothing",
			c.Before.Format(time.DateOnly), c.After.Format(time.DateOnly))
	}
	return nil
}

// wants reports whether a bucket survives --state. No --state means all of them.
func (c CLI) wants(bucket classify.Bucket) bool {
	return len(c.State) == 0 || slices.Contains(c.State, bucket)
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("gh turn"),
		kong.Description("Sort your open pull requests by whose move it actually is."),
		kong.Vars{"version": version},
	)
	ctx.FatalIfErrorf(run(cli))
}

func run(cli CLI) error {
	progress := newProgress()
	response, err := github.Fetch(github.Options{
		Limit:  cli.Limit,
		After:  cli.After.Time,
		Before: cli.Before.Time,
	}, progress.update)
	progress.clear()
	if err != nil {
		return err
	}
	me := response.Login

	rows := make([]row, 0, len(response.Authored)+len(response.Requested))
	add := func(pr classify.PullRequest, verdict classify.Verdict) {
		if cli.wants(verdict.Bucket) {
			rows = append(rows, newRow(pr, verdict))
		}
	}
	for _, pr := range response.Authored {
		add(pr, classify.Authored(pr, me))
	}
	for _, pr := range response.Requested {
		add(pr, classify.Requested(pr, me))
	}

	if cli.TSV {
		printTSV(rows)
		return nil
	}
	printReport(rows, colourEnabled(), cli.linked(isTerminal(os.Stdout)))
	return nil
}

// linked decides whether references are rendered as OSC 8 hyperlinks. Auto
// means any terminal, since support cannot be detected; see README.md.
func (c CLI) linked(terminal bool) bool {
	switch c.Links {
	case "always":
		return true
	case "never":
		return false
	default:
		return terminal
	}
}

// hyperlink wraps text in OSC 8, making it clickable where that is understood.
func hyperlink(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// When is a moment given either as a date (2026-01-31) or as an age counted
// back from now (6mo, 3w, 90d, 1y). Kong calls UnmarshalText for it, so a bad
// value is reported against the flag that carried it.
type When struct{ time.Time }

var ages = []struct {
	suffix string
	back   func(time.Time, int) time.Time
}{
	{"mo", func(t time.Time, n int) time.Time { return monthsAgo(t, n) }},
	{"y", func(t time.Time, n int) time.Time { return monthsAgo(t, 12*n) }},
	{"w", func(t time.Time, n int) time.Time { return t.AddDate(0, 0, -7*n) }},
	{"d", func(t time.Time, n int) time.Time { return t.AddDate(0, 0, -n) }},
}

// monthsAgo steps back whole months, clamping to the last day of the month it
// lands in. time.AddDate alone normalises overflow forwards, so one month
// before 31 October would come out as 1 October, and a year before 29 February
// as 1 March.
func monthsAgo(t time.Time, months int) time.Time {
	year, month, day := t.Date()
	first := time.Date(year, month-time.Month(months), 1,
		t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())

	// Day zero of the following month is the last day of this one.
	last := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
	return first.AddDate(0, 0, min(day, last)-1)
}

func (w *When) UnmarshalText(text []byte) error {
	return w.parse(string(text), time.Now())
}

func (w *When) parse(value string, now time.Time) error {
	if value == "" {
		w.Time = time.Time{}
		return nil
	}
	if date, err := time.Parse(time.DateOnly, value); err == nil {
		w.Time = date
		return nil
	}
	// Longest suffix first, so "mo" is never read as a bare "o".
	for _, age := range ages {
		digits, found := strings.CutSuffix(value, age.suffix)
		if !found {
			continue
		}
		n, err := strconv.Atoi(digits)
		if err != nil || n < 0 {
			break
		}
		w.Time = age.back(now, n)
		return nil
	}
	return fmt.Errorf("want a date like 2026-01-31 or an age like 6mo, 3w, 90d or 1y, got %q", value)
}

func newRow(pr classify.PullRequest, verdict classify.Verdict) row {
	return row{
		verdict: verdict,
		ref:     pr.Ref(),
		title:   pr.Title,
		url:     pr.URL,
	}
}

// printTSV emits full titles: truncating here would hand lossy data to
// whatever is parsing the columns.
func printTSV(rows []row) {
	for _, r := range rows {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n",
			r.verdict.Bucket, r.ref, r.verdict.Why,
			strings.ReplaceAll(r.title, "\t", " "), r.url)
	}
}

func printReport(rows []row, colour, links bool) {
	paint := func(code, s string) string {
		if !colour {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}

	// Measure across every row rather than per section, so the columns hold
	// their position all the way down the report instead of stepping in and
	// out at each heading.
	refWidth, whyWidth := 0, 0
	for _, r := range rows {
		refWidth = max(refWidth, len(r.ref))
		whyWidth = max(whyWidth, len(r.verdict.Why))
	}

	printed := 0
	for _, section := range sections {
		var matching []row
		for _, r := range rows {
			if r.verdict.Bucket == section.bucket {
				matching = append(matching, r)
			}
		}
		if len(matching) == 0 {
			continue
		}
		printed += len(matching)

		fmt.Printf("\n%s %s\n",
			paint("1;"+section.colour, section.heading),
			paint("2", fmt.Sprintf("(%d)", len(matching))))

		for _, r := range matching {
			// Pad outside the escapes, so neither colour nor the hyperlink
			// counts towards the column width.
			ref := r.ref
			if links {
				ref = hyperlink(r.url, ref)
			}
			ref = paint("36", ref) + strings.Repeat(" ", refWidth-len(r.ref))

			// The reason is the point of the report, so it reads at full
			// strength and the title recedes behind it.
			fmt.Printf("  %s  %s  %s\n",
				ref,
				pad(r.verdict.Why, whyWidth),
				paint("2", truncate(r.title, titleWidth)))

			// With a clickable reference the URL is noise; without one it is
			// the only way to reach the pull request.
			if !links {
				fmt.Printf("  %s\n", paint("2", r.url))
			}
		}
	}

	if printed == 0 {
		fmt.Println("Nothing open. Enjoy it while it lasts.")
		return
	}
	fmt.Println()
}

func pad(s string, width int) string {
	return s + strings.Repeat(" ", width-len(s))
}

// truncate shortens a title to width runes.
//
// shortcut: counts runes, not display cells, so a title full of CJK or emoji
// overshoots. Reach for x/text/width or uniseg if that ever matters.
func truncate(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-3]) + "..."
}

// progress names the pull requests being fetched. GitHub takes seconds to
// resolve every timeline, which is long enough to look like a hang, and a
// count alone does not say what is slow. It draws on stderr, so piping stdout
// stays clean, and does nothing at all when stderr is not a terminal.
//
// Names arrive in list order, so entries hold their position as the set
// shrinks rather than jumping about between redraws.
type progress struct {
	mu       sync.Mutex
	lines    int // how many lines are on screen, to erase them later
	frame    int
	done     int
	total    int
	inFlight []string
	on       bool
	stop     chan struct{}
	stopped  chan struct{}
}

var spinner = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

func newProgress() *progress {
	p := &progress{
		on:      isTerminal(os.Stderr),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	if !p.on {
		close(p.stopped)
		return p
	}
	// Batches can take a second or more, so the display has to animate on its
	// own rather than only when one lands.
	go p.animate()
	return p
}

func (p *progress) animate() {
	defer close(p.stopped)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-p.stop:
			return
		case <-tick.C:
			p.mu.Lock()
			p.frame++
			p.draw()
			p.mu.Unlock()
		}
	}
}

func (p *progress) update(done, total int, inFlight []string) {
	if !p.on {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done, p.total, p.inFlight = done, total, inFlight
	p.draw()
}

// draw repaints the whole block. The caller must hold mu.
func (p *progress) draw() {
	p.erase()
	frame := spinner[p.frame%len(spinner)]

	// Before the first batch reports in, the listing pass is still running.
	if p.total == 0 {
		fmt.Fprintf(os.Stderr, "\033[2m%c Listing pull requests\033[0m\n", frame)
		p.lines = 1
		return
	}

	fmt.Fprintf(os.Stderr, "\033[2m%c %d/%d fetched, %d in flight\033[0m\n",
		frame, p.done, p.total, len(p.inFlight))
	p.lines = 1
	for _, name := range p.inFlight {
		fmt.Fprintf(os.Stderr, "\033[2m    %s\033[0m\n", name)
		p.lines++
	}
}

func (p *progress) clear() {
	if !p.on {
		return
	}
	close(p.stop)
	<-p.stopped

	p.mu.Lock()
	defer p.mu.Unlock()
	p.erase()
}

// erase walks back up over whatever was drawn last and wipes each line.
func (p *progress) erase() {
	for range p.lines {
		fmt.Fprint(os.Stderr, "\033[1A\033[2K")
	}
	p.lines = 0
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// colourEnabled mirrors the usual terminal conventions: no colour when piped,
// and NO_COLOR always wins.
func colourEnabled() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
