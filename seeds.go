package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Pre-generated scanner reports for the bundled sample seeds, stored zipped.
// Shipping the report (not the source) means a sample benchmark needs NO scanner,
// JDK/Maven, or network build step — the tool replays the embedded report directly.
// Generated against the oldest supported SonarQube (2026.1), so they replay on 2026.1+.
//
//go:embed seeds/react.zip seeds/jackson.zip seeds/jackson-pr.zip
var seedFS embed.FS

// sampleInfo describes a bundled seed for the run banner / report label.
type sampleInfo struct {
	label string // human label, e.g. "React (JavaScript, ~98k ncloc)"
	zip   string // full-scan report within seedFS
	prZip string // optional PR-sized report; when set the seed is a mixed workload
}

var samples = map[string]sampleInfo{
	"js":   {label: "React (JavaScript, ~98k ncloc)", zip: "seeds/react.zip"},
	"java": {label: "jackson-databind (Java, ~76k ncloc)", zip: "seeds/jackson.zip"},
	// mixed: mostly small PR-sized analyses with the occasional full scan — the realistic
	// real-world traffic mix (see mixPRFraction for the ratio).
	"mixed": {label: "jackson-databind mixed (Java, ~3.5k PR + ~76k full)", zip: "seeds/jackson.zip", prZip: "seeds/jackson-pr.zip"},
}

// defaultSample is used when neither seed_repo nor sample is set. The realistic
// PR + full mix is the default so a bare run reflects real-world traffic.
const defaultSample = "mixed"

// mixPRFraction is the share of a mixed-seed burst that replays the small PR-sized
// report (the rest replay the full scan). Reuses model.pr_fraction when set so the
// measured mix and the production model agree; defaults to a realistic 0.8.
func mixPRFraction(cfg *Config) float64 {
	if cfg.Model != nil && cfg.Model.PRFraction > 0 && cfg.Model.PRFraction <= 1 {
		return cfg.Model.PRFraction
	}
	return 0.8
}

// replaySource holds the report directory (or directories) to replay during the burst.
type replaySource struct {
	full   string  // full/branch-analysis report dir (always set; contains metadata.pb)
	pr     string  // PR-sized report dir ("" unless a mixed seed)
	prFrac float64 // share of submissions that replay the PR report (0 unless mixed)
}

// pick chooses the report dir for submission i. For a mixed seed it interleaves ~prFrac
// PR analyses with occasional full ones, evenly spread and deterministic (reproducible).
func (r *replaySource) pick(i int) string {
	if r.pr == "" || r.prFrac <= 0 {
		return r.full
	}
	if r.prFrac >= 1 {
		return r.pr
	}
	everyN := int(math.Round(1 / (1 - r.prFrac))) // e.g. prFrac 0.8 -> every 5th is a full scan
	if everyN < 2 {
		everyN = 2
	}
	if i%everyN == 0 {
		return r.full
	}
	return r.pr
}

// unzipSeedZip extracts an embedded seed zip into dst (the report dir, with metadata.pb
// at its root), guarding against zip-slip.
func unzipSeedZip(zipRel, dst string) {
	data, err := seedFS.ReadFile(zipRel)
	if err != nil {
		die("bundled seed %q is missing (%v) — rebuild the binary", zipRel, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		die("cannot stage seed: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		die("bundled seed %q is corrupt (%v)", zipRel, err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		clean := filepath.Clean(f.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			continue // guard against zip-slip
		}
		out := filepath.Join(dst, clean)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			die("cannot stage seed: %v", err)
		}
		rc, err := f.Open()
		if err != nil {
			die("cannot read seed entry %s: %v", f.Name, err)
		}
		w, err := os.Create(out)
		if err != nil {
			rc.Close()
			die("cannot write seed file %s: %v", out, err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			w.Close()
			rc.Close()
			die("cannot extract seed file %s: %v", out, err)
		}
		w.Close()
		rc.Close()
	}
}

// seedLabel is the seed name shown in the report subtitle.
func (c *Config) seedLabel() string {
	if c.SeedRepo != "" {
		return filepath.Base(strings.TrimRight(c.SeedRepo, "/"))
	}
	name := c.Sample
	if name == "" {
		name = defaultSample
	}
	info, ok := samples[name]
	if !ok {
		return name
	}
	if info.prZip != "" { // mixed seed: spell out the PR/full split
		prf := mixPRFraction(c)
		return fmt.Sprintf("%s (%.0f%% PR + %.0f%% full)", info.label, prf*100, (1-prf)*100)
	}
	return info.label
}

// addPRSlice tries to enrich rs with an auto-picked PR-sized slice of the customer's repo
// so the burst replays a realistic PR + full mix. Any failure leaves rs full-only.
func addPRSlice(t Target, cfg *Config, workdir string, rs *replaySource) {
	slice, n, ok := pickPRSlice(cfg.SeedRepo)
	if !ok {
		fmt.Println("  PR mix: no suitable slice found in the repo — running full-scan only")
		return
	}
	pr, err := producePRSlice(t, cfg, workdir, slice, findJavaBinaries(cfg.SeedRepo))
	if err != nil {
		fmt.Printf("  PR mix: slice scan failed (%v) — running full-scan only\n", err)
		return
	}
	rs.pr, rs.prFrac = pr, mixPRFraction(cfg)
	fmt.Printf("  PR mix: %.0f%% slice (%s, %d files) + %.0f%% full\n", rs.prFrac*100, slice, n, (1-rs.prFrac)*100)
}

// seedReport returns the report(s) to replay: the customer's own scanned repo when
// seed_repo is set, otherwise a bundled pre-scanned sample (no scanner needed). A mixed
// sample returns both a full and a PR-sized report for the burst to interleave.
func seedReport(t Target, cfg *Config, workdir string) *replaySource {
	if cfg.SeedRepo != "" {
		fmt.Println("  scanning seed once ...")
		rs := &replaySource{full: produceSeedReport(t, cfg, workdir)}
		if cfg.PRMix {
			addPRSlice(t, cfg, workdir, rs)
		}
		return rs
	}
	name := cfg.Sample
	if name == "" {
		name = defaultSample
	}
	info, ok := samples[name]
	if !ok {
		die("unknown sample %q — use one of: js, java, mixed (or set seed_repo to your own repo)", name)
	}
	full := filepath.Join(workdir, "full")
	unzipSeedZip(info.zip, full)
	rs := &replaySource{full: full}
	if info.prZip != "" {
		pr := filepath.Join(workdir, "pr")
		unzipSeedZip(info.prZip, pr)
		rs.pr, rs.prFrac = pr, mixPRFraction(cfg)
	}
	fmt.Printf("  using bundled pre-scanned sample: %s — no scanner needed\n", cfg.seedLabel())
	fmt.Println("     (set seed_repo in bench.yaml to benchmark your own repository instead)")
	return rs
}
