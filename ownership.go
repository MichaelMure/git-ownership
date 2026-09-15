// ownership.go — run-length encoded per-file line ownership and repo state.

package main

import "strings"

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
			dFrom := max(cur, pos)
			dTo := min(hi, end)
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


// State is the full ownership picture of the repo at a point in time.
//
// Files excluded by the filter are still replayed into Untracked, but don't
// count towards Totals. That way a file moved across the filter boundary keeps
// its original authors, and filtering never changes who owns a line.
type State struct {
	Files     map[string][]Seg // tracked filepath → RLE ownership
	Untracked map[string][]Seg // excluded filepath → RLE ownership
	Totals    map[string]int   // author email → total lines in tracked files

	filter *pathFilter
}

func newState(filter *pathFilter) *State {
	return &State{
		Files:     make(map[string][]Seg),
		Untracked: make(map[string][]Seg),
		Totals:    make(map[string]int),
		filter:    filter,
	}
}

// filesFor returns the map holding file, and whether it counts towards Totals.
func (st *State) filesFor(file string) (map[string][]Seg, bool) {
	if st.filter.excluded(file) {
		return st.Untracked, false
	}
	return st.Files, true
}

func (st *State) subTotal(author string, n int) {
	st.Totals[author] -= n
	if st.Totals[author] <= 0 {
		delete(st.Totals, author)
	}
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
//
//	offset += A - D
func (st *State) applyHunk(file string, oldStart, oldCount, newCount int, author string, offset *int) {
	files, tracked := st.filesFor(file)
	segs := files[file]

	if oldCount > 0 {
		delPos := oldStart - 1 + *offset
		if delPos < 0 {
			delPos = 0
		}
		newSegs, removed := deleteRange(segs, delPos, oldCount)
		segs = newSegs
		if tracked {
			for a, n := range removed {
				st.subTotal(a, n)
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
		if tracked {
			st.Totals[author] += newCount
		}
	}

	*offset += newCount - oldCount

	if len(segs) == 0 {
		delete(files, file)
	} else {
		files[file] = segs
	}
}

// renameFile moves ownership from one path to another, updating Totals when
// the file crosses the filter boundary.
func (st *State) renameFile(from, to string) {
	src, fromTracked := st.filesFor(from)
	dst, toTracked := st.filesFor(to)
	segs, ok := src[from]
	if !ok {
		return
	}
	delete(src, from)
	dst[to] = segs
	if fromTracked == toTracked {
		return
	}
	for _, s := range segs {
		if toTracked {
			st.Totals[s.Author] += s.N
		} else {
			st.subTotal(s.Author, s.N)
		}
	}
}

// computeDirTotals returns per-directory author line counts for a targeted set
// of directories. Only directories present in trackDirs are computed, so the
// caller controls memory by passing only the directories it cares about.
func (st *State) computeDirTotals(trackDirs map[string]bool) map[string]map[string]int {
	result := make(map[string]map[string]int)
	for file, segs := range st.Files {
		// Walk every ancestor directory of this file (deepest → shallowest).
		prefix := file
		for {
			i := strings.LastIndexByte(prefix, '/')
			if i <= 0 {
				break
			}
			prefix = prefix[:i]
			if !trackDirs[prefix] {
				continue
			}
			m := result[prefix]
			if m == nil {
				m = make(map[string]int)
				result[prefix] = m
			}
			for _, seg := range segs {
				m[seg.Author] += seg.N
			}
		}
	}
	return result
}
