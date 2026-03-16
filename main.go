// git-ownership: visualise % of code per author over every commit in a repo.
//
// Strategy: replay git history incrementally.
//   - For each commit, fetch only the diff against its parent (git diff -U0).
//   - Apply the diff to a per-file run-length-encoded ownership table.
//   - Additions → credited to the current committer.
//   - Deletions → debited from whoever actually owned those lines (tracked in the RLE table).
//   - Record a snapshot of running totals at each commit (or every N commits).
//
// Cost: O(total lines changed across history) — one git-diff per commit, no blame.

package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── run-length encoded line ownership ────────────────────────────────────────

// Seg is a run of N consecutive lines all owned by the same author.
type Seg struct {
	Author string
	N      int
}

// mergeSegs collapses adjacent segments with the same author.
func mergeSegs(in []Seg) []Seg {
	out := make([]Seg, 0, len(in))
	for _, s := range in {
		if s.N <= 0 {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Author == s.Author {
			out[len(out)-1].N += s.N
		} else {
			out = append(out, s)
		}
	}
	return out
}

// deleteRange removes `count` lines starting at `pos` (0-indexed).
// Returns the updated segments and a map of author → lines removed.
func deleteRange(segs []Seg, pos, count int) ([]Seg, map[string]int) {
	removed := make(map[string]int)
	if count == 0 || len(segs) == 0 {
		return segs, removed
	}
	end := pos + count
	out := make([]Seg, 0, len(segs))
	cur := 0
	for _, s := range segs {
		hi := cur + s.N
		if hi <= pos || cur >= end {
			out = append(out, s) // entirely outside deletion zone
		} else {
			if cur < pos { // prefix before zone
				out = append(out, Seg{s.Author, pos - cur})
			}
			dFrom := imax(cur, pos)
			dTo := imin(hi, end)
			removed[s.Author] += dTo - dFrom
			if hi > end { // suffix after zone
				out = append(out, Seg{s.Author, hi - end})
			}
		}
		cur = hi
	}
	return mergeSegs(out), removed
}

// insertAt inserts `count` lines by `author` at position `pos` (0-indexed).
func insertAt(segs []Seg, pos int, author string, count int) []Seg {
	if count == 0 {
		return segs
	}
	out := make([]Seg, 0, len(segs)+2)
	cur := 0
	done := false
	for _, s := range segs {
		if !done && cur+s.N >= pos {
			pre := pos - cur
			if pre > 0 {
				out = append(out, Seg{s.Author, pre})
			}
			out = append(out, Seg{author, count})
			suf := s.N - pre
			if suf > 0 {
				out = append(out, Seg{s.Author, suf})
			}
			done = true
		} else {
			out = append(out, s)
		}
		cur += s.N
	}
	if !done {
		out = append(out, Seg{author, count})
	}
	return mergeSegs(out)
}

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func imin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── repo ownership state ──────────────────────────────────────────────────────

// State is the full ownership picture of the repo at a point in time.
type State struct {
	Files  map[string][]Seg // filepath → RLE ownership
	Totals map[string]int   // author email → total lines
}

func newState() *State {
	return &State{Files: make(map[string][]Seg), Totals: make(map[string]int)}
}

func (st *State) totalLines() int {
	n := 0
	for _, v := range st.Totals {
		n += v
	}
	return n
}

func (st *State) copyTotals() map[string]int {
	m := make(map[string]int, len(st.Totals))
	for k, v := range st.Totals {
		m[k] = v
	}
	return m
}

// applyHunk applies one diff hunk to a single file.
//
// oldStart is the 1-indexed line number in the pre-patch file.
// offset is the running delta for this file in this diff (caller must track).
//
// After a deletion of D lines and insertion of A lines:
//   offset += A - D
func (st *State) applyHunk(file string, oldStart, oldCount, newCount int, author string, offset *int) {
	segs := st.Files[file]

	if oldCount > 0 {
		delPos := oldStart - 1 + *offset
		if delPos < 0 {
			delPos = 0
		}
		newSegs, removed := deleteRange(segs, delPos, oldCount)
		segs = newSegs
		for a, n := range removed {
			st.Totals[a] -= n
			if st.Totals[a] <= 0 {
				delete(st.Totals, a)
			}
		}
	}

	if newCount > 0 {
		var insertPos int
		if oldCount == 0 {
			// Pure insertion: git says "after line oldStart" → 0-indexed = oldStart
			insertPos = oldStart + *offset
		} else {
			// Replacement: insert where we just deleted
			insertPos = oldStart - 1 + *offset
		}
		if insertPos < 0 {
			insertPos = 0
		}
		segs = insertAt(segs, insertPos, author, newCount)
		st.Totals[author] += newCount
	}

	*offset += newCount - oldCount

	if len(segs) == 0 {
		delete(st.Files, file)
	} else {
		st.Files[file] = segs
	}
}

func (st *State) renameFile(from, to string) {
	if segs, ok := st.Files[from]; ok {
		st.Files[to] = segs
		delete(st.Files, from)
	}
}

func (st *State) deleteFile(file string) {
	if segs, ok := st.Files[file]; ok {
		for _, s := range segs {
			st.Totals[s.Author] -= s.N
			if st.Totals[s.Author] <= 0 {
				delete(st.Totals, s.Author)
			}
		}
		delete(st.Files, file)
	}
}

// ── diff parsing ──────────────────────────────────────────────────────────────

// parseHunkHeader parses "@@ -A[,B] +C[,D] @@ ..." into oldStart, oldCount, newCount.
// When the count is omitted it means 1 (git convention).
func parseHunkHeader(line string) (oldStart, oldCount, newCount int) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return
	}
	parseRange := func(s string) (start, count int) {
		s = s[1:] // strip leading '-' or '+'
		if i := strings.IndexByte(s, ','); i >= 0 {
			start, _ = strconv.Atoi(s[:i])
			count, _ = strconv.Atoi(s[i+1:])
		} else {
			start, _ = strconv.Atoi(s)
			count = 1
		}
		return
	}
	oldStart, oldCount = parseRange(fields[1])
	_, newCount = parseRange(fields[2])
	return
}

