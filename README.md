# SonarQube EE vs DCE — Compute Engine throughput benchmark

A one-command benchmark that compares **Compute Engine (CE) throughput** between a
SonarQube **Enterprise Edition** node and a **Data Center Edition** cluster, and
produces a PDF report — designed to show the impact of DCE on analysis feedback
(queue behaviour) at scale.

It ships as a **single static binary** with no dependencies to install, so it runs
on locked-down machines that only allow a downloaded executable.

> ⚠️ **Non-production benchmark.** It generates load by replaying SonarScanner's
> internal report format via `api/ce/submit` (unsupported internals). Run it only
> against **non-production** instances.

## Get the binary

Download the archive for your OS/architecture from the release, unzip it, and run the
`sq-dce-benchmark` binary inside — there is nothing else to install.

| Platform | Archive |
|---|---|
| macOS (Apple Silicon / Intel) | `sq-dce-benchmark_<ver>_darwin_arm64.zip` · `…_darwin_amd64.zip` |
| Linux (arm64 / x86-64) | `…_linux_arm64.zip` · `…_linux_amd64.zip` |
| Windows (x86-64 / arm64) | `…_windows_amd64.zip` · `…_windows_arm64.zip` |

Or build from source (Go 1.24+):

```bash
go build -o sq-dce-benchmark .
```

Maintainers cross-compile all platforms at once:

```bash
./build-release.sh v1.0.0        # -> dist/v1.0.0/*.zip
```

Verify it runs:

```bash
./sq-dce-benchmark version
```

## What you need

- The **`sq-dce-benchmark` binary** (above) — nothing else to install.
- A scanner for your seed project's language (see *Compatible languages*): the
  **SonarScanner CLI** for source-analysed languages, or **Maven/Gradle + a JDK** for Java.
- A **SonarQube Enterprise Edition** and a **Data Center Edition** instance (2026.1+),
  reachable from this machine.
- An **admin token** for each instance — it needs create-project, execute-analysis and
  **Administer System**; the tool checks this up front and fails fast with a clear message.

## Compatible languages

Java, JavaScript/TypeScript, Python, Go, PHP, Kotlin, Ruby, Scala, HTML/CSS, XML, YAML,
IaC, and other source-analysed languages.

