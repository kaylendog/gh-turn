// Command gh-turn sorts your open pull requests by whose move it actually is.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

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

	response, err := github.Fetch(limit)
	if err != nil {
		return err
	}
	me := response.Viewer.Login

	rows := make([]row, 0, len(response.Authored.Nodes)+len(response.Requested.Nodes))
	for _, pr := range response.Authored.Nodes {
		rows = append(rows, newRow(pr, classify.Authored(pr, me)))
	}
	for _, pr := range response.Requested.Nodes {
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
