// chart.go — snapshot types and chart data builder.

package main

import (
	"fmt"
	"sort"
	"strings"
)

type Snapshot struct {
	Label  string
	Totals map[string]int
	Total  int
}

// member is one author inside the "Others" group.
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
	BorderWidth     float64   `json:"borderWidth"`
	Tension         float64   `json:"tension"`
	Members         []member  `json:"members,omitempty"` // only for the "Others" group
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
