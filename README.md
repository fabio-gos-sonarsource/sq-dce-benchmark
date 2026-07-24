# sq-dce-benchmark

A one-command benchmark that compares **Compute Engine (CE) throughput** between a
SonarQube **Enterprise Edition** node and a **Data Center Edition** cluster, and
produces a PDF report — designed to show the impact of DCE on analysis feedback
(queue behaviour) at scale.

> ⚠️ **Non-production benchmark.** It generates load by replaying SonarScanner's
> internal report format via `api/ce/submit` (unsupported internals). Run it only
> against **non-production** instances.

## Why "replay"?

A normal analysis has two parts: the **scan** (CPU-heavy: `sonar-scanner` runs the
analyzers and produces a *report*), and the **process** step (the server's Compute
Engine ingests that report).

To load-test throughput you need many analyses at once. Running the scanner N times
is heavy and, on one machine, makes the **scanner** the bottleneck — you'd measure
scan time, not the server.

So this tool **scans once**, then **replays that one report N times** (each tagged as
a distinct project). Re-sending a saved report is cheap, so the server's CE gets N
real tasks almost instantly and you measure how fast it clears the queue. Each
replayed task is processed as a **full, real CE task** (same rules, issues, measures);
only the redundant client-side scan is skipped.

## What you need (one machine)

- **Python 3.9+** and the deps in `requirements.txt`
- The **SonarScanner CLI** on `PATH` (or set `scanner:` to its path)
- Network access to both instances
- An **admin token** for each instance (create-project + execute-analysis + admin)

## Setup

```bash
pip install -r requirements.txt
cp bench.example.yaml bench.yaml     # then edit: hosts, tokens, seed_repo, N
```

Point `seed_repo` at a **representative repository** (the bundled `sample-project`
is tiny — good only for a smoke test; a bigger repo gives realistic CE task sizes).

## Run

```bash
python3 sq_bench.py run --config bench.yaml
```

Per target it will: scan the seed once → validate one replay (fail fast if versions
differ) → pre-create N projects → replay N in parallel while sampling the queue every
second → collect `api/ce/activity` metrics → delete the bench projects. Then it writes
the comparison **PDF** (`report:` path).

### Run targets independently (recommended when EE and DCE share hardware)

If both instances aren't on separate hardware, run each **on its own** so they don't
compete for CPU — results accumulate into `results_file` and the report combines them:

```bash
python3 sq_bench.py run    --config bench.yaml --only EE     # (DCE idle/stopped)
python3 sq_bench.py run    --config bench.yaml --only DCE    # (EE idle/stopped)
python3 sq_bench.py report --config bench.yaml               # combined PDF from results.json
```

Clean up anytime (e.g. after an interrupted run):

```bash
python3 sq_bench.py cleanup --config bench.yaml
```

## Example run & output

A filled-in `bench.yaml` (tokens redacted):

```yaml
seed_repo: /repos/acme-web          # a representative ~78K-ncloc service
n: 40
concurrency: 12
scanner: sonar-scanner
namespace: bench
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

Run each target while the other is idle (they shared one host in this lab):

```text
$ python3 sq_bench.py run --config bench.yaml --only DCE-12
⚠  Non-production benchmark — replaying internal report format via api/ce/submit.

=== DCE-12  (https://sonarqube-dce.acme.internal) ===
  scanning seed once ...
  detected CE workers: 12 (4/node × 3)
  validating one replay ...
  ✓ replay valid (ncloc=77774)
  pre-creating 40 projects ...
  staging 40 reports ...
  firing burst (N=40, concurrency=12) ...
  → 40/40 ok | drain 15s | wait avg 5.6s p95 11s | queue avg 14.8 peak 35

$ python3 sq_bench.py run --config bench.yaml --only EE-6
=== EE-6  (https://sonarqube-ee.acme.internal) ===
  scanning seed once ...
  detected CE workers: 6
  validating one replay ...
  ✓ replay valid (ncloc=77774)
  ...
  → 40/40 ok | drain 39s | wait avg 16.8s p95 31s | queue avg 17.7 peak 37

Report written: ./acme-ee-vs-dce.pdf
```

The report contains this table plus a queue-size-over-time chart:

| Metric — N=40 burst | EE-6 | DCE-12 |
|---|---|---|
| Workers (detected) | 6 | 12 (4/node × 3) |
| Tasks OK | 40 | 40 |
| Queue drain (s) | 39 | **15** |
| Throughput (tasks/hr) | 3,692 | **9,600** |
| Avg queue wait (s) | 16.8 | **5.6** |
| p95 queue wait (s) | 31 | **11** |
| Avg queue size | 17.7 | 14.8 |
| Peak queue size | 37 | 35 |
| CE time / task (s) | 5.0 | 3.4 |

**Reading it:** with 2× the workers across nodes, DCE drained the same burst **~2.6× faster**
(15 s vs 39 s) and cut **average developer wait ~67%** (5.6 s vs 16.8 s). On separate
production hardware — where nodes don't share CPUs — the gap is larger still.

> Numbers above are from a same-machine lab (EE and DCE sharing one host, run one at a
> time). Yours will differ with hardware, project size, `n`, and worker counts.

## For a fair, meaningful result

- **Same version** on both instances (the validator aborts if a replay fails).
- **Comparable hardware/DB** for the EE node and each DCE node; **equal per-worker CE
  heap** (~1–2 GB). Undersized heap causes failures that look like "DCE is slow".
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

A PDF with a side-by-side metrics table (drain time, throughput, avg/p95 queue wait,
avg/peak queue size, CE time per task) and a **queue-size-over-time** chart.

### Production model (optional)

If you add a `model:` block to `bench.yaml`, the report also includes a **production
model**: it takes the *measured* CE time per analysis and each target's *auto-detected*
workers/nodes, and projects the **average feedback delay vs. load** for a developer
population you choose:

```yaml
model:
  devs: 5000               # developer population
  analyses_per_dev_day: 8  # PRs, branches, CI per dev/day
  peak_fraction: 0.15      # share landing in the peak hour
```

It renders the assumptions (e.g. *5,000 devs × 8/day = 40,000/day; ~15% peak ≈ 6,000/hr*),
a capacity/utilisation/feedback-delay table per configuration (including a DCE "headroom"
row at more workers/node), and a chart marking where a single EE node saturates while the
DCE cluster stays near zero. Change `devs` and re-run `report` to re-model instantly.

## Files

| File | Purpose |
|---|---|
| `sq_bench.py` | the CLI (`run`, `cleanup`) |
| `bench.example.yaml` | config template (copy to `bench.yaml`) |
| `proto/` | SonarScanner report schema (compiled at runtime, protobuf-version-safe) |
| `sample-project/` | tiny sample for a smoke test |

## Notes / attribution

`proto/scanner_report.proto` and `proto/constants.proto` are SonarSource's
open-source (LGPL) scanner-report schema, included so the tool can read/re-key
reports. Compiled in-memory at runtime to match your installed `protobuf`.
