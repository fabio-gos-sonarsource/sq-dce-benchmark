# SonarQube EE vs DCE — Compute Engine throughput benchmark

A one-command benchmark that compares **Compute Engine (CE) throughput** between a
SonarQube **Enterprise Edition** node and a **Data Center Edition** cluster, and
produces a PDF report, designed to show the impact of DCE on analysis feedback
(queue behaviour) at scale.

> ⚠️ **Non-production benchmark.** It generates load by replaying SonarScanner's
> internal report format via `api/ce/submit` (unsupported internals). Run it only
> against **non-production** instances.

## What you need

- The **`sq-benchmark` binary**.
- A **SonarQube Enterprise Edition** and a **Data Center Edition** instance (2026.1+),
  reachable from this machine.
- An **admin token** for each instance: it needs create-project, execute-analysis and
  **Administer System**.
- **Only if you benchmark your own repo** (instead of a bundled sample): a scanner for
  its language — the **SonarScanner CLI** for source-analysed languages, or
  **Maven/Gradle + a JDK** for Java. The bundled samples are pre-scanned, so out of the
  box you need none of this.

## Compatible languages

Java, JavaScript/TypeScript, Python, Go, PHP, Kotlin, Ruby, Scala, HTML/CSS, XML, YAML,
IaC, and other source-analysed languages.