// applyDiff applies a unified diff (-U0) to state, attributing all new lines to author.
//
// Handles: new files, deleted files, renames, modifications, binary files (ignored).
func applyDiff(st *State, diff []byte, author string) {
	sc := bufio.NewScanner(bytes.NewReader(diff))
	sc.Buffer(make([]byte, 64<<20), 64<<20) // 64 MB — handles minified/generated single-line files

	var (
		curFile    string
		renameFrom string
		isDel      bool   // current file is being deleted
		offset     int    // running line-number delta for curFile in this diff
	)

	for sc.Scan() {
		line := sc.Text()

		switch {
		case strings.HasPrefix(line, "diff --git "):
			// Start of a new file section — reset per-file state.
			curFile = ""
			renameFrom = ""
			isDel = false
			offset = 0

		case strings.HasPrefix(line, "deleted file mode"):
			isDel = true

		case strings.HasPrefix(line, "rename from "):
			renameFrom = strings.TrimPrefix(line, "rename from ")

		case strings.HasPrefix(line, "rename to "):
			to := strings.TrimPrefix(line, "rename to ")
			if renameFrom != "" {
				st.renameFile(renameFrom, to)
				curFile = to
				renameFrom = ""
			}

		case strings.HasPrefix(line, "--- "):
			src := strings.TrimPrefix(line, "--- ")
			if src != "/dev/null" && strings.HasPrefix(src, "a/") {
				curFile = src[2:]
			}

		case strings.HasPrefix(line, "+++ "):
			dst := strings.TrimPrefix(line, "+++ ")
			if dst != "/dev/null" && strings.HasPrefix(dst, "b/") {
				curFile = dst[2:]
			}

		case strings.HasPrefix(line, "@@ "):
			if curFile == "" {
				continue
			}
			oldStart, oldCount, newCount := parseHunkHeader(line)
			if isDel {
				newCount = 0 // deletion: consume old lines, produce none
			}
			st.applyHunk(curFile, oldStart, oldCount, newCount, author, &offset)
		}
	}
}

// ── git helpers ───────────────────────────────────────────────────────────────

// emptyTree is git's well-known SHA for the empty tree object.
// `git diff emptyTree HEAD` gives a diff of everything added in HEAD.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func gitCmd(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %v: %w — %s", args, err, strings.TrimSpace(errBuf.String()))
	}
	return out.Bytes(), nil
}

// CommitMeta is the metadata we need per commit.
type CommitMeta struct {
	Hash       string
	Parent     string // first parent hash; empty for root commits
	IsMerge    bool   // true when commit has 2+ parents
	AuthorEmail string
	AuthorName  string
	Date       time.Time
}

