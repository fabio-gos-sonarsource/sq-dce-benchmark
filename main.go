// Command sq-benchmark compares Compute Engine (CE) throughput between a SonarQube
// Enterprise Edition node and a Data Center Edition cluster and writes a PDF report.
// It uses the "replay" method: scan a seed project ONCE per target, then replay that
// one report N times as distinct projects to create an instant CE burst.
//
// NON-PRODUCTION BENCHMARK: it POSTs SonarScanner's internal report format to
// api/ce/submit (unsupported internals). Run only against non-prod instances.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// Target is one instance under test.
type Target struct {
	Name    string `yaml:"name"`
	URL     string `yaml:"url"`
	Token   string `yaml:"token"`
	Workers int    `yaml:"workers"` // optional label override; real count is auto-detected
}

// Model holds the optional production-model parameters.
type Model struct {
	Devs              int     `yaml:"devs"`
	AnalysesPerDevDay float64 `yaml:"analyses_per_dev_day"`
	PeakFraction      float64 `yaml:"peak_fraction"`
	CeSeconds         float64 `yaml:"ce_seconds"`
	// Optional PR/branch mix: real traffic is mostly cheap PR analyses (changeset-sized)
	// plus some full branch analyses. If both CE times are given, the model uses a blended
	// per-analysis CE time instead of the single measured value.
	PRFraction      float64 `yaml:"pr_fraction"`       // 0..1 share of analyses that are PRs
	PRCeSeconds     float64 `yaml:"pr_ce_seconds"`     // CE time for a typical PR analysis
	BranchCeSeconds float64 `yaml:"branch_ce_seconds"` // CE time for a full/branch analysis
}

// LoadSpec drives a sustained arrival rate over time instead of a one-shot burst.
type LoadSpec struct {
	RatePerMin  int `yaml:"rate_per_min"` // analyses submitted per minute
	DurationSec int `yaml:"duration_sec"` // for how long
}

// Config mirrors bench.yaml. Only `targets` is required; everything else defaults.
type Config struct {
	SeedRepo     string            `yaml:"seed_repo"` // your own repo to scan; empty -> bundled sample
	Sample       string            `yaml:"sample"`    // bundled sample when seed_repo is empty: python | java
	N            int               `yaml:"n"`
	Concurrency  int               `yaml:"concurrency"`
	ScanMode     string            `yaml:"scan_mode"`
	ScannerCLI   string            `yaml:"scanner_cli"`
	Scanner      string            `yaml:"scanner"` // legacy alias for scanner_cli
	Maven        string            `yaml:"maven"`
	Gradle       string            `yaml:"gradle"`
	JavaHome     string            `yaml:"java_home"`
	BuildArgs    string            `yaml:"build_args"`
	ScannerProps map[string]string `yaml:"scanner_props"`
	Namespace    string            `yaml:"namespace"`
	Report       string            `yaml:"report"`
	ResultsFile  string            `yaml:"results_file"`
	Exclusions   string            `yaml:"exclusions"`
	KeepProjects bool              `yaml:"keep_projects"`
	ScanTimeout  int               `yaml:"scan_timeout"`
	Load         *LoadSpec         `yaml:"load"`
	Model        *Model            `yaml:"model"`
	Targets      []Target          `yaml:"targets"`
}

func (c *Config) concurrency() int {
	if c.Concurrency > 0 {
		return c.Concurrency
	}
	return 12
}

func (c *Config) resultsFile() string {
	if c.ResultsFile != "" {
		return c.ResultsFile
	}
	return "results.json"
}

func (c *Config) reportPath() string {
	if c.Report != "" {
		return c.Report
	}
	return "sq-ee-dce-benchmark.pdf"
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

func loadConfig(path string) *Config {
	b, err := os.ReadFile(path)
	if err != nil {
		die("cannot read config %s: %v", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		die("invalid YAML in %s: %v", path, err)
	}
	if len(c.Targets) == 0 {
		die("config %s has no targets — add at least one instance with a url and token", path)
	}
	for _, t := range c.Targets {
		if t.URL == "" || t.Token == "" {
			die("target %q needs both a url and a token", t.Name)
		}
	}
	// defaults so the user only has to fill in targets
	if c.N == 0 {
		c.N = 40
	}
	if c.Namespace == "" {
		c.Namespace = "sq_ee_dce_benchmark"
	}
	if c.Sample == "" {
		c.Sample = "python"
	}
	return &c
}

// discoverConfig finds bench.yaml when --config isn't given: current folder first,
// then next to the executable (so a double-clicked binary finds the adjacent config).
func discoverConfig() string {
	if exists("bench.yaml") {
		return "bench.yaml"
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "bench.yaml")
		if exists(p) {
			return p
		}
	}
	die("no bench.yaml found (in this folder or next to the binary).\n" +
		"  Copy bench.example.yaml to bench.yaml and fill in your two instances, or pass --config <file>.")
	return ""
}

func usage() {
	fmt.Println("sq-benchmark — SonarQube EE vs DCE Compute Engine throughput benchmark")
	fmt.Println()
	fmt.Println("Usage: sq-benchmark [run|cleanup|report] [--config bench.yaml] [--only NAME]")
	fmt.Println("  With no arguments it runs the benchmark using the nearest bench.yaml.")
}

func main() {
	args := os.Args[1:]
	command := "run" // default so a bare invocation / double-click just runs
	i := 0
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		i = 1
	}
	var configPath, only string
	for ; i < len(args); i++ {
		switch args[i] {
		case "--config", "-c":
			i++
			if i < len(args) {
				configPath = args[i]
			}
		case "--only":
			i++
			if i < len(args) {
				only = args[i]
			}
		case "--version", "-v":
			command = "version"
		case "--help", "-h":
			command = "help"
		}
	}

	switch command {
	case "version":
		fmt.Println("sq-benchmark", version)
		return
	case "help":
		usage()
		return
	}

	if configPath == "" {
		configPath = discoverConfig()
	}
	cfg := loadConfig(configPath)

	switch command {
	case "run":
		cmdRun(cfg, only)
	case "cleanup":
		cmdCleanup(cfg)
	case "report":
		cmdReport(cfg)
	default:
		usage()
		os.Exit(1)
	}
}
