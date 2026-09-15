package main

import (
	"maps"
	"slices"
	"testing"
)

func TestRenameAcrossFilterBoundary(t *testing.T) {
	filter, err := newPathFilter(nil, []string{"vendor/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	filtered := newState(filter)
	unfiltered := newState(&pathFilter{})

	steps := []struct {
		author string
		events []diffEvent
		want   map[string]int // filtered totals after this step
	}{
		{"alice", []diffEvent{{file: "vendor/a.txt", oldStart: 0, oldCount: 0, newCount: 10}},
			map[string]int{}},
		// Moving into a tracked path brings alice's lines along.
		{"bob", []diffEvent{{file: "vendor/a.txt", renameTo: "pkg/a.txt"}},
			map[string]int{"alice": 10}},
		// Later hunks apply against the real content.
		{"carol", []diffEvent{{file: "pkg/a.txt", oldStart: 1, oldCount: 1, newCount: 2}},
			map[string]int{"alice": 9, "carol": 2}},
		// Moving back out removes the lines from the totals...
		{"dave", []diffEvent{{file: "pkg/a.txt", renameTo: "vendor/b.txt"}},
			map[string]int{}},
		// ...and edits while excluded are remembered.
		{"erin", []diffEvent{{file: "vendor/b.txt", oldStart: 11, oldCount: 0, newCount: 3}},
			map[string]int{}},
		{"frank", []diffEvent{{file: "vendor/b.txt", renameTo: "lib/b.txt"}},
			map[string]int{"alice": 9, "carol": 2, "erin": 3}},
	}
	for i, s := range steps {
		applyEvents(filtered, s.author, s.events)
		applyEvents(unfiltered, s.author, s.events)
		if !maps.Equal(filtered.Totals, s.want) {
			t.Fatalf("step %d (%s): totals = %v, want %v", i, s.author, filtered.Totals, s.want)
		}
	}

	// Filtering must not change who owns what.
	if !maps.EqualFunc(filtered.Files, unfiltered.Files, slices.Equal[[]Seg]) {
		t.Errorf("tracked files differ from unfiltered run:\n%v\n%v", filtered.Files, unfiltered.Files)
	}
	if len(filtered.Untracked) != 0 {
		t.Errorf("untracked files left behind: %v", filtered.Untracked)
	}
}