> **Not supported: .NET (C#/VB.NET)** — it requires the SonarScanner for .NET
> (`begin` → build → `end`), which this tool does not drive. Point `seed_repo` at a
> supported project instead.

The seed is scanned with the right scanner automatically (`scan_mode: auto`):

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
cp bench.example.yaml bench.yaml     # then edit: hosts, tokens, seed_repo, n
```

Point `seed_repo` at a **representative repository** (the bundled `sample-project`
is tiny — good only for a smoke test; a bigger repo gives realistic CE task sizes).
For a quick Java check, point `seed_repo` at `sample-java` (a self-contained Maven
project that builds offline) — it exercises the full `scan_mode: maven` path.

## Run

```bash
./sq-dce-benchmark run --config bench.yaml
```

Per target it will: scan the seed once → validate one replay (fail fast if versions
differ) → pre-create N projects → replay N in parallel while sampling the queue every
second → collect `api/ce/activity` metrics → delete the bench projects. Then it writes
the comparison **PDF** (`report:` path).

### Run targets independently (recommended when EE and DCE share hardware)

If both instances aren't on separate hardware, run each **on its own** so they don't
compete for CPU — results accumulate into `results_file` and the report combines them:

```bash
./sq-dce-benchmark run    --config bench.yaml --only EE     # (DCE idle/stopped)
./sq-dce-benchmark run    --config bench.yaml --only DCE    # (EE idle/stopped)
./sq-dce-benchmark report --config bench.yaml               # combined PDF from results.json
```

Clean up anytime (e.g. after an interrupted run):

```bash
./sq-dce-benchmark cleanup --config bench.yaml
```

## Example run & output

A filled-in `bench.yaml` (tokens redacted):

```yaml
seed_repo: /repos/acme-web          # a representative ~78K-ncloc service
n: 40
concurrency: 12
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

Run each target while the other is idle so they don't compete for CPU:

```text
$ ./sq-dce-benchmark run --config bench.yaml --only DCE-12
⚠  Non-production benchmark — replaying internal report format via api/ce/submit.

=== DCE-12  (https://sonarqube-dce.acme.internal) ===
  scanning seed once ...
  detected CE workers: 12 (4/node × 3)
  validating one replay ...
  ✓ replay valid (ncloc=77774)
  pre-creating 40 projects ...
  staging 40 reports ...
  firing burst (N=40, concurrency=12) ...
  → 40/40 ok | drain 15s | wait avg 5.6s p95 11s | throughput 9600/hr

$ ./sq-dce-benchmark run --config bench.yaml --only EE-6
=== EE-6  (https://sonarqube-ee.acme.internal) ===
  scanning seed once ...
  detected CE workers: 6
  validating one replay ...
  ✓ replay valid (ncloc=77774)
  ...
  → 40/40 ok | drain 39s | wait avg 16.8s p95 31s | throughput 3692/hr

Report written: ./acme-ee-vs-dce.pdf
```

The report contains a side-by-side table (illustrative numbers) plus a
queue-size-over-time chart:

| Metric — N=40 burst | EE | DCE |
|---|---|---|
| Workers (detected) | 6 | 12 (4/node × 3) |
| Tasks OK | 40 | 40 |
| Queue drain (s) | 39 | **15** |
| Throughput (tasks/hr) | 3,692 | **9,600** |
| Avg queue wait (s) | 16.8 | **5.6** |
| p95 queue wait (s) | 31 | **11** |
| CE time / task (s) | 5.0 | 3.4 |

Lower is better on every row. Your numbers will vary with hardware, project size, `n`,
and worker counts — run EE and DCE on **separate hardware** for a representative result.

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

A compact vector PDF with a side-by-side metrics table (drain time, throughput, avg/p95
queue wait, CE time per task) and a **queue-size-over-time** chart.

### Production model (optional)

If you add a `model:` block to `bench.yaml`, the report also includes a **production
model**: it takes the *measured* CE time per analysis and each target's *auto-detected*
workers/nodes, and projects the **average feedback delay vs. load** for a developer
population you choose:

```yaml
model:
  devs: 5000                # developer population
  analyses_per_dev_day: 15  # PRs, branches, CI per dev/day
  peak_fraction: 0.25       # share landing in the peak hour
```

It renders the assumptions (e.g. *5,000 devs × 15/day = 75,000/day; ~25% peak ≈ 18,750/hr*),
a capacity/utilisation/feedback-delay table — the **measured** EE and DCE configs plus
auto-generated **DCE sizing scenarios** (several node × workers/node combinations, with the
recommended one highlighted) — and a chart showing where each configuration saturates as
load rises. Change `devs` and re-run `report` to re-model instantly.

## Files

| File | Purpose |
|---|---|
| `sq-dce-benchmark` | the CLI binary (`run` / `cleanup` / `report` / `version`) |
| `bench.example.yaml` | config template (copy to `bench.yaml`) |
| `build-release.sh` | cross-compile the binaries for all platforms |
| `*.go`, `go.mod`, `go.sum` | Go sources — only needed to build from source |
| `sample-project/` | tiny sample for a **cli** smoke test |
| `sample-java/` | tiny self-contained Maven project for a **Java (maven)** smoke test (builds offline) |

## Notes / attribution

The scanner-report format this tool replays is SonarSource's open-source (LGPL)
scanner-report schema. The binary re-keys only a few known fields at the protobuf **wire
level** (via `google.golang.org/protobuf`), preserving all other fields — so no schema
files are needed at runtime and it stays compatible across SonarQube versions.