> **Not supported: .NET (C#/VB.NET)** — it requires the SonarScanner for .NET
> (`begin` → build → `end`), which this tool does not drive. Point `seed_repo` at a
> supported project instead.

This applies when you benchmark **your own** repo. When you do, it's scanned with the
right scanner automatically (`scan_mode: auto`) — the bundled samples come pre-scanned:

| Project contains | Scanner used | You need |
|---|---|---|
| `pom.xml` | Maven — `mvn verify sonar:sonar` | **Maven + a JDK**, dependencies resolvable |
| `build.gradle(.kts)` | Gradle — `gradlew build sonar` | **Gradle + a JDK**, and the **SonarQube Gradle plugin** applied in the build |
| neither | CLI — `sonar-scanner` | the **SonarScanner CLI** |

**Java note:** Java analysis needs compiled bytecode, so Maven/Gradle **build then scan**
in one step — you don't set `sonar.java.binaries` manually. The project must **build on the
machine running the tool** (JDK + Maven/Gradle + resolvable dependencies/credentials).
Force a mode with `scan_mode: maven | gradle | cli` if you don't want auto-detection.

If your project targets a specific JDK (common with Lombok or older codebases), set
`java_home:` in `bench.yaml` — the tool builds/scans the seed with that JDK
(`JAVA_HOME` + its `bin` on `PATH`) instead of the default `java` on `PATH`.

If your project needs a build **profile** or module selection to build (e.g. a
corporate Artifactory mirror profile, or building only some modules), pass it via
`build_args:` — the string is inserted into the `mvn`/`gradle` command, e.g.
`build_args: "-P artifactory"` or `build_args: "-pl backend -am"`.

The seed scan streams the build output live and aborts if it stalls (default 30 min;
tune with `scan_timeout:` in `bench.yaml`), so a long build never looks like a hang.

## Setup

```bash
cp bench.example.yaml bench.yaml     # then edit: hosts, tokens, n
```

**By default the benchmark replays a bundled, pre-scanned sample** — nothing to install,
build, or clone. The default is `mixed`:

```yaml
sample: mixed       # DEFAULT — realistic mix (see below).  Or: js | java
```

`mixed` replays a realistic workload: mostly small PR-sized analyses (~3.5k ncloc) with
the occasional full scan (~76k ncloc), defaulting to 80% PRs. This mirrors real traffic
(cheap PR analyses dominate; full branch scans are rarer), so the measured throughput —
and the production model's blended CE time — reflect how the instances behave in
production rather than "every analysis is a full scan". Change the ratio with
`model.pr_fraction`.

For a single-project **full-scan** seed instead, use `sample: js` (React, ~98k ncloc) or
`sample: java` (jackson-databind, ~76k ncloc).

To benchmark **your own** code instead, set `seed_repo` (this is the only mode that runs
a scanner — see *Compatible languages* for the toolchain it needs):

```yaml
seed_repo: /path/to/your/repo
# pr_mix: true            # replay a realistic PR + full mix of your repo (see below)
```

Add `pr_mix: true` to get the same realistic mix as the `mixed` sample, but on **your**
code: after the full scan, the tool auto-picks a module-sized slice of the repo, scans it
on its own as a "PR-sized" changeset, and replays 80% slice + 20% full. It needs only the
folder you pass (no git history), and if a slice can't be produced it falls back to a
full-scan-only run.

## Run

With `bench.yaml` next to the binary, just **double-click `run.command` (macOS) /
`run.bat` (Windows)** — or from a terminal:

```bash
./sq-benchmark          # no arguments: runs using the nearest bench.yaml
```

(`--config <file>` is optional; it defaults to a `bench.yaml` in the current folder or
next to the binary.)

Per target it will: get the seed report (a bundled sample, or scan your `seed_repo`) →
validate one replay (fail fast if versions differ) → pre-create the projects → replay the
workload while sampling the queue every second → collect `api/ce/activity` metrics →
delete the bench projects. Then it writes the comparison **PDF** (`report:` path).

By default the workload is a **sustained load at your modelled peak** (see below), not a
one-shot burst.

Outputs (the PDF and `results_file`) are written **next to the binary** by default: a
relative path resolves to the executable's folder, not the launch directory — so a
double-clicked run's report lands beside `sq-benchmark`, not in your home folder. Give an
absolute path to put it elsewhere.

### Run targets independently (recommended when EE and DCE share hardware)

If both instances aren't on separate hardware, run each **on its own** so they don't
compete for CPU — results accumulate into `results_file` and the report combines them:

```bash
./sq-benchmark run    --only EE     # (DCE idle/stopped)
./sq-benchmark run    --only DCE    # (EE idle/stopped)
./sq-benchmark report               # combined PDF from results.json
```

Clean up anytime (e.g. after an interrupted run):

```bash
./sq-benchmark cleanup
```

### Load: sustained at your modelled peak (default)

By default the tool runs a **sustained load** for 60s at the peak rate implied by `devs`:

```
rate = devs × analyses_per_dev_day × peak_fraction ÷ 60      (5,000 devs → ~312/min)
```

Set `devs` (or leave the default) and run — nothing else to configure. To override the
rate or duration, add a `load:` block; for a one-shot burst instead, set `burst:`:

```yaml
# override the sustained rate/duration
load:
  rate_per_min: 300
  duration_sec: 60

# …or a one-shot burst instead
burst: 40
```

Run the same load against EE and DCE, and give the DCE cluster its own hardware for a
fair result.

## Example run & output

A filled-in `bench.yaml` (tokens redacted):

```yaml
seed_repo: /repos/acme-web          # a representative ~78K-ncloc service
pr_mix: true                        # replay a realistic PR + full mix of it
devs: 5000                          # sizes the model + the default sustained rate
scan_mode: auto
namespace: ee_vs_dce_benchmark_test
report: ./acme-ee-vs-dce.pdf
results_file: ./results.json
targets:
  - name: EE-6
    url: https://sonarqube-ee.acme.internal
    token: squ_xxxxxxxxxxxxxxxxxxxxxxxx
  - name: DCE-12
    url: https://sonarqube-dce.acme.internal
    token: squ_yyyyyyyyyyyyyyyyyyyyyyyy
```

## For a fair, meaningful result

- **Same version** on both instances (the validator aborts if a replay fails).
- **Comparable hardware/DB** for the EE node and each DCE node; **equal per-worker CE
  heap** (~1–2 GB).
- **DCE app nodes on separate hosts** — DCE's throughput advantage comes from adding
  machines; all-nodes-on-one-VM cannot out-throughput EE.
- Decide the worker comparison up front, e.g. **EE 6 workers vs DCE 12** (4/node × 3), and
  set it in each instance's UI beforehand. The tool **auto-detects** the real worker count
  (`api/ce/worker_count` × application nodes) and prints/reports it — the config `workers`
  field is optional and only overrides the report label.
- Run **off-peak**; it creates real CE/DB/ES load and `bench-*` projects (auto-deleted).
- Use a **dedicated service token**; disable webhooks on the bench namespace if the
  instance is wired to CI/Slack/Jira.

## Output

A compact PDF with a side-by-side metrics table (drain time, throughput, avg/p95
queue wait, CE time per task) and a **queue-size-over-time** chart.

### Production model

The report always includes a **production model**: it takes the *measured* CE time per
analysis and each target's *auto-detected* workers/nodes, and projects the **average
feedback delay vs. load** for a developer population you choose. It renders with sensible
defaults; to size it for your org the usual knob is a single top-level line:

```yaml
devs: 5000                  # developer population — the one knob most people set
# analyses_per_dev_day: 15  # optional (default 15)
# peak_fraction: 0.25       # optional — share landing in the peak hour (default 0.25)
```

It renders the assumptions, a plain-language **verdict** (*"a single EE node handles
~X/hr — above/below your ~Y/hr peak"*), a capacity/utilisation/feedback-delay table for
the **measured** EE and DCE configurations, and a chart showing where each configuration
saturates as load rises. Change `devs` and re-run `report` to re-model instantly.

**PR/branch mix.** Real traffic is mostly cheap PR analyses (sized to the changeset)
plus some full branch analyses. The default `mixed` seed already replays that blend, so
its *measured* CE time is realistic out of the box. If you instead run a full-scan seed
(`js`/`java`) — whose single measured CE time *over*-estimates the average cost — or want
to model different figures, the advanced `model:` block blends a PR and a branch CE time
explicitly:

```yaml
devs: 5000                # sizing stays top-level
model:                    # model: holds only the advanced CE-time blend
  pr_fraction: 0.8        # 80% of analyses are PRs (also sets the mixed-seed split)
  pr_ce_seconds: 0.5      # … at ~0.5s CE each
  branch_ce_seconds: 8    # full branch analyses at ~8s
```

This makes the capacity verdict honest: if the customer's realistic peak fits one EE
node, the report says so (and points at HA/growth as the DCE value) rather than
manufacturing a throughput gap.

## Files

| File | Purpose |
|---|---|
| `sq-benchmark` | the CLI binary (`run` / `cleanup` / `report` / `version`) |
| `run.command` / `run.bat` | double-click launchers (macOS / Windows) — shipped in the release zip; the sources live in `packaging/` |
| `bench.example.yaml` | config template (copy to `bench.yaml`) |
| `build-release.sh` | cross-compile the binaries for all platforms into `dist/<version>/` |
| `*.go`, `go.mod`, `go.sum` | Go sources — only needed to build from source |
| `seeds/` | bundled pre-scanned sample reports (`js` = React, `java` = jackson-databind, `mixed` = PR + full replay) + their licences |

## Notes / attribution

The scanner-report format this tool replays is SonarSource's open-source (LGPL)
scanner-report schema. The binary re-keys only a few known fields at the protobuf **wire
level** (via `google.golang.org/protobuf`), preserving all other fields — so no schema
files are needed at runtime and it stays compatible across SonarQube versions.
