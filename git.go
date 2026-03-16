// git.go — git subprocess helpers and parallel streaming log parser.

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

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
	Hash        string
	Parent      string
	IsMerge     bool
	AuthorEmail string
	AuthorName  string
	Date        time.Time
}

// commitCount returns the number of commits reachable from branch.
func commitCount(repo, branch string) (int, error) {
	out, err := gitCmd(repo, "rev-list", "--count", branch)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("rev-list --count: unexpected output %q", out)
	}
	return n, nil
}

// getHashes returns all commit hashes oldest-first.
func getHashes(repo, branch string) ([]string, error) {
	out, err := gitCmd(repo, "log", branch, "--format=%H", "--reverse")
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\n"), nil
}

// ── diff event types ──────────────────────────────────────────────────────────

// diffEvent is either a hunk application or a file rename, pre-parsed from diff
// output. Storing events instead of raw bytes lets workers parse in parallel
// while the main goroutine applies them cheaply in order.
type diffEvent struct {
	file     string // hunk: file path  |  rename: from-path
	renameTo string // non-empty ⟹ rename event
	oldStart int
	oldCount int
	newCount int
}

type parsedCommit struct {
	meta   CommitMeta
	events []diffEvent
}

// ── byte-slice diff header prefixes (avoid sc.Text() allocation per line) ────

var (
	bDiffGit     = []byte("diff --git ")
	bDeletedFile = []byte("deleted file mode")
	bRenameFrom  = []byte("rename from ")
	bRenameTo    = []byte("rename to ")
	bMinus       = []byte("--- ")
	bPlus        = []byte("+++ ")
	bHunk        = []byte("@@ ")
	bDevNull     = []byte("/dev/null")
)

// parseHunk parses "@@ -A[,B] +C[,D] @@…" without allocations.
func parseHunk(b []byte) (oldStart, oldCount, newCount int) {
	i := 3 // skip "@@ "
	if i >= len(b) || b[i] != '-' {
		return
	}
	i++
	readInt := func() int {
		n := 0
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			n = n*10 + int(b[i]-'0')
			i++
		}
		return n
	}
	oldStart = readInt()
	oldCount = 1
	if i < len(b) && b[i] == ',' {
		i++
		oldCount = readInt()
	}
	for i < len(b) && b[i] != '+' {
		i++
	}
	i++ // skip '+'
	for i < len(b) && b[i] != ',' && b[i] != ' ' {
		i++
	}
	newCount = 1
	if i < len(b) && b[i] == ',' {
		i++
		newCount = readInt()
	}
	return
}

// ── per-chunk worker ──────────────────────────────────────────────────────────

