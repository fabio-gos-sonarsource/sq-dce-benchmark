#!/usr/bin/env python3
"""
sq-dce-benchmark — Compute Engine throughput benchmark for SonarQube EE vs DCE.

Idea ("replay"): scan a seed project ONCE per target, then replay that one report
N times (each as a distinct project) to create an INSTANT Compute Engine burst.
Because the heavy scanning happens only once, the CE — not the scanner — is the
bottleneck, so you measure the server's real throughput and queue behaviour.

Each replayed submission is processed as a full, real CE task (same rules, issues
and measures as a genuine analysis); only the redundant client-side scan is skipped.

Flow per target:  scan seed -> validate 1 replay -> pre-create N -> replay N in
parallel (sampling the queue every second) -> collect api/ce/activity -> clean up.
Then a comparison PDF is generated.

⚠️  NON-PRODUCTION BENCHMARK. It POSTs SonarScanner's internal report format to
    api/ce/submit (unsupported internals). Run only against non-prod instances.

Usage:
    python3 sq_bench.py run     --config bench.yaml
    python3 sq_bench.py cleanup --config bench.yaml
"""
import argparse, os, sys, time, glob, shutil, subprocess, tempfile, threading, csv, re, json
import datetime as dt
import concurrent.futures as cf

try:
    import yaml, requests
except ImportError:
    sys.exit("Missing deps. Run:  pip install -r requirements.txt")

HERE = os.path.dirname(os.path.abspath(__file__))
PB = None  # protobuf module, loaded at runtime


# ----------------------------- proto -----------------------------
def load_proto():
    """Compile the bundled scanner-report proto at runtime (protobuf-version safe)."""
    try:
        from grpc_tools import protoc
    except ImportError:
        sys.exit("Missing grpcio-tools. Run:  pip install -r requirements.txt")
    out = tempfile.mkdtemp(prefix="sqbench_pb_")
    pdir = os.path.join(HERE, "proto")
    rc = protoc.main(["", f"-I{pdir}", f"--python_out={out}",
                      "scanner_report.proto", "constants.proto"])
    if rc != 0:
        sys.exit("Failed to compile bundled proto files.")
    sys.path.insert(0, out)
    import scanner_report_pb2 as pb
    return pb


# ----------------------------- http helpers -----------------------------
def _auth(t): return (t["token"], "")
def get(t, path, **params):
    return requests.get(t["url"].rstrip("/") + path, params=params, auth=_auth(t), timeout=30)
def post(t, path, **params):
    return requests.post(t["url"].rstrip("/") + path, params=params, auth=_auth(t), timeout=60)


# ----------------------------- scanning / reports -----------------------------
DEFAULT_EXCL = "**/node_modules/**,**/dist/**,**/*.min.js,**/*.pyc,**/__pycache__/**"

def scan_seed(t, cfg, workdir):
    key = cfg["namespace"] + "-seed"
    cmd = [cfg.get("scanner", "sonar-scanner"),
           f'-Dsonar.host.url={t["url"]}', f'-Dsonar.token={t["token"]}',
           f'-Dsonar.projectKey={key}', f'-Dsonar.projectName={key}',
           f'-Dsonar.projectBaseDir={cfg["seed_repo"]}', '-Dsonar.sources=.',
           f'-Dsonar.working.directory={workdir}',
           '-Dsonar.scm.disabled=true', '-Dsonar.scanner.keepReport=true',
           f'-Dsonar.exclusions={cfg.get("exclusions", DEFAULT_EXCL)}']
    r = subprocess.run(cmd, capture_output=True, text=True)
    rep = os.path.join(workdir, "scanner-report")
    if not os.path.exists(os.path.join(rep, "metadata.pb")):
        sys.stderr.write((r.stdout or "")[-1500:] + (r.stderr or "")[-1500:] + "\n")
        raise RuntimeError(f"[{t['name']}] seed scan produced no report — check scanner + host/token")
    post(t, "/api/projects/bulk_delete", q=key)          # keep only the report, not the project
    return rep

def fetch_profiles(t):
    return {q["language"]: q["key"] for q in get(t, "/api/qualityprofiles/search",
                                                 defaults="true").json().get("profiles", [])}

