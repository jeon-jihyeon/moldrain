package main

import (
	"fmt"
	"strings"
)

// The tables of a run
// 1. accuracy per dataset
// 2. averages per contender
// 3. gates
// 4. Match scaling
// 5. the oracle
func markdown(res Run) string {
	var b strings.Builder
	rows := map[string]map[string]Row{}
	var datasets []string
	for _, r := range res.Rows {
		if rows[r.Dataset] == nil {
			rows[r.Dataset] = map[string]Row{}
			datasets = append(datasets, r.Dataset)
		}
		rows[r.Dataset][r.Contender] = r
	}
	fmt.Fprintf(&b, "Run %s on %s, %s, Python %s with Drain3 %s, faceair/drain %s, commit %s\n\n",
		res.Date, res.Machine, res.Go, res.Python, res.Drain3, res.Faceair, res.Commit)

	heads := []string{"moldrain", "drain3", "faceair"}
	b.WriteString("| Dataset | Lines | GA moldrain / Drain3 / faceair | FGA | PA | FTA |\n|---|---|---|---|---|---|\n")
	for _, ds := range datasets {
		r := rows[ds]
		cell := func(get func(Row) float64) string {
			var parts []string
			for _, c := range heads {
				if x, ok := r[c]; ok && x.Error == "" {
					parts = append(parts, fmt.Sprintf("%.3f", get(x)))
				} else {
					parts = append(parts, "-")
				}
			}
			return strings.Join(parts, " / ")
		}
		lines := 0
		for _, x := range r {
			lines = x.Lines
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s | %s |\n", ds, lines,
			cell(func(x Row) float64 { return x.GA }), cell(func(x Row) float64 { return x.FGA }),
			cell(func(x Row) float64 { return x.PA }), cell(func(x Row) float64 { return x.FTA }))
	}

	b.WriteString("\n| Contender | Avg GA | Avg FGA | Avg PA | Avg FTA | Templates per event | Lines per second | Allocs per line | Bytes per line |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range allContenders {
		var n int
		var ga, fga, pa, fta, tpe, allocs, bytes, lines, seconds float64
		for _, ds := range datasets {
			x, ok := rows[ds][c]
			if !ok || x.Error != "" {
				continue
			}
			n++
			ga, fga, pa, fta, tpe = ga+x.GA, fga+x.FGA, pa+x.PA, fta+x.FTA, tpe+x.TemplatesPerEvent
			allocs += x.AllocsPerLine * float64(x.Lines)
			bytes += x.BytesPerLine * float64(x.Lines)
			lines += float64(x.Lines)
			seconds += float64(x.Lines) / x.LinesPerSec
		}
		if n == 0 {
			continue
		}
		k := float64(n)
		mem := "- | -"
		if allocs > 0 {
			mem = fmt.Sprintf("%.1f | %.0f", allocs/lines, bytes/lines)
		}
		// the time of a contender with a resolver includes the resolver's
		speed := "-"
		if !resolved[c] {
			speed = fmt.Sprintf("%.0f", lines/seconds)
		}
		fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f | %.3f | %.2f | %s | %s |\n",
			c, ga/k, fga/k, pa/k, fta/k, tpe/k, speed, mem)
	}

	b.WriteString("\n| Gated option | Avg FGA with | Avg FGA without | Largest GA drop | Passes |\n|---|---|---|---|---|\n")
	for _, opt := range []string{"respecialize", "spans", "punct"} {
		var with, without float64
		n := 0
		worst, worstDS := 0.0, "-"
		for _, ds := range datasets {
			x, ok1 := rows[ds][opt]
			base, ok2 := rows[ds]["moldrain"]
			if !ok1 || !ok2 || x.Error != "" || base.Error != "" {
				continue
			}
			n++
			with += x.FGA
			without += base.FGA
			if d := base.GA - x.GA; d > worst {
				worst, worstDS = d, ds
			}
		}
		if n == 0 {
			continue
		}
		pass := with/float64(n) >= without/float64(n) && worst <= 0.02
		fmt.Fprintf(&b, "| %s | %.3f | %.3f | %.3f %s | %v |\n", opt, with/float64(n), without/float64(n), worst, worstDS, pass)
	}

	b.WriteString("\n| Dataset | Match per second at 1 | at 4 | at 8 | 8 over 1 |\n|---|---|---|---|---|\n")
	for _, ds := range datasets {
		x, ok := rows[ds]["moldrain"]
		if !ok || x.MatchPerSec == nil {
			continue
		}
		one, four, eight := x.MatchPerSec["1"], x.MatchPerSec["4"], x.MatchPerSec["8"]
		fmt.Fprintf(&b, "| %s | %.0f | %.0f | %.0f | %.1f |\n", ds, one, four, eight, eight/one)
	}

	b.WriteString("\n| Dataset | Templates per event without / with oracle | GA without / with | FGA without / with |\n|---|---|---|---|\n")
	for _, ds := range datasets {
		x, ok1 := rows[ds]["oracle"]
		base, ok2 := rows[ds]["moldrain"]
		if !ok1 || !ok2 {
			continue
		}
		fmt.Fprintf(&b, "| %s | %.2f / %.2f | %.3f / %.3f | %.3f / %.3f |\n", ds,
			base.TemplatesPerEvent, x.TemplatesPerEvent, base.GA, x.GA, base.FGA, x.FGA)
	}
	return b.String()
}