func listCommits(repo, branch string) ([]CommitMeta, error) {
	// Use tab (%x09) as field separator — safer than | since names can contain pipes.
	// Fields: hash, parents, author-email, author-name, author-date-ISO
	out, err := gitCmd(repo, "log", branch,
		"--format=%H%x09%P%x09%ae%x09%an%x09%aI",
		// No --first-parent: walk every commit reachable from branch so that
		// work done on feature branches is attributed to its actual authors.
		// Merge commits are detected below and skipped during diff application
		// to avoid double-counting the branch's changes.
		"--reverse",
	)
	if err != nil {
		return nil, err
	}
	var commits []CommitMeta
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 5)
		if len(parts) != 5 {
			continue
		}
		t, err := time.Parse(time.RFC3339, parts[4])
		if err != nil {
			continue
		}
		parents := strings.Fields(parts[1])
		parent := ""
		if len(parents) > 0 {
			parent = parents[0]
		}
		commits = append(commits, CommitMeta{
			Hash:        parts[0],
			Parent:      parent,
			IsMerge:     len(parents) > 1,
			AuthorEmail: parts[2],
			AuthorName:  parts[3],
			Date:        t,
		})
	}
	return commits, nil
}

func getDiff(repo, parent, hash string) ([]byte, error) {
	base := parent
	if base == "" {
		base = emptyTree
	}
	return gitCmd(repo, "diff", base, hash,
		"-U0",              // no context lines — only actual changes
		"--find-renames",   // detect renames (M factor default 50%)
		"--no-color",
	)
}

// ── snapshots & chart data ────────────────────────────────────────────────────

type Snapshot struct {
	Label  string
	Totals map[string]int
	Total  int
}

// Member is one author inside the "Others" group.
type member struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	FinalLines int    `json:"finalLines"`
}

// chartDataset carries both the % and absolute line-count series for one author,
// plus display metadata. The JS switches between pctData and absData on toggle.
// Members is non-nil only for the synthetic "Others" dataset.
type chartDataset struct {
	Label           string    `json:"label"`
	Email           string    `json:"email"`
	PctData         []float64 `json:"pctData"`
	AbsData         []float64 `json:"absData"`
	Fill            bool      `json:"fill"`
	BackgroundColor string    `json:"backgroundColor"`
	BorderColor     string    `json:"borderColor"`
	BorderWidth     float64  `json:"borderWidth"`
	Tension         float64  `json:"tension"`
	Members         []member `json:"members,omitempty"` // only for the "Others" group
}

type chartData struct {
	Labels   []string       `json:"labels"`
	Datasets []chartDataset `json:"datasets"`
}

var palette = []string{
	"#e94560", "#3ed6a0", "#3b82f6", "#fbbf24", "#a78bfa",
	"#fb7185", "#22d3ee", "#a3e635", "#f97316", "#ec4899",
	"#10b981", "#6366f1", "#f59e0b", "#ef4444", "#14b8a6",
	"#8b5cf6", "#06b6d4", "#84cc16", "#f43f5e", "#0ea5e9",
}

func hexRGBA(hex string, a float64) string {
	hex = strings.TrimPrefix(hex, "#")
	var r, g, b int
	fmt.Sscanf(hex[0:2], "%x", &r)
	fmt.Sscanf(hex[2:4], "%x", &g)
	fmt.Sscanf(hex[4:6], "%x", &b)
	return fmt.Sprintf("rgba(%d,%d,%d,%.2f)", r, g, b, a)
}

