package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const defaultExcl = "**/node_modules/**,**/dist/**,**/*.min.js,**/*.pyc,**/__pycache__/**"

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func glob(dir, pat string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, pat))
	return len(m) > 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// detectScanMode picks the scanner from the project's build files.
func detectScanMode(seed string) string {
	if exists(filepath.Join(seed, "pom.xml")) {
		return "maven"
	}
	if exists(filepath.Join(seed, "build.gradle")) || exists(filepath.Join(seed, "build.gradle.kts")) || glob(seed, "settings.gradle*") {
		return "gradle"
	}
	if glob(seed, "*.sln") || glob(seed, "*.csproj") || hasCsprojDeep(seed) {
		return "dotnet"
	}
	return "cli"
}

func hasCsprojDeep(seed string) bool {
	found := false
	filepath.WalkDir(seed, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".csproj") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func props(cfg *Config) []string {
	var out []string
	for k, v := range cfg.ScannerProps {
		out = append(out, fmt.Sprintf("-D%s=%s", k, v))
	}
	return out
}

// buildArgs splits cfg.BuildArgs like a shell (handles simple quoting).
func buildArgs(cfg *Config) []string { return shellSplit(cfg.BuildArgs) }

func shellSplit(s string) []string {
	var out []string
	var cur strings.Builder
	inArg := false
	quote := rune(0)
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			inArg = true
		case r == '\'' || r == '"':
			quote = r
			inArg = true
		case r == ' ' || r == '\t':
			if inArg {
				out = append(out, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if inArg {
		out = append(out, cur.String())
	}
	return out
}

func findReport(roots ...string) string {
	var best string
	var bestT time.Time
	consider := func(p string) {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(bestT) {
			bestT = fi.ModTime()
			best = filepath.Dir(p)
		}
	}
	for _, d := range roots {
		if d == "" {
			continue
		}
		consider(filepath.Join(d, "scanner-report", "metadata.pb"))
		filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !de.IsDir() && de.Name() == "metadata.pb" && filepath.Base(filepath.Dir(p)) == "scanner-report" {
				consider(p)
			}
			return nil
		})
	}
	return best
}

// produceSeedReport scans the seed ONCE with the right scanner; returns its report dir.
func produceSeedReport(t Target, cfg *Config, workdir string) string {
	seed := cfg.SeedRepo
	key := cfg.Namespace + "-seed"
	mode := cfg.ScanMode
	if mode == "" || mode == "auto" {
		mode = detectScanMode(seed)
	}
	if mode == "dotnet" {
		die("[%s] .NET (C#/VB.NET) is NOT supported by this tool. Point seed_repo at a "+
			"Java / JS / TS / Python / Go / … project (see README > Compatible languages).", t.Name)
	}
	fmt.Printf("  scan mode: %s\n", mode)

	common := []string{
		"-Dsonar.host.url=" + strings.TrimRight(t.URL, "/"),
		"-Dsonar.token=" + t.Token,
		"-Dsonar.projectKey=" + key, "-Dsonar.projectName=" + key,
		"-Dsonar.scanner.keepReport=true", "-Dsonar.scm.disabled=true",
	}
	common = append(common, props(cfg)...)

	var name, cwd string
	var args, roots []string
	switch mode {
	case "maven":
		name = firstNonEmpty(cfg.Maven, "mvn")
		args = append([]string{"-B"}, buildArgs(cfg)...)
		args = append(args, "-DskipTests", "verify", "org.sonarsource.scanner.maven:sonar-maven-plugin:sonar")
		args = append(args, common...)
		cwd = seed
		roots = []string{filepath.Join(seed, "target"), seed, workdir}
	case "gradle":
		gw := cfg.Gradle
		if gw == "" {
			if exists(filepath.Join(seed, "gradlew")) {
				gw = "./gradlew"
			} else {
				gw = "gradle"
			}
		}
		name = gw
		args = append(buildArgs(cfg), "build", "sonar", "-x", "test")
		args = append(args, common...)
		cwd = seed
		roots = []string{filepath.Join(seed, "build"), seed, workdir}
	default: // cli
		name = firstNonEmpty(cfg.ScannerCLI, cfg.Scanner, "sonar-scanner")
		args = []string{
			"-Dsonar.projectBaseDir=" + seed, "-Dsonar.sources=.",
			"-Dsonar.working.directory=" + workdir, "-Dsonar.scanner.skipJreProvisioning=true",
			"-Dsonar.exclusions=" + firstNonEmpty(cfg.Exclusions, defaultExcl),
		}
		args = append(args, common...)
		roots = []string{workdir}
	}

	tail := runScan(t, name, args, cwd, cfg, mode)
	rep := findReport(roots...)
	if rep == "" {
		fmt.Fprintln(os.Stderr, tail)
		die("[%s] %s scan produced no report — see build output above (the project must build in this "+
			"environment: JDK + Maven/Gradle + resolvable deps).", t.Name, mode)
	}
	post(t, "/api/projects/bulk_delete", url.Values{"q": {key}}) // keep only the report, not the project
	return rep
}

// runScan streams the build output live and enforces a hard timeout by killing the
// whole process group (mvn + child JVMs/npm), so a stalled build never hangs forever.
func runScan(t Target, name string, args []string, cwd string, cfg *Config, mode string) string {
	timeout := cfg.ScanTimeout
	if timeout <= 0 {
		timeout = 1800
	}
	fmt.Printf("  building & scanning the seed (mode=%s) — this runs your project's build and can take "+
		"several minutes; live output follows (aborts after %ds if it stalls):\n", mode, timeout)

	cmd := exec.Command(name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	env := os.Environ()
	if cfg.JavaHome != "" {
		env = append(env, "JAVA_HOME="+cfg.JavaHome)
		env = prependPath(env, filepath.Join(cfg.JavaHome, "bin"))
		fmt.Printf("  JAVA_HOME: %s\n", cfg.JavaHome)
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own process group

	pr, pw, err := os.Pipe()
	if err != nil {
		die("[%s] internal pipe error: %v", t.Name, err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		die("[%s] cannot run %q — not found or not executable (%v). Install it, or set its path in "+
			"bench.yaml (maven / gradle / scanner_cli).", t.Name, name, err)
	}
	pw.Close()

	var timedOut atomic.Bool
	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(time.Duration(timeout) * time.Second):
			timedOut.Store(true)
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // kill the whole group
		}
	}()

	var tail []string
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 200 {
			line = line[:200]
		}
		fmt.Println("    " + line)
		tail = append(tail, line)
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
	}
	cmd.Wait()
	close(done)
	if timedOut.Load() {
		die("[%s] seed scan exceeded %ds and was aborted — it looked stuck. Check the build and network; "+
			"raise 'scan_timeout' in bench.yaml if the build is legitimately long.", t.Name, timeout)
	}
	return strings.Join(tail, "\n")
}

func prependPath(env []string, dir string) []string {
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + dir + string(os.PathListSeparator) + e[len("PATH="):]
			return env
		}
	}
	return append(env, "PATH="+dir)
}
