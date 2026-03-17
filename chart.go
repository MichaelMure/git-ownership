// chart.go — snapshot types and chart data builder.

package main

import (
	"fmt"
	"sort"
	"strings"
)

type Snapshot struct {
	Label     string
	Totals    map[string]int            // global author → lines
	Total     int                       // total lines across all authors
	DirTotals map[string]map[string]int // root-dir → author → lines (not serialised)
}

// folderData is one entry in the per-folder chart array embedded in the HTML.
type folderData struct {
	Path  string    `json:"path"`
	Chart chartData `json:"chart"`
}

// buildAllFolderData builds chart data for "/" (whole repo) followed by one
// entry per selected directory, in the order provided by selectedDirs.
func buildAllFolderData(snaps []Snapshot, emailToName map[string]string, maxBands int, selectedDirs []string) []folderData {
	// Compute a stable global author ranking from the root-level snapshots so
	// that every folder uses the same dataset order and palette colours.
	displayName := func(email string) string {
		if n := emailToName[email]; n != "" {
			return n
		}
		if at := strings.LastIndex(email, "@"); at >= 0 {
			return email[:at]
		}
		return email
	}
	nameTotals := make(map[string]int)
	for _, s := range snaps {
		for email, n := range s.Totals {
			nameTotals[displayName(email)] += n
		}
	}
	type nameTotal struct{ name string; total int }
	ranked := make([]nameTotal, 0, len(nameTotals))
	for name, total := range nameTotals {
		ranked = append(ranked, nameTotal{name, total})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].total > ranked[j].total })
	globalRank := make(map[string]int, len(ranked))
	for i, nt := range ranked {
		globalRank[nt.name] = i
	}

	folders := []folderData{
		{Path: "/", Chart: buildChart(snaps, emailToName, maxBands, globalRank)},
	}
	for _, dir := range selectedDirs {
		dirSnaps := make([]Snapshot, len(snaps))
		for i, s := range snaps {
			totals := s.DirTotals[dir]
			total := 0
			for _, n := range totals {
				total += n
			}
			dirSnaps[i] = Snapshot{Label: s.Label, Totals: totals, Total: total}
		}
		folders = append(folders, folderData{
			Path:  dir,
			Chart: buildChart(dirSnaps, emailToName, maxBands, globalRank),
		})
	}
	return folders
}


// chartDataset carries both the % and absolute line-count series for one author,
// plus display metadata. The JS switches between pctData and absData on toggle.
type chartDataset struct {
	Label           string    `json:"label"`
	Email           string    `json:"email"`
	PctData         []float64 `json:"pctData"`
	AbsData         []float64 `json:"absData"`
	Fill            bool      `json:"fill"`
	BackgroundColor string    `json:"backgroundColor"`
	BorderColor     string    `json:"borderColor"`
	BorderWidth     float64   `json:"borderWidth"`
	Tension         float64   `json:"tension"`
}

type chartData struct {
	Labels   []string       `json:"labels"`
	Datasets []chartDataset `json:"datasets"`
	TotalAbs []float64      `json:"totalAbs"` // total lines across ALL authors per snapshot (incl. excluded)
}

var palette = []string{
	"#e94560", "#3ed6a0", "#3b82f6", "#fbbf24", "#a78bfa",
	"#fb7185", "#22d3ee", "#a3e635", "#f97316", "#ec4899",
	"#10b981", "#6366f1", "#f59e0b", "#ef4444", "#14b8a6",
	"#8b5cf6", "#06b6d4", "#84cc16", "#f43f5e", "#0ea5e9",
}

// hexPastel returns a pastel version of a hex colour: 82% original + 18% white.
func hexPastel(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	var r, g, b int
	fmt.Sscanf(hex[0:2], "%x", &r)
	fmt.Sscanf(hex[2:4], "%x", &g)
	fmt.Sscanf(hex[4:6], "%x", &b)
	return fmt.Sprintf("rgba(%d,%d,%d,0.90)",
		int(float64(r)*0.82+255*0.18),
		int(float64(g)*0.82+255*0.18),
		int(float64(b)*0.82+255*0.18),
	)
}

func buildChart(snaps []Snapshot, emailToName map[string]string, maxBands int, globalRank map[string]int) chartData {
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
	// Sort by global rank so dataset order (= stack order) is identical across
	// all folders. Authors absent from globalRank fall back to local total.
	sort.Slice(groups, func(i, j int) bool {
		ri, oki := globalRank[groups[i].name]
		rj, okj := globalRank[groups[j].name]
		if oki && okj { return ri < rj }
		if oki { return true }
		if okj { return false }
		return groups[i].total > groups[j].total
	})

	labels := make([]string, len(snaps))
	for i, s := range snaps {
		labels[i] = s.Label
	}

	// Cap to maxBands if specified; JS will handle the Others grouping dynamically.
	if maxBands > 0 && len(groups) > maxBands {
		groups = groups[:maxBands]
	}

	ds := make([]chartDataset, 0, len(groups))
	nextUnranked := len(globalRank)
	for _, g := range groups {
		rank, ok := globalRank[g.name]
		if !ok {
			rank = nextUnranked
			nextUnranked++
		}
		color := palette[rank%len(palette)]
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
			BackgroundColor: hexPastel(color),
			BorderColor:     color,
			BorderWidth:     1.5,
			Tension:         0.3,
		})
	}

	totalAbs := make([]float64, len(snaps))
	for j, s := range snaps {
		totalAbs[j] = float64(s.Total)
	}
	return chartData{Labels: labels, Datasets: ds, TotalAbs: totalAbs}
}
