// git-ownership: visualise % of code per author over every commit in a repo.
//
// Strategy: replay git history incrementally.
//   - Fetch all commit hashes, split into N chunks.
//   - Run N parallel git log -p workers; each pre-parses its chunk into
//     lightweight diffEvent structs.
//   - Main goroutine applies chunks in order to the RLE ownership state.
//   - Record a snapshot of running totals every stride commits.
//
// Cost: O(total lines changed) across N parallel git processes.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

func main() {
	repoFlag    := flag.String("repo", ".", "path to git repository")
	branchFlag  := flag.String("branch", "HEAD", "branch/ref to analyse")
	outputFlag  := flag.String("output", "", "output HTML file (default: <reponame>.html)")
	maxPtsFlag  := flag.Int("max-points", 1000,
		"max chart data points — commits are strided to fit; 0 = record every commit")
	minPctFlag  := flag.Float64("min-pct", 1.0,
		"authors who never exceeded this %% of total lines are grouped into 'Others'")
	workersFlag := flag.Int("workers", runtime.NumCPU(),
		"parallel git log workers (default: number of CPUs)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: git-ownership [flags] [repo-path]\n\nFlags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  git-ownership .
  git-ownership --branch main /path/to/repo
  git-ownership --output graph.html --max-points 0 .
`)
	}
	flag.Parse()
	if flag.NArg() > 0 {
		*repoFlag = flag.Arg(0)
	}

	absRepo, err := filepath.Abs(*repoFlag)
	if err != nil {
		log.Fatalf("invalid repo path: %v", err)
	}
	if _, err := gitCmd(absRepo, "rev-parse", "--git-dir"); err != nil {
		log.Fatalf("%s is not a git repository", absRepo)
	}

	outFile := *outputFlag
	if outFile == "" {
		outFile = filepath.Base(absRepo) + ".html"
	}

	fmt.Printf("Repository : %s\n", absRepo)
	fmt.Printf("Branch     : %s\n", *branchFlag)
	fmt.Printf("Workers    : %d\n", *workersFlag)

	// 1. Fetch commit hashes (fast; no diff output) for chunk boundaries + stride.
	fmt.Print("Loading commit list… ")
	hashes, err := getHashes(absRepo, *branchFlag)
	if err != nil {
		log.Fatalf("\n%v", err)
	}
	total := len(hashes)
	fmt.Printf("%d commits\n", total)
	if total == 0 {
		log.Fatal("no commits found")
	}

	// 2. Determine snapshot stride.
	stride := 1
	if *maxPtsFlag > 0 && total > *maxPtsFlag {
		stride = total / *maxPtsFlag
	}
	if stride > 1 {
		fmt.Printf("Stride     : every %d commits (~%d snapshots)\n", stride, total/stride)
	} else {
		fmt.Printf("Snapshots  : every commit\n")
	}
	fmt.Println()

	// 3. Replay history with parallel git workers.
	state := newState()
	var snaps []Snapshot
	emailToName := make(map[string]string)

	start := time.Now()
	i := 0

	if err := streamLog(absRepo, *branchFlag, *workersFlag, state, func(c CommitMeta) error {
		if c.AuthorName != "" {
			emailToName[c.AuthorEmail] = c.AuthorName
		}

		wantSnap := (i == 0) || (i == total-1) || ((i+1)%stride == 0)
		if wantSnap {
			snaps = append(snaps, Snapshot{
				Label:  c.Date.Format("2006-01-02") + " " + c.Hash[:7],
				Totals: state.copyTotals(),
				Total:  state.totalLines(),
			})
		}

		if (i+1)%200 == 0 || i == total-1 {
			elapsed := time.Since(start)
			rate := float64(i+1) / elapsed.Seconds()
			remaining := float64(total-i-1) / rate
			eta := time.Duration(remaining) * time.Second
			fmt.Printf("\r[%d/%d] %.0f commits/s  ETA %-8s  lines tracked: %-8d",
				i+1, total, rate, eta.Round(time.Second), state.totalLines())
		}
		i++
		return nil
	}); err != nil {
		log.Fatalf("\nstream log: %v", err)
	}

	fmt.Printf("\n\nDone in %s\n", time.Since(start).Round(time.Millisecond*100))
	fmt.Printf("Snapshots  : %d\n", len(snaps))

	// 4. Build chart data and render HTML.
	cd := buildChart(snaps, emailToName, *minPctFlag)
	vars := buildTemplateVars(filepath.Base(absRepo), *branchFlag, outFile, total, snaps, cd)
	renderHTML(outFile, vars)

	fmt.Printf("Output     : %s\n", outFile)
}
