package main

import (
	"fmt"
	"math"
	"sort"

	"github.com/jung-kurt/gofpdf"
)

func renderModel(pdf *gofpdf.Fpdf, tr func(string) string, cfg *Config, results map[string]Metrics, names []string, cols map[string][3]int) {
	// The production model is the core EE-vs-DCE story, so it renders by default even
	// with no model: block — using assumed figures the reader should override in bench.yaml.
	mdl := cfg.Model
	if mdl == nil {
		mdl = &Model{}
	}
	devs := mdl.Devs
	if devs <= 0 {
		devs = 5000 // default developer population; set model.devs to the customer's real count
	}
	apd := mdl.AnalysesPerDevDay
	if apd == 0 {
		apd = 15 // default assumption; override with the customer's real figure
	}
	pf := mdl.PeakFraction
	if pf == 0 {
		pf = 0.25 // share of a day's analyses in the busiest hour; override with real data
	}
	daily := float64(devs) * apd
	peak := daily * pf

	var T float64
	var tSource string
	switch {
	case mdl.CeSeconds > 0:
		T = mdl.CeSeconds
		tSource = fmt.Sprintf("%.1fs/analysis (configured)", T)
	case mdl.PRCeSeconds > 0 && mdl.BranchCeSeconds > 0:
		// blended PR/branch cost: real traffic is mostly cheap PR analyses
		f := mdl.PRFraction
		if f <= 0 || f > 1 {
			f = 0.9
		}
		T = f*mdl.PRCeSeconds + (1-f)*mdl.BranchCeSeconds
		tSource = fmt.Sprintf("%.1fs/analysis blended (%.0f%% PRs @ %.1fs + %.0f%% branch @ %.1fs)",
			T, f*100, mdl.PRCeSeconds, (1-f)*100, mdl.BranchCeSeconds)
	default:
		base := names[0]
		for _, n := range names {
			if results[n].Workers < results[base].Workers {
				base = n
			}
		}
		T = results[base].ProcAvg
		if T == 0 {
			T = 5.0
		}
		tSource = fmt.Sprintf("%.1fs/analysis (measured this run)", T)
	}
	c1 := 3600.0 / T
	capf := func(w float64) float64 { return w * c1 }
	dmin := func(lam, w float64) float64 {
		c := capf(w)
		if lam <= c {
			return 0
		}
		return (lam - c) / (2 * c) * 60
	}
	util := func(w float64) float64 { return peak / capf(w) * 100 }
	utilCell := func(w int) string {
		u := util(float64(w))
		if u > 100 {
			return fmt.Sprintf("%.0f%% (over)", u)
		}
		return fmt.Sprintf("%.0f%%", u)
	}
	delayCell := func(w int) string {
		c := capf(float64(w))
		d := dmin(peak, float64(w))
		mx := 0.0
		if peak > c {
			mx = (peak - c) / c * 60
		}
		if d >= 1 {
			return fmt.Sprintf("~%.0f min (max ~%.0f min)", d, mx)
		}
		return "< 1 s"
	}
	topo := func(name string, nodes, wpn, w int) string {
		if nodes <= 1 {
			return fmt.Sprintf("%s — 1 node, %d workers", name, w)
		}
		return fmt.Sprintf("%s — %d nodes × %d (%d workers)", name, nodes, wpn, w)
	}

	Wp := int(math.Ceil(peak / c1))
	if Wp < 1 {
		Wp = 1
	}

	ee, dce := "", ""
	for _, n := range names {
		nodes := results[n].AppNodes
		if nodes <= 1 && ee == "" {
			ee = n
		}
		if nodes > 1 && dce == "" {
			dce = n
		}
	}
	dceW, eeW := 0, 0
	if dce != "" {
		dceW = results[dce].Workers
	}
	if ee != "" {
		eeW = results[ee].Workers
	}

	byTotal := map[int][][2]int{}
	for nodes := 3; nodes <= 6; nodes++ {
		for wpn := 3; wpn <= 6; wpn++ {
			tot := nodes * wpn
			if u := util(float64(tot)); u >= 30 && u <= 100 {
				byTotal[tot] = append(byTotal[tot], [2]int{nodes, wpn})
			}
		}
	}
	recIsMeasured := dceW > 0 && util(float64(dceW)) <= 70
	floor := 0
	if recIsMeasured {
		floor = dceW
	}
	var totals []int
	for tot := range byTotal {
		totals = append(totals, tot)
	}
	sort.Ints(totals)
	var est []int
	for _, tot := range totals {
		if tot > floor && tot != dceW {
			est = append(est, tot)
		}
	}
	if len(est) > 4 {
		est = est[:4]
	}
	// build table rows: measured configs + one DCE sizing estimate per distinct capacity
	rows := [][]string{{"Configuration", "Peak capacity", "Utilisation", "Avg feedback delay", "Type"}}
	add := func(label string, w int, typ string) {
		rows = append(rows, []string{label, commas(capf(float64(w))) + "/hr", utilCell(w), delayCell(w), typ})
	}
	for _, n := range names {
		m := results[n]
		w := m.Workers
		if w == 0 {
			w = 1
		}
		nodes := m.AppNodes
		if nodes == 0 {
			nodes = 1
		}
		wpn := m.WorkersPerNode
		if wpn == 0 {
			wpn = w
		}
		add(topo(n, nodes, wpn, w), w, "measured")
	}
	if ee != "" && eeW > 0 && eeW < Wp {
		add(fmt.Sprintf("EE — 1 node, %d workers (single node · no HA)", Wp), Wp, "estimate")
	}
	for _, tot := range est {
		nodes, wpn := byTotal[tot][0][0], byTotal[tot][0][1]
		add(topo("DCE", nodes, wpn, tot), tot, "estimate")
	}

	// ---- render ----
	pdf.Ln(3)
	hr(pdf)
	pdf.SetFont("Helvetica", "B", 13)
	setText(pdf, [3]int{0x1F, 0x8F, 0xD6})
	pdf.CellFormat(usableW, 6, tr(fmt.Sprintf("Production model — %s developers", commas(float64(devs)))), "", 1, "L", false, 0, "")
	pdf.SetFont("Helvetica", "", 10)
	setText(pdf, ink)
	pdf.MultiCell(usableW, 5, tr(fmt.Sprintf(
		"Assumptions: %s developers × %s analyses/day = %s/day; ~%.0f%% land in the peak hour ~ %s analyses/hr; "+
			"%s. Capacity = workers × 3600 / CE-time; feedback delay is ~0 below capacity and grows once load exceeds it.",
		commas(float64(devs)), trimF(apd), commas(daily), pf*100, commas(peak), tSource)), "", "L", false)
	pdf.Ln(1)

	// capacity-vs-peak verdict for a single EE node — the one honest decision number
	if ee != "" && eeW > 0 {
		eeCap := capf(float64(eeW))
		var verdict string
		if peak <= eeCap {
			verdict = fmt.Sprintf("A single EE node (%d workers) handles ~%s analyses/hr — above your ~%s/hr peak "+
				"(~%.0f%% utilisation). At this scale one node keeps up, so DCE's value here is high availability and "+
				"growth headroom rather than raw throughput.", eeW, commas(eeCap), commas(peak), peak/eeCap*100)
		} else {
			verdict = fmt.Sprintf("A single EE node (%d workers) handles ~%s analyses/hr, but your peak is ~%s/hr "+
				"(~%.0f%% of one node) — one node can't sustain it, so its queue grows and feedback delay climbs. DCE "+
				"adds nodes to close the gap.", eeW, commas(eeCap), commas(peak), peak/eeCap*100)
		}
		pdf.SetFont("Helvetica", "B", 10)
		pdf.MultiCell(usableW, 5, tr("Verdict: "+verdict), "", "L", false)
		pdf.SetFont("Helvetica", "", 10)
		pdf.Ln(1)
	}

	colW := []float64{62, 26, 20, 45, 25}
	drawTable(pdf, tr, colW, rows, nil, 8)
	pdf.Ln(1)
	pdf.SetFont("Helvetica", "", 8.5)
	setText(pdf, mut)
	pdf.MultiCell(usableW, 4, tr("Measured rows are this run's actual configuration; estimates project the same measured "+
		"per-analysis CE time onto other worker counts. EE is a single node — bounded by one host, no HA — while DCE "+
		"scales by adding nodes; the estimates show several node × workers/node combinations that reach the needed "+
		"capacity. Estimates are linear (capacity = workers × 3600 / CE-time); near or above 100% utilisation real "+
		"queueing grows faster than shown."), "", "L", false)
	pdf.Ln(2)

	// ---- model chart (measured lines only) ----
	xmax := peak * 2
	ymax := 30.0
	for _, n := range names {
		w := float64(results[n].Workers)
		if w == 0 {
			w = 1
		}
		if d := dmin(xmax, w) * 1.1; d > ymax {
			ymax = d
		}
	}
	if pdf.GetY() > 210 { // keep the chart off the very bottom
		pdf.AddPage()
	}
	p := &plot{pdf: pdf, x: 30, y: pdf.GetY() + 10, w: usableW - 30, h: 52, xmax: xmax, ymax: ymax}
	p.frame(tr, fmt.Sprintf("Production model at %s developers — avg feedback delay vs load", commas(float64(devs))),
		"peak analysis submission rate (analyses/hour)", "", func(v float64) string { return fmt.Sprintf("%.0fk", v/1000) })
	var labels []string
	var lcols [][3]int
	for _, n := range names {
		w := float64(results[n].Workers)
		if w == 0 {
			w = 1
		}
		pts := make([][2]float64, 0, 60)
		for i := 0; i <= 60; i++ {
			x := xmax * float64(i) / 60
			pts = append(pts, [2]float64{x, dmin(x, w)})
		}
		p.line(pts, cols[n], false)
		labels = append(labels, fmt.Sprintf("%s — %d workers", n, results[n].Workers))
		lcols = append(lcols, cols[n])
	}
	// peak marker
	setDraw(pdf, ink)
	pdf.SetLineWidth(0.3)
	pdf.Line(p.px(peak), p.y, p.px(peak), p.y+p.h)
	pdf.SetFont("Helvetica", "B", 8)
	setText(pdf, ink)
	pdf.SetXY(p.px(peak)-24, p.y+2)
	pdf.CellFormat(22, 4, tr(fmt.Sprintf("%s-dev peak", commas(float64(devs)))), "", 0, "R", false, 0, "")
	pdf.SetXY(16, p.y-6)
	pdf.SetFont("Helvetica", "", 8)
	setText(pdf, mut)
	pdf.CellFormat(30, 4, tr("avg feedback delay (min)"), "", 0, "L", false, 0, "")
	p.legend(tr, labels, lcols)
	pdf.SetY(p.y + p.h + 12)

	_ = gofpdf.PointType{}
}

// trimF prints a float without a trailing .0 for whole numbers (e.g. 15 not 15.0).
func trimF(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%g", v)
}