def detect_workers(t):
    """Read the REAL CE worker count from the instance: per-node × application nodes."""
    try:
        per = get(t, "/api/ce/worker_count").json().get("value")
    except Exception:
        per = None
    if per is None:
        return None
    nodes = 1
    try:
        app = [n for n in get(t, "/api/system/health").json().get("nodes", [])
               if n.get("type") == "APPLICATION"]
        if app:
            nodes = len(app)
    except Exception:
        pass
    total = per * nodes
    return dict(per_node=per, app_nodes=nodes, total=total,
               detail=(f"{total} ({per}/node × {nodes})" if nodes > 1 else str(total)))

def patch_report(report_dir, new_key, date_ms, profiles):
    """Re-key a copied report so it can be replayed as a different project on this server."""
    md_path = os.path.join(report_dir, "metadata.pb")
    md = PB.Metadata(); md.ParseFromString(open(md_path, "rb").read())
    old_key = md.project_key
    md.project_key = new_key
    md.analysis_date = date_ms
    for lang in list(md.qprofiles_per_language.keys()):
        if lang in profiles:
            md.qprofiles_per_language[lang].key = profiles[lang]
        else:
            del md.qprofiles_per_language[lang]           # target lacks this language's profile
    open(md_path, "wb").write(md.SerializeToString())
    for cpb in glob.glob(os.path.join(report_dir, "component-*.pb")):
        c = PB.Component(); c.ParseFromString(open(cpb, "rb").read()); changed = False
        if c.key == old_key:  c.key = new_key;  changed = True
        if c.name == old_key: c.name = new_key; changed = True
        if changed:
            open(cpb, "wb").write(c.SerializeToString())

def stage_zip(report_dir, key, date_ms, profiles, stage_root):
    d = os.path.join(stage_root, key)
    shutil.copytree(report_dir, d)
    for c in glob.glob(os.path.join(d, "analysis-cache*.pb")):
        os.remove(c)                                       # avoid cache-insert races on identical clones
    patch_report(d, key, date_ms, profiles)
    shutil.make_archive(d, "zip", d)                       # -> <d>.zip with report files at root
    return d + ".zip"

def submit(t, key, zip_path):
    with open(zip_path, "rb") as f:
        return requests.post(t["url"].rstrip("/") + "/api/ce/submit",
                             params={"projectKey": key, "projectName": key},
                             files={"report": ("scanner-report.zip", f, "application/zip")},
                             auth=_auth(t), timeout=180)

def precreate(t, keys):
    for k in keys:
        post(t, "/api/projects/create", project=k, name=k)


# ----------------------------- sampling / waiting -----------------------------
class Sampler(threading.Thread):
    def __init__(self, t, csv_path):
        super().__init__(daemon=True); self.t = t; self.csv = csv_path; self.stop = False
    def run(self):
        with open(self.csv, "w") as f:
            f.write("epoch,pending,inProgress\n"); f.flush()
            while not self.stop:
                try:
                    d = get(self.t, "/api/ce/activity_status").json()
                    f.write(f"{int(time.time())},{d.get('pending',0)},{d.get('inProgress',0)}\n"); f.flush()
                except Exception:
                    pass
                time.sleep(1)

def wait_drain(t, timeout=1800):
    start = time.time()
    while time.time() - start < timeout:
        try:
            d = get(t, "/api/ce/activity_status").json()
            if d.get("pending", 0) + d.get("inProgress", 0) == 0:
                return
        except Exception:
            pass
        time.sleep(2)

def validate_status(t, key, timeout=90):
    """Confirm a single replayed task SUCCEEDS and produces measures (fail fast on drift)."""
    start = time.time()
    while time.time() - start < timeout:
        tasks = get(t, "/api/ce/activity", component=key, ps=1).json().get("tasks", [])
        if tasks and tasks[0]["status"] in ("SUCCESS", "FAILED", "CANCELED"):
            if tasks[0]["status"] != "SUCCESS":
                return False, tasks[0].get("errorMessage", "task failed")
            m = get(t, "/api/measures/component", component=key, metricKeys="ncloc").json()
            ncloc = next((x["value"] for x in m.get("component", {}).get("measures", [])), "0")
            return (int(ncloc) > 0), f"ncloc={ncloc}"
        time.sleep(2)
    return False, "timed out"


# ----------------------------- metrics -----------------------------
def _p(s): return dt.datetime.fromisoformat(s) if s else None