func buildChart(snaps []Snapshot, emailToName map[string]string, minPct float64) chartData {
	// displayName resolves an email to a human name.
	displayName := func(email string) string {
		if n := emailToName[email]; n != "" {
			return n
		}
		if at := strings.LastIndex(email, "@"); at >= 0 {
			return email[:at]
		}
		return email
	}

	// sumSnap returns the total lines across a slice of emails at one snapshot.
	sumSnap := func(s Snapshot, emails []string) int {
		n := 0
		for _, e := range emails {
			n += s.Totals[e]
		}
		return n
	}

	// Collect per-email totals (sum across all snapshots — proxy for ranking).
	emailTotals := make(map[string]int)
	for _, s := range snaps {
		for a, n := range s.Totals {
			emailTotals[a] += n
		}
	}

	// Merge emails that share the same display name into one entry.
	type group struct {
		name   string
		emails []string
		total  int // sum of emailTotals across group
	}
	nameToGroup := make(map[string]*group)
	for email, t := range emailTotals {
		name := displayName(email)
		g, ok := nameToGroup[name]
		if !ok {
			g = &group{name: name}
			nameToGroup[name] = g
		}
		g.emails = append(g.emails, email)
		g.total += t
	}
	groups := make([]*group, 0, len(nameToGroup))
	for _, g := range nameToGroup {
		sort.Strings(g.emails) // deterministic order
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].total > groups[j].total })

	labels := make([]string, len(snaps))
	for i, s := range snaps {
		labels[i] = s.Label
	}

	// Split into major (ever exceeded minPct) and minor (never did).
	var major, minor []*group
	for _, g := range groups {
		var peak float64
		for _, s := range snaps {
			if s.Total > 0 {
				if p := float64(sumSnap(s, g.emails)) / float64(s.Total) * 100; p > peak {
					peak = p
				}
			}
		}
		if peak >= minPct {
			major = append(major, g)
		} else {
			minor = append(minor, g)
		}
	}

	ds := make([]chartDataset, 0, len(major)+1)
	for i, g := range major {
		color := palette[i%len(palette)]
		pct := make([]float64, len(snaps))
		abs := make([]float64, len(snaps))
		for j, s := range snaps {
			v := sumSnap(s, g.emails)
			abs[j] = float64(v)
			if s.Total > 0 {
				pct[j] = float64(v) / float64(s.Total) * 100
			}
		}
		ds = append(ds, chartDataset{
			Label:           g.name,
			Email:           strings.Join(g.emails, ", "),
			PctData:         pct,
			AbsData:         abs,
			Fill:            true,
			BackgroundColor: hexRGBA(color, 0.75),
			BorderColor:     color,
			BorderWidth:     1.5,
			Tension:         0.3,
		})
	}

	// Build the "Others" band from all minor groups.
	if len(minor) > 0 {
		pct := make([]float64, len(snaps))
		abs := make([]float64, len(snaps))
		for j, s := range snaps {
			for _, g := range minor {
				abs[j] += float64(sumSnap(s, g.emails))
			}
			if s.Total > 0 {
				pct[j] = abs[j] / float64(s.Total) * 100
			}
		}
		last := snaps[len(snaps)-1]
		members := make([]member, 0, len(minor))
		for _, g := range minor {
			members = append(members, member{
				Name:       g.name,
				Email:      strings.Join(g.emails, ", "),
				FinalLines: sumSnap(last, g.emails),
			})
		}
		sort.Slice(members, func(i, j int) bool { return members[i].FinalLines > members[j].FinalLines })
		ds = append(ds, chartDataset{
			Label:           fmt.Sprintf("Others (%d authors)", len(minor)),
			Email:           "",
			PctData:         pct,
			AbsData:         abs,
			Fill:            true,
			BackgroundColor: hexRGBA("#94a3b8", 0.45),
			BorderColor:     "#94a3b8",
			BorderWidth:     1,
			Tension:         0.3,
			Members:         members,
		})
	}

	return chartData{Labels: labels, Datasets: ds}
}

// ── HTML output ───────────────────────────────────────────────────────────────

//go:embed template.html
var htmlTpl string

