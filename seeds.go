package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Pre-generated scanner reports for the bundled sample seeds, stored zipped.
// Shipping the report (not the source) means a sample benchmark needs NO scanner,
// JDK/Maven, or network build step — the tool replays the embedded report directly.
// Generated against the oldest supported SonarQube (2026.1), so they replay on 2026.1+.
//
//go:embed seeds/react.zip seeds/jackson.zip
var seedFS embed.FS

// sampleInfo describes a bundled seed for the run banner / report label.
type sampleInfo struct {
	label string // human label, e.g. "Rich (Python, ~32k ncloc)"
	zip   string // path within seedFS
}

var samples = map[string]sampleInfo{
	"js":   {label: "React (JavaScript, ~98k ncloc)", zip: "seeds/react.zip"},
	"java": {label: "jackson-databind (Java, ~76k ncloc)", zip: "seeds/jackson.zip"},
}

// defaultSample is used when neither seed_repo nor sample is set.
const defaultSample = "js"

// materializeSample unzips a bundled seed's scanner-report into workdir and returns
// the report directory (the one containing metadata.pb), ready for staging/replay.
func materializeSample(name, workdir string) (string, sampleInfo) {
	info, ok := samples[name]
	if !ok {
		die("unknown sample %q — use one of: js, java (or set seed_repo to your own repo)", name)
	}
	data, err := seedFS.ReadFile(info.zip)
	if err != nil {
		die("bundled sample %q is missing its report (%v) — rebuild the binary", name, err)
	}
	dst := filepath.Join(workdir, "scanner-report")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		die("cannot stage sample: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		die("bundled sample %q is corrupt (%v)", name, err)
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
			die("cannot stage sample: %v", err)
		}
		rc, err := f.Open()
		if err != nil {
			die("cannot read bundled sample entry %s: %v", f.Name, err)
		}
		w, err := os.Create(out)
		if err != nil {
			rc.Close()
			die("cannot write sample file %s: %v", out, err)
		}
		if _, err := io.Copy(w, rc); err != nil {
			w.Close()
			rc.Close()
			die("cannot extract sample file %s: %v", out, err)
		}
		w.Close()
		rc.Close()
	}
	return dst, info
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
	if info, ok := samples[name]; ok {
		return info.label
	}
	return name
}

// seedReport returns the report directory to replay: the customer's own scanned repo
// when seed_repo is set, otherwise a bundled pre-scanned sample (no scanner needed).
func seedReport(t Target, cfg *Config, workdir string) string {
	if cfg.SeedRepo != "" {
		fmt.Println("  scanning seed once ...")
		return produceSeedReport(t, cfg, workdir)
	}
	name := cfg.Sample
	if name == "" {
		name = defaultSample
	}
	dir, info := materializeSample(name, workdir)
	fmt.Printf("  using bundled pre-scanned sample: %s — no scanner needed\n", info.label)
	fmt.Println("     (set seed_repo in bench.yaml to benchmark your own repository instead)")
	return dir
}