def collect(t, namespace):
    tasks = get(t, "/api/ce/activity", q=namespace, ps=500,
                status="SUCCESS,FAILED,CANCELED").json().get("tasks", [])
    pat = re.compile(re.escape(namespace) + r"-\d+$")
    rows = []
    for x in tasks:
        if not pat.match(x.get("componentKey", "")):
            continue
        sub, st, ex = _p(x.get("submittedAt")), _p(x.get("startedAt")), _p(x.get("executedAt"))
        if not (sub and st and ex):
            continue
        rows.append(dict(wait=(st - sub).total_seconds(),
                         proc=int(x.get("executionTimeMs", 0)) / 1000,
                         status=x.get("status"), sub=sub, ex=ex))
    if not rows:
        return dict(n=0, ok=0)
    rows.sort(key=lambda r: r["sub"])
    n = len(rows); ok = sum(1 for r in rows if r["status"] == "SUCCESS")
    drain = (max(r["ex"] for r in rows) - min(r["sub"] for r in rows)).total_seconds()
    waits = sorted(r["wait"] for r in rows); procs = [r["proc"] for r in rows]
    def pct(a, q): return a[min(len(a) - 1, max(0, int(round(q * len(a)) - 1)))]
    return dict(n=n, ok=ok, drain=drain, thr=n / drain * 3600 if drain else 0,
                wait_avg=sum(waits) / n, wait_p95=pct(waits, .95), wait_max=max(waits),
                proc_avg=sum(procs) / n)

def queue_stats(csv_path):
    try:
        rows = [(int(r["pending"]), int(r["inProgress"])) for r in csv.DictReader(open(csv_path))]
    except Exception:
        return dict(q_avg=0, q_peak=0, series=[])
    act = [r for r in rows if r[0] + r[1] > 0]
    if not act:
        return dict(q_avg=0, q_peak=0, series=[])
    pend = [r[0] for r in act]
    return dict(q_avg=sum(pend) / len(pend), q_peak=max(pend), series=pend)