//go:embed chartjs.min.js
var chartJS string

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	repoFlag := flag.String("repo", ".", "path to git repository")
	branchFlag := flag.String("branch", "HEAD", "branch/ref to analyse")
	outputFlag := flag.String("output", "", "output HTML file (default: <reponame>.html)")
	maxPtsFlag := flag.Int("max-points", 1000,
		"max chart data points — commits are strided to fit; 0 = record every commit")
	minPctFlag := flag.Float64("min-pct", 1.0,
		"authors who never exceeded this %% of total lines are grouped into 'Others'")
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

	// Default output filename: <reponame>.html in the current directory.
	outFile := *outputFlag
	if outFile == "" {
		outFile = filepath.Base(absRepo) + ".html"
	}

	fmt.Printf("Repository : %s\n", absRepo)
	fmt.Printf("Branch     : %s\n", *branchFlag)

	// 1. List all commits in chronological order.
	fmt.Print("Loading commit history… ")
	commits, err := listCommits(absRepo, *branchFlag)
	if err != nil {
		log.Fatalf("\nfailed to list commits: %v", err)
	}
	fmt.Printf("%d commits\n", len(commits))

	if len(commits) == 0 {
		log.Fatal("no commits found")
	}

	// 2. Determine snapshot stride.
	stride := 1
	if *maxPtsFlag > 0 && len(commits) > *maxPtsFlag {
		stride = len(commits) / *maxPtsFlag
	}
	if stride > 1 {
		fmt.Printf("Stride     : every %d commits (~%d snapshots)\n", stride, len(commits)/stride)
	} else {
		fmt.Printf("Snapshots  : every commit\n")
	}
	fmt.Println()

	// 3. Replay history, applying diffs incrementally.
	state := newState()
	var snaps []Snapshot
	// emailToName maps author email → most-recently-seen display name.
	emailToName := make(map[string]string)

	start := time.Now()
	errCount := 0

	for i, c := range commits {
		if c.AuthorName != "" {
			emailToName[c.AuthorEmail] = c.AuthorName
		}
		// Skip merge commits: each branch commit is already processed
		// individually, so applying the merge diff would double-count all
		// changes from the merged branch.  Conflict-resolution lines (the only
		// thing unique to the merge commit) are omitted — they're usually tiny.
		if c.IsMerge {
			goto snapshot
		}

		{
			diff, err := getDiff(absRepo, c.Parent, c.Hash)
			if err != nil {
				errCount++
				if errCount <= 5 {
					fmt.Printf("\nwarn: diff failed %s (%s): %v",
						c.Hash[:7], c.Date.Format("2006-01-02"), err)
				}
				goto snapshot
			}
			applyDiff(state, diff, c.AuthorEmail)
		}

	snapshot:
		// Record snapshot: always at first and last commit, otherwise per stride.
		wantSnap := (i == 0) || (i == len(commits)-1) || ((i+1)%stride == 0)
		if wantSnap {
			snaps = append(snaps, Snapshot{
				Label:  c.Date.Format("2006-01-02") + " " + c.Hash[:7],
				Totals: state.copyTotals(),
				Total:  state.totalLines(),
			})
		}

		// Progress every 200 commits.
		if (i+1)%200 == 0 || i == len(commits)-1 {
			elapsed := time.Since(start)
			rate := float64(i+1) / elapsed.Seconds()
			remaining := float64(len(commits)-i-1) / rate
			eta := time.Duration(remaining) * time.Second
			fmt.Printf("\r[%d/%d] %.0f commits/s  ETA %-8s  lines tracked: %-8d",
				i+1, len(commits), rate, eta.Round(time.Second), state.totalLines())
		}
	}
	fmt.Printf("\n\nDone in %s", time.Since(start).Round(time.Millisecond*100))
	if errCount > 0 {
		fmt.Printf(" (%d diff errors skipped)", errCount)
	}
	fmt.Printf("\nSnapshots  : %d\n", len(snaps))

	// 4. Build chart data.
	cd := buildChart(snaps, emailToName, *minPctFlag)

	// Count unique authors at last snapshot.
	authorCount := 0
	if len(snaps) > 0 {
		authorCount = len(snaps[len(snaps)-1].Totals)
	}

	jsonBytes, err := json.Marshal(cd)
	if err != nil {
		log.Fatalf("json marshal: %v", err)
	}

	// 5. Render HTML.
	type tplVars struct {
		Repo         string
		Branch       string
		ChartJS      template.JS
		TotalCommits int
		Samples      int
		Authors      int
		Generated    string
		DataJSON     template.JS
	}
	tmpl := template.Must(template.New("page").Parse(htmlTpl))
	outF, err := os.Create(outFile)
	if err != nil {
		log.Fatalf("create output file: %v", err)
	}
	defer outF.Close()

	if err := tmpl.Execute(outF, tplVars{
		Repo:         absRepo,
		Branch:       *branchFlag,
		ChartJS:      template.JS(chartJS),
		TotalCommits: len(commits),
		Samples:      len(snaps),
		Authors:      authorCount,
		Generated:    time.Now().Format("2006-01-02 15:04"),
		DataJSON:     template.JS(jsonBytes),
	}); err != nil {
		log.Fatalf("render template: %v", err)
	}

	fmt.Printf("Output     : %s\n", outFile)
}
