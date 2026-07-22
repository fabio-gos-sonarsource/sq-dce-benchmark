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