# ----------------------------- report -----------------------------
def generate_report(cfg, results):
    import matplotlib; matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    from reportlab.lib.pagesizes import A4
    from reportlab.lib.units import cm
    from reportlab.lib import colors
    from reportlab.lib.styles import getSampleStyleSheet, ParagraphStyle
    from reportlab.platypus import (SimpleDocTemplate, Paragraph, Spacer, Table, TableStyle,
                                    Image, HRFlowable)
    INK="#22303C"; MUT="#7A8A99"; GRID="#DDE4EA"; PAL=["#E8833A","#1F8FD6","#2F9E6E","#9B59B6"]
    A = tempfile.mkdtemp(prefix="sqbench_rep_")
    names = list(results.keys())
    cols = {n: PAL[i % len(PAL)] for i, n in enumerate(names)}
    plt.rcParams.update({"font.size":11,"axes.edgecolor":MUT,"axes.labelcolor":INK,"text.color":INK,
                         "xtick.color":MUT,"ytick.color":MUT,"axes.grid":True,"grid.color":GRID,"figure.dpi":150})

    # queue-over-time
    fig, ax = plt.subplots(figsize=(6.6, 3.6))
    for n in names:
        s = results[n].get("series", [])
        if s:
            ax.fill_between(range(len(s)), s, color=cols[n], alpha=0.12)
            ax.plot(range(len(s)), s, color=cols[n], lw=2.4,
                    label=f"{n} — {results[n].get('workers','?')} workers")
    ax.set_xlabel("seconds after burst"); ax.set_ylabel("analyses waiting in queue")
    ax.set_title("Queue size over time (measured)", fontsize=11, pad=8)
    ax.legend(frameon=False, fontsize=9)
    for sp in ("top", "right"): ax.spines[sp].set_visible(False)
    fig.tight_layout(); fig.savefig(f"{A}/q.png", bbox_inches="tight"); plt.close(fig)

    st = getSampleStyleSheet()
    H1 = ParagraphStyle("H1", parent=st["Heading1"], textColor=colors.HexColor(INK), fontSize=18, spaceAfter=4)
    H2 = ParagraphStyle("H2", parent=st["Heading2"], textColor=colors.HexColor("#1F8FD6"), fontSize=13, spaceBefore=10, spaceAfter=4)
    BODY = ParagraphStyle("BODY", parent=st["BodyText"], textColor=colors.HexColor(INK), fontSize=10, leading=14)
    SMALL = ParagraphStyle("SMALL", parent=BODY, fontSize=8.5, textColor=colors.HexColor(MUT), leading=11)
    out = cfg.get("report", "sq-dce-benchmark-report.pdf")
    doc = SimpleDocTemplate(out, pagesize=A4, topMargin=1.4*cm, bottomMargin=1.2*cm,
                            leftMargin=1.6*cm, rightMargin=1.6*cm, title="SonarQube EE vs DCE — CE throughput benchmark")
    E = [Paragraph("SonarQube — Compute Engine throughput benchmark", H1),
         Paragraph(f"Burst of N={cfg['n']} analyses · seed: {os.path.basename(cfg['seed_repo'].rstrip('/'))} · replay method",
                   SMALL),
         HRFlowable(width="100%", color=colors.HexColor(GRID), spaceBefore=6, spaceAfter=6)]

    # comparison table
    hdr = ["Metric"] + names
    def row(label, key, fmt):
        return [label] + [fmt(results[n].get(key)) for n in names]
    f1 = lambda v: "—" if v is None else f"{v:.1f}"
    f0 = lambda v: "—" if v is None else f"{v:.0f}"
    data = [hdr,
            ["Workers (detected)"] + [str(results[n].get("workers_detail", results[n].get("workers", "?"))) for n in names],
            row("Tasks OK", "ok", lambda v: str(v)),
            row("Queue drain (s)", "drain", f0),
            row("Throughput (tasks/hr)", "thr", f0),
            row("Avg queue wait (s)", "wait_avg", f1),
            row("p95 queue wait (s)", "wait_p95", f0),
            row("Avg queue size", "q_avg", f1),
            row("Peak queue size", "q_peak", f0),
            row("CE time / task (s)", "proc_avg", f1)]
    tbl = Table(data, colWidths=[6.2*cm] + [ (10.2/len(names))*cm ]*len(names))
    tbl.setStyle(TableStyle([("BACKGROUND",(0,0),(-1,0),colors.HexColor(INK)),("TEXTCOLOR",(0,0),(-1,0),colors.white),
        ("FONTSIZE",(0,0),(-1,-1),9),("FONTNAME",(0,0),(-1,0),"Helvetica-Bold"),
        ("GRID",(0,0),(-1,-1),0.4,colors.HexColor(GRID)),
        ("ROWBACKGROUNDS",(0,1),(-1,-1),[colors.white,colors.HexColor("#F4F7F9")]),
        ("VALIGN",(0,0),(-1,-1),"MIDDLE"),("TOPPADDING",(0,0),(-1,-1),4),("BOTTOMPADDING",(0,0),(-1,-1),4)]))
    E += [tbl, Spacer(1, 8), Image(f"{A}/q.png", width=15.5*cm, height=8.4*cm)]
    E.append(Paragraph("Measured on the customer's instances via the replay method (one scan per target, then N "
        "instant re-submissions) so the Compute Engine — not the scanner — is the bottleneck. Lower is better on "
        "every row. A cluster with more workers across nodes drains the queue faster and keeps developer wait low.",
        SMALL))
    E.append(HRFlowable(width="100%", color=colors.HexColor(GRID), spaceBefore=6, spaceAfter=6))
    E.append(Paragraph("Non-production benchmark. Load generated by replaying SonarScanner's internal report format "
        "via api/ce/submit. Ensure both instances run the same version, comparable hardware/DB, and equal per-worker "
        "CE heap for a fair comparison.", SMALL))
    doc.build(E)
    print(f"\nReport written: {out}")


