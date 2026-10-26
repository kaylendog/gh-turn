// Command gh-turn sorts your open pull requests by whose move it actually is.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

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

func main() {
	tsv := flag.Bool("tsv", false, "print tab-separated rows instead of a report")
	limit := flag.Int("limit", 50, "maximum pull requests to fetch per search")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if err := run(*tsv, *limit); err != nil {
		fmt.Fprintln(os.Stderr, "gh turn:", err)
		os.Exit(1)
	}
}

func run(tsv bool, limit int) error {
	// GitHub caps a search page at 100; without this the API rejects the
	// query with a message that says nothing about --limit.
	if limit < 1 || limit > 100 {
		return fmt.Errorf("--limit must be between 1 and 100, got %d", limit)
	}

	progress := newProgress()
	response, err := github.Fetch(limit, progress.update)
	progress.clear()
	if err != nil {
		return err
	}
	me := response.Login

	rows := make([]row, 0, len(response.Authored)+len(response.Requested))
	for _, pr := range response.Authored {
		rows = append(rows, newRow(pr, classify.Authored(pr, me)))
	}
	for _, pr := range response.Requested {
		rows = append(rows, newRow(pr, classify.Requested(pr, me)))
	}

	if tsv {
		printTSV(rows)
		return nil
	}
	printReport(rows, colourEnabled())
	return nil
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

func printReport(rows []row, colour bool) {
	paint := func(code, s string) string {
		if !colour {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
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

		// Pad before colouring, so escape codes never count towards width.
		refWidth, whyWidth := 0, 0
		for _, r := range matching {
			refWidth = max(refWidth, len(r.ref))
			whyWidth = max(whyWidth, len(r.verdict.Why))
		}

		fmt.Printf("\n%s %s\n",
			paint("1;"+section.colour, section.heading),
			paint("2", fmt.Sprintf("(%d)", len(matching))))

		for _, r := range matching {
			fmt.Printf("  %s  %s  %s\n",
				paint("36", pad(r.ref, refWidth)),
				paint("2", pad(r.verdict.Why, whyWidth)),
				truncate(r.title, titleWidth))
			fmt.Printf("  %s\n", paint("2", r.url))
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
// Batches land out of order, so this shows the most recent arrivals rather
// than a stable list.
type progress struct {
	mu    sync.Mutex
	lines int // how many lines are on screen, to erase them later
	on    bool
}

const progressRows = 4

func newProgress() *progress {
	return &progress{on: isTerminal(os.Stderr)}
}

func (p *progress) update(done, total int, names []string) {
	if !p.on {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	p.erase()
	fmt.Fprintf(os.Stderr, "\033[2mFetched %d/%d pull requests\033[0m\n", done, total)
	p.lines = 1
	for _, name := range names {
		if p.lines > progressRows {
			fmt.Fprintf(os.Stderr, "\033[2m  … and %d more\033[0m\n",
				len(names)-progressRows)
			p.lines++
			break
		}
		fmt.Fprintf(os.Stderr, "\033[2m  ✓ %s\033[0m\n", name)
		p.lines++
	}
}

func (p *progress) clear() {
	if !p.on {
		return
	}
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