// fetchChunk runs one git log -p process covering endHash (inclusive) but
// excluding excludeHash and its ancestors (empty string = no exclusion).
// Parsed commits are sent on the returned channel; channel is closed on finish.
func fetchChunk(repo, endHash, excludeHash string) <-chan parsedCommit {
	ch := make(chan parsedCommit, 64)
	go func() {
		defer close(ch)
		args := []string{
			"log",
			"--format=%x00%H%x09%P%x09%ae%x09%an%x09%aI",
			"-p", "-U0", "--reverse", "--find-renames", "--no-color",
			endHash,
		}
		if excludeHash != "" {
			args = append(args, "--not", excludeHash)
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return
		}
		if err := cmd.Start(); err != nil {
			return
		}

		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<20), 64<<20)

		var (
			cur        CommitMeta
			hasCur     bool
			events     []diffEvent
			curFile    string
			renameFrom string
			isDel      bool
		)
		resetDiff := func() { curFile = ""; renameFrom = ""; isDel = false }

		for sc.Scan() {
			b := sc.Bytes()
			if len(b) == 0 {
				continue
			}
			if b[0] == 0 {
				if hasCur {
					ch <- parsedCommit{cur, events}
				}
				hasCur = false
				events = nil
				resetDiff()

				parts := strings.SplitN(string(b[1:]), "\t", 5)
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
				cur = CommitMeta{
					Hash:        parts[0],
					Parent:      parent,
					IsMerge:     len(parents) > 1,
					AuthorEmail: parts[2],
					AuthorName:  parts[3],
					Date:        t,
				}
				hasCur = true
				continue
			}

			if !hasCur || cur.IsMerge {
				continue
			}

			switch {
			case bytes.HasPrefix(b, bDiffGit):
				resetDiff()
			case bytes.HasPrefix(b, bDeletedFile):
				isDel = true
			case bytes.HasPrefix(b, bRenameFrom):
				renameFrom = string(b[len(bRenameFrom):])
			case bytes.HasPrefix(b, bRenameTo):
				to := string(b[len(bRenameTo):])
				if renameFrom != "" {
					events = append(events, diffEvent{file: renameFrom, renameTo: to})
					curFile = to
					renameFrom = ""
				}
			case bytes.HasPrefix(b, bMinus):
				src := b[len(bMinus):]
				if !bytes.Equal(src, bDevNull) && len(src) > 2 && src[0] == 'a' && src[1] == '/' {
					curFile = string(src[2:])
				}
			case bytes.HasPrefix(b, bPlus):
				dst := b[len(bPlus):]
				if !bytes.Equal(dst, bDevNull) && len(dst) > 2 && dst[0] == 'b' && dst[1] == '/' {
					curFile = string(dst[2:])
				}
			case bytes.HasPrefix(b, bHunk):
				if curFile == "" {
					continue
				}
				oldStart, oldCount, newCount := parseHunk(b)
				if isDel {
					newCount = 0
				}
				events = append(events, diffEvent{
					file: curFile, oldStart: oldStart, oldCount: oldCount, newCount: newCount,
				})
			}
		}
		if hasCur {
			ch <- parsedCommit{cur, events}
		}
		cmd.Wait()
	}()
	return ch
}

// applyEvents applies pre-parsed diff events to state on behalf of author.
func applyEvents(state *State, author string, events []diffEvent) {
	offsets := make(map[string]int)
	for _, e := range events {
		if e.renameTo != "" {
			state.renameFile(e.file, e.renameTo)
			delete(offsets, e.file)
			continue
		}
		off := offsets[e.file]
		state.applyHunk(e.file, e.oldStart, e.oldCount, e.newCount, author, &off)
		offsets[e.file] = off
	}
}

// ── parallel coordinator ──────────────────────────────────────────────────────

// streamLog splits history into workers chunks, fetches them concurrently,
// and applies commits in order via fn. State is mutated synchronously.
func streamLog(repo, branch string, workers int, state *State, fn func(CommitMeta) error) error {
	hashes, err := getHashes(repo, branch)
	if err != nil {
		return err
	}
	if len(hashes) == 0 {
		return nil
	}

	if workers < 1 {
		workers = 1
	}
	if workers > len(hashes) {
		workers = len(hashes)
	}

	// Split hashes into workers chunks; last chunk absorbs the remainder.
	chunkSize := len(hashes) / workers
	type chunk struct {
		endHash     string
		excludeHash string // last hash of previous chunk, or ""
	}
	chunks := make([]chunk, workers)
	for k := range chunks {
		end := (k+1)*chunkSize - 1
		if k == workers-1 {
			end = len(hashes) - 1
		}
		exclude := ""
		if k > 0 {
			exclude = hashes[k*chunkSize-1]
		}
		chunks[k] = chunk{endHash: hashes[end], excludeHash: exclude}
	}

	// Launch all workers now so git processes overlap with each other
	// and with our application work.
	channels := make([]<-chan parsedCommit, workers)
	for k, c := range chunks {
		channels[k] = fetchChunk(repo, c.endHash, c.excludeHash)
	}

	// Apply chunks in order.
	for _, ch := range channels {
		for pc := range ch {
			if !pc.meta.IsMerge {
				applyEvents(state, pc.meta.AuthorEmail, pc.events)
			}
			if err := fn(pc.meta); err != nil {
				return err
			}
		}
	}
	return nil
}