# ----------------------------- orchestration -----------------------------
def run_target(t, cfg):
    print(f"\n=== {t['name']}  ({t['url']}) ===")
    work = tempfile.mkdtemp(prefix=f"sqbench_{t['name']}_")
    stage = tempfile.mkdtemp(prefix=f"sqstage_{t['name']}_")
    ns = cfg["namespace"]
    post(t, "/api/projects/bulk_delete", q=ns)                         # clean slate
    print("  scanning seed once ...")
    rep = scan_seed(t, cfg, work)
    profiles = fetch_profiles(t)
    w = detect_workers(t)
    w_total = w["total"] if w else t.get("workers", "?")
    w_detail = w["detail"] if w else str(t.get("workers", "?"))
    print(f"  detected CE workers: {w_detail}")

    print("  validating one replay ...")
    vkey = f"{ns}-000000"
    post(t, "/api/projects/create", project=vkey, name=vkey)
    submit(t, vkey, stage_zip(rep, vkey, int(time.time() * 1000), profiles, stage))
    ok, info = validate_status(t, vkey)
    post(t, "/api/projects/bulk_delete", q=vkey)
    if not ok:
        sys.exit(f"  ✗ validation replay failed ({info}). Versions/config likely differ — aborting {t['name']}.")
    print(f"  ✓ replay valid ({info})")

    N = cfg["n"]
    keys = [f"{ns}-{i:04d}" for i in range(1, N + 1)]
    print(f"  pre-creating {N} projects ...")
    precreate(t, keys)
    print(f"  staging {N} reports ...")
    base = int(time.time() * 1000)
    zips = {k: stage_zip(rep, k, base - (N - i) * 2000, profiles, stage) for i, k in enumerate(keys, 1)}

    csvp = os.path.join(work, "queue.csv")
    smp = Sampler(t, csvp); smp.start()
    print(f"  firing burst (N={N}, concurrency={cfg.get('concurrency',12)}) ...")
    with cf.ThreadPoolExecutor(max_workers=cfg.get("concurrency", 12)) as ex:
        list(ex.map(lambda k: submit(t, k, zips[k]), keys))
    wait_drain(t)
    time.sleep(2); smp.stop = True; smp.join(timeout=3)

    m = collect(t, ns); m.update(queue_stats(csvp))
    m["workers"] = w_total; m["workers_detail"] = w_detail
    print(f"  → {m.get('ok',0)}/{m.get('n',0)} ok | drain {m.get('drain',0):.0f}s | "
          f"wait avg {m.get('wait_avg',0):.1f}s p95 {m.get('wait_p95',0):.0f}s | "
          f"queue avg {m.get('q_avg',0):.1f} peak {m.get('q_peak',0):.0f}")
    if not cfg.get("keep_projects"):
        post(t, "/api/projects/bulk_delete", q=ns)
        shutil.rmtree(stage, ignore_errors=True)
    return m

def cmd_run(cfg, only=None):
    global PB
    PB = load_proto()
    print("⚠  Non-production benchmark — replaying internal report format via api/ce/submit.")
    rf = cfg.get("results_file", "results.json")
    results = {}
    if os.path.exists(rf):
        try:
            results = json.load(open(rf))           # accumulate across independent runs
        except Exception:
            results = {}
    targets = [t for t in cfg["targets"] if only in (None, t["name"])]
    if not targets:
        sys.exit(f"no target named '{only}' in config")
    for t in targets:
        results[t["name"]] = run_target(t, cfg)
    json.dump(results, open(rf, "w"), indent=2)
    generate_report(cfg, results)

def cmd_report(cfg):
    rf = cfg.get("results_file", "results.json")
    if not os.path.exists(rf):
        sys.exit(f"no results file: {rf}")
    generate_report(cfg, json.load(open(rf)))

def cmd_cleanup(cfg):
    for t in cfg["targets"]:
        r = post(t, "/api/projects/bulk_delete", q=cfg["namespace"])
        print(f"{t['name']}: deleted '{cfg['namespace']}*' -> HTTP {r.status_code}")


def load_config(path):
    with open(path) as f:
        cfg = yaml.safe_load(f)
    for req in ("targets", "seed_repo", "n", "namespace"):
        if req not in cfg:
            sys.exit(f"config missing '{req}'")
    return cfg

def main():
    ap = argparse.ArgumentParser(description="SonarQube EE vs DCE Compute Engine throughput benchmark")
    ap.add_argument("command", choices=["run", "cleanup", "report"])
    ap.add_argument("--config", required=True)
    ap.add_argument("--only", help="run only this target (accumulates into results_file for a combined report)")
    a = ap.parse_args()
    cfg = load_config(a.config)
    if a.command == "run":
        cmd_run(cfg, a.only)
    elif a.command == "cleanup":
        cmd_cleanup(cfg)
    else:
        cmd_report(cfg)

if __name__ == "__main__":
    main()
