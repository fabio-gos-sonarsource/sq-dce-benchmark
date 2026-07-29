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
		if peak <= c {
			return "no queue" // under capacity: analyses are picked up as fast as they arrive
		}
		d := dmin(peak, float64(w))
		mx := (peak - c) / c * 60
		if d >= 1 {
			return fmt.Sprintf("~%.0f min (max ~%.0f min)", d, mx)
		}
		return "< 1 min"
	}
	topo := func(name string, nodes, wpn, w int) string {
		if nodes <= 1 {
			return fmt.Sprintf("%s — 1 node, %d workers", name, w)
		}
		return fmt.Sprintf("%s — %d nodes × %d (%d workers)", name, nodes, wpn, w)
	}

	// single EE node (fewest app-nodes) — used by the capacity-vs-peak verdict below
	ee := ""
	for _, n := range names {
		if results[n].AppNodes <= 1 && ee == "" {
			ee = n
		}
	}
	eeW := 0
	if ee != "" {
		eeW = results[ee].Workers
	}

	// table rows: the measured EE and DCE configurations only (no projected estimates)
	rows := [][]string{{"Configuration", "Peak capacity", "Utilisation", "Avg feedback delay"}}
	add := func(label string, w int) {
		rows = append(rows, []string{label, commas(capf(float64(w))) + "/hr", utilCell(w), delayCell(w)})
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
		add(topo(n, nodes, wpn, w), w)
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
		"Assumptions: %s developers × %s analyses/day = %s/day; ~%.0f%% in the peak hour ~ %s analyses/hr.",
		commas(float64(devs)), trimF(apd), commas(daily), pf*100, commas(peak))), "", "L", false)
	pdf.Ln(1)

	// one bold verdict line: EE-vs-peak decision + the queue/no-queue consequence,
	// merged so the same point isn't made twice.
	if ee != "" && eeW > 0 {
		eeCap := capf(float64(eeW))
		// the DCE cluster row (multi-node, else the most-workers target) for the contrast
		dce := ""
		for _, n := range names {
			if results[n].AppNodes > 1 && dce == "" {
				dce = n
			}
		}
		var verdict string
		switch {
		case peak <= eeCap:
			verdict = fmt.Sprintf("At the ~%s/hr peak a single EE node (%d workers, ~%s/hr) stays under capacity — "+
				"no queue forms, feedback is immediate. DCE's value here is high availability and headroom, not raw throughput.",
				commas(peak), eeW, commas(eeCap))
		case dce != "" && peak <= capf(float64(results[dce].Workers)):
			verdict = fmt.Sprintf("At the ~%s/hr peak a single EE node (%d workers, ~%s/hr) is over capacity — its queue "+
				"grows and feedback delay climbs; the DCE cluster (%d workers, ~%s/hr) stays under capacity, so no queue forms.",
				commas(peak), eeW, commas(eeCap), results[dce].Workers, commas(capf(float64(results[dce].Workers))))
		default:
			verdict = fmt.Sprintf("At the ~%s/hr peak both EE and DCE are over capacity — queues grow on both. Size DCE "+
				"with more nodes/workers until capacity clears the peak.", commas(peak))
		}
		pdf.SetFont("Helvetica", "B", 10)
		pdf.MultiCell(usableW, 5, tr(verdict), "", "L", false)
		pdf.SetFont("Helvetica", "", 10)
		pdf.Ln(1)
	}

	colW := []float64{70, 30, 25, 53}
	drawTable(pdf, tr, colW, rows, nil, 8)
	pdf.Ln(1)
	pdf.SetFont("Helvetica", "", 8.5)
	setText(pdf, mut)
	pdf.MultiCell(usableW, 4, tr(fmt.Sprintf("Measured EE and DCE configurations; %s. Capacity = workers × 3600 / CE-time. "+
		"EE is one host (no HA); DCE scales by adding nodes. Above 100%% utilisation, real queueing grows faster than this "+
		"linear model.", tSource)), "", "L", false)
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
	p.frame(tr, fmt.Sprintf("Production model at %s developers — feedback delay vs load", commas(float64(devs))),
		"peak analysis submission rate (analyses/hour)", "avg feedback delay (minutes)",
		func(v float64) string { return fmt.Sprintf("%.0fk", v/1000) })
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

	// annotate each edition's feedback delay AT the peak, so the reader can read the
	// exact minutes EE and DCE would wait at this load (a dot on the curve + a label).
	type peakMark struct {
		y   float64
		txt string
		c   [3]int
	}
	var marks []peakMark
	for _, n := range names {
		w := float64(results[n].Workers)
		if w == 0 {
			w = 1
		}
		d := dmin(peak, w)
		p.dot(peak, d, cols[n])
		txt := "no queue"
		if peak > capf(w) {
			if d >= 1 {
				txt = fmt.Sprintf("~%.0f min", d)
			} else {
				txt = "< 1 min"
			}
		}
		marks = append(marks, peakMark{y: p.py(d), txt: txt, c: cols[n]})
	}
	// draw bottom-most first and push overlapping labels upward so both stay legible
	sort.Slice(marks, func(i, j int) bool { return marks[i].y > marks[j].y })
	pdf.SetFont("Helvetica", "B", 8)
	prevY := 1e9
	for _, m := range marks {
		ly := m.y - 3.5 // sit clear above the dot (and above the axis for the "no queue" point)
		if prevY-ly < 4 {
			ly = prevY - 4
		}
		prevY = ly
		setText(pdf, m.c)
		pdf.SetXY(p.px(peak)+3, ly)
		pdf.CellFormat(22, 4, tr(m.txt), "", 0, "L", false, 0, "")
	}

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
