// Command sq-dce-benchmark compares Compute Engine (CE) throughput between a
// SonarQube Enterprise Edition node and a Data Center Edition cluster and writes
// a PDF report. It is a Go port of the original Python tool (same "replay" method:
// scan a seed project ONCE per target, then replay that one report N times as
// distinct projects to create an instant CE burst).
//
// NON-PRODUCTION BENCHMARK: it POSTs SonarScanner's internal report format to
// api/ce/submit (unsupported internals). Run only against non-prod instances.
package main

import (
	"fmt"
	"os"

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
}

// Config mirrors bench.yaml.
type Config struct {
	SeedRepo     string            `yaml:"seed_repo"`
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
	return "sq-dce-benchmark-report.pdf"
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
	if len(c.Targets) == 0 || c.SeedRepo == "" || c.N == 0 || c.Namespace == "" {
		die("config missing one of: targets, seed_repo, n, namespace")
	}
	return &c
}

func usage() {
	die("usage: sq-dce-benchmark <run|cleanup|report> --config bench.yaml [--only NAME]")
}

func main() {
	args := os.Args[1:]
	if len(args) < 1 {
		usage()
	}
	command := args[0]
	if command == "version" || command == "--version" || command == "-v" {
		fmt.Println("sq-dce-benchmark", version)
		return
	}
	var configPath, only string
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--config":
			i++
			if i < len(args) {
				configPath = args[i]
			}
		case "--only":
			i++
			if i < len(args) {
				only = args[i]
			}
		default:
			usage()
		}
	}
	if configPath == "" {
		usage()
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
	}
}
