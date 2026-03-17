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
	"sort"
	"strings"
	"time"
)

func main() {
	branchFlag := flag.String("branch", "HEAD", "branch/ref to analyse")
	outputFlag := flag.String("output", "", "output HTML file (default: <reponame>.html)")
	maxPtsFlag := flag.Int("max-points", 1000,
		"max chart data points — commits are strided to fit; 0 = record every commit")
	maxGraphFlag := flag.Int("max-graph", 50,
		"max authors included as individual chart datasets; 0 = all")
	folderFlag := flag.Int("folder", 10,
		"number of largest folders to break down (searched at any depth); 0 = whole project only")
	workersFlag := flag.Int("workers", runtime.NumCPU(),
		"parallel git log workers (default: number of CPUs)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: git-ownership [flags] <repo-path>\n\nFlags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
Examples:
  git-ownership .
  git-ownership --branch main /path/to/repo
  git-ownership --output graph.html --max-points 0 .
`)
	}
	flag.Parse()
	repo := "."
	if flag.NArg() > 0 {
		repo = flag.Arg(0)
	}

	absRepo, err := filepath.Abs(repo)
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

	// 1. Pre-scan repo tree to select folders to break down (file count as proxy).
	var selectedDirs []string
	var selectedFileCounts map[string]int
	var totalFiles int
	if *folderFlag > 0 {
		fmt.Print("Folders    : scanning… ")
		var dirErr error
		selectedDirs, selectedFileCounts, totalFiles, dirErr = selectFolders(absRepo, *branchFlag, *folderFlag)
		if dirErr != nil {
			fmt.Printf("\rFolders    : (skipped: %v)\n", dirErr)
			selectedDirs = nil
		}
	}

	// Print selected folders as a Unicode tree with file counts (available immediately).
	if len(selectedDirs) > 0 {
		fmt.Printf("\rFolders    : \033[1;36m/\033[0m  %d files\n", totalFiles)

		// Build the full node set: selected dirs + all their ancestor directories,
		// so the tree has no gaps and renders correctly.
		type treeNode struct {
			path     string
			name     string
			depth    int
			files    int
			selected bool
		}
		nodeMap := make(map[string]*treeNode)
		for _, d := range selectedDirs {
			parts := strings.Split(d, "/")
			for depth := 0; depth < len(parts); depth++ {
				path := strings.Join(parts[:depth+1], "/")
				if _, exists := nodeMap[path]; !exists {
					nodeMap[path] = &treeNode{path: path, name: parts[depth], depth: depth}
				}
			}
			nodeMap[d].selected = true
			nodeMap[d].files = selectedFileCounts[d]
		}
		nodes := make([]*treeNode, 0, len(nodeMap))
		for _, n := range nodeMap {
			nodes = append(nodes, n)
		}
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].path < nodes[j].path })

		// Does the ancestor of nodes[i] at `level` have any later sibling?
		hasLaterSiblingAt := func(i, level int) bool {
			parts := strings.Split(nodes[i].path, "/")
			myAncestor := strings.Join(parts[:level+1], "/")
			parentPath := strings.Join(parts[:level], "/")
			for _, other := range nodes[i+1:] {
				op := strings.Split(other.path, "/")
				if len(op) > level {
					if strings.Join(op[:level], "/") == parentPath &&
						strings.Join(op[:level+1], "/") != myAncestor {
						return true
					}
				}
			}
			return false
		}

		// Is nodes[i] the last child of its parent?
		isLastSibling := func(i int) bool {
			n := nodes[i]
			parts := strings.Split(n.path, "/")
			myParent := strings.Join(parts[:n.depth], "/")
			for _, other := range nodes[i+1:] {
				if other.depth == n.depth {
					op := strings.Split(other.path, "/")
					if strings.Join(op[:other.depth], "/") == myParent {
						return false
					}
				}
			}
			return true
		}

		for i, n := range nodes {
			prefix := ""
			for level := 0; level < n.depth; level++ {
				if hasLaterSiblingAt(i, level) {
					prefix += "│   "
				} else {
					prefix += "    "
				}
			}
			connector := "├── "
			if isLastSibling(i) {
				connector = "└── "
			}
			if n.selected {
				fmt.Printf("             %s%s\033[1;36m%s/\033[0m  %d files\n", prefix, connector, n.name, n.files)
			} else {
				fmt.Printf("             %s%s%s/\n", prefix, connector, n.name)
			}
		}
	}

	trackDirs := make(map[string]bool, len(selectedDirs))
	for _, d := range selectedDirs {
		trackDirs[d] = true
	}

	// 2. Fetch commit hashes and determine snapshot stride.
	fmt.Print("Snapshots  : loading commit list… ")
	hashes, err := getHashes(absRepo, *branchFlag)
	if err != nil {
		log.Fatalf("\n%v", err)
	}
	total := len(hashes)
	if total == 0 {
		log.Fatal("no commits found")
	}
	stride := 1
	if *maxPtsFlag > 0 && total > *maxPtsFlag {
		stride = total / *maxPtsFlag
	}
	if stride > 1 {
		fmt.Printf("\rSnapshots  : every %d commits (%d commits total)\n\n", stride, total)
	} else {
		fmt.Printf("\rSnapshots  : every commit (%d commits)\n\n", total)
	}

	// 4. Replay history with parallel git workers.
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
				Label:     c.Date.Format("2006-01-02") + " " + c.Hash[:7],
				Totals:    state.copyTotals(),
				Total:     state.totalLines(),
				DirTotals: state.computeDirTotals(trackDirs),
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

	// 5. Build per-folder chart data and render HTML.
	folders := buildAllFolderData(snaps, emailToName, *maxGraphFlag, selectedDirs)
	vars := buildTemplateVars(filepath.Base(absRepo), *branchFlag, outFile, total, snaps, folders)
	renderHTML(outFile, vars)

	fmt.Printf("Output     : %s\n", outFile)
}
