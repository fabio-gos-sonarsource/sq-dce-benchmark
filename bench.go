package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Metrics is one target's result; the JSON tags are the field names in results.json.
type Metrics struct {
	N              int     `json:"n"`
	OK             int     `json:"ok"`
	Drain          float64 `json:"drain"`
	Thr            float64 `json:"thr"`
	WaitAvg        float64 `json:"wait_avg"`
	WaitP95        float64 `json:"wait_p95"`
	WaitMax        float64 `json:"wait_max"`
	ProcAvg        float64 `json:"proc_avg"`
	QAvg           float64 `json:"q_avg"`
	QPeak          int     `json:"q_peak"`
	Series         []int   `json:"series"`
	Workers        int     `json:"workers"`
	WorkersDetail  string  `json:"workers_detail"`
	WorkersPerNode int     `json:"workers_per_node"`
	AppNodes       int     `json:"app_nodes"`
}

type ceTask struct {
	Status          string `json:"status"`
	ComponentKey    string `json:"componentKey"`
	SubmittedAt     string `json:"submittedAt"`
	StartedAt       string `json:"startedAt"`
	ExecutedAt      string `json:"executedAt"`
	ExecutionTimeMs int    `json:"executionTimeMs"`
	ErrorMessage    string `json:"errorMessage"`
}

func submit(t Target, key, zipPath string) error {
	f, err := os.Open(zipPath)
	if err != nil {
		return err
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("report", "scanner-report.zip")
	if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	mw.Close()
	q := url.Values{"projectKey": {key}, "projectName": {key}}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	req := newRequest(ctx, "POST", targetURL(t, "/api/ce/submit", q), t.Token, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func precreate(t Target, keys []string) {
	for _, k := range keys {
		post(t, "/api/projects/create", url.Values{"project": {k}, "name": {k}})
	}
}

func activityStatus(t Target) (pending, inProgress int) {
	r, err := get(t, "/api/ce/activity_status", nil)
	if err != nil {
		return 0, 0
	}
	defer r.Body.Close()
	var d struct {
		Pending    int `json:"pending"`
		InProgress int `json:"inProgress"`
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	json.Unmarshal(b, &d)
	return d.Pending, d.InProgress
}

// startSampler polls the queue every second; the returned stop() ends it and
// returns q_avg / q_peak / series (pending-only, over samples where anything was queued).
func startSampler(t Target) func() (float64, int, []int) {
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	var samples [][2]int
	go func() {
		defer close(doneCh)
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-tk.C:
				p, ip := activityStatus(t)
				samples = append(samples, [2]int{p, ip})
			}
		}
	}()
	return func() (float64, int, []int) {
		close(stopCh)
		<-doneCh
		var pend []int
		for _, s := range samples {
			if s[0]+s[1] > 0 {
				pend = append(pend, s[0])
			}
		}
		if len(pend) == 0 {
			return 0, 0, nil
		}
		sum, peak := 0, 0
		for _, p := range pend {
			sum += p
			if p > peak {
				peak = p
			}
		}
		return float64(sum) / float64(len(pend)), peak, pend
	}
}

func fireBurst(t Target, keys []string, zips map[string]string, conc int) {
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, k := range keys {
		wg.Add(1)
		sem <- struct{}{}
		go func(k string) {
			defer wg.Done()
			defer func() { <-sem }()
			submit(t, k, zips[k])
		}(k)
	}
	wg.Wait()
}

func waitDrain(t Target, timeoutSec int) {
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		if p, ip := activityStatus(t); p+ip == 0 {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

func measureNcloc(t Target, key string) int {
	var resp struct {
		Component struct {
			Measures []struct {
				Value string `json:"value"`
			} `json:"measures"`
		} `json:"component"`
	}
	r, err := get(t, "/api/measures/component", url.Values{"component": {key}, "metricKeys": {"ncloc"}})
	if err != nil {
		return 0
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	json.Unmarshal(b, &resp)
	if len(resp.Component.Measures) > 0 {
		n, _ := strconv.Atoi(resp.Component.Measures[0].Value)
		return n
	}
	return 0
}

func validateStatus(t Target, key string) (bool, string) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var resp struct {
			Tasks []ceTask `json:"tasks"`
		}
		if r, err := get(t, "/api/ce/activity", url.Values{"component": {key}, "ps": {"1"}}); err == nil {
			b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			r.Body.Close()
			json.Unmarshal(b, &resp)
			if len(resp.Tasks) > 0 {
				s := resp.Tasks[0].Status
				if s == "SUCCESS" || s == "FAILED" || s == "CANCELED" {
					if s != "SUCCESS" {
						return false, firstNonEmpty(resp.Tasks[0].ErrorMessage, "task failed")
					}
					n := measureNcloc(t, key)
					return n > 0, fmt.Sprintf("ncloc=%d", n)
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false, "timed out"
}

func parseTS(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range []string{"2006-01-02T15:04:05-0700", "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05Z07:00"} {
		if tm, err := time.Parse(l, s); err == nil {
			return tm, true
		}
	}
	return time.Time{}, false
}

func collect(t Target, ns string) Metrics {
	var resp struct {
		Tasks []ceTask `json:"tasks"`
	}
	getJSON(t, "/api/ce/activity", url.Values{"q": {ns}, "ps": {"500"}, "status": {"SUCCESS,FAILED,CANCELED"}}, "ce/activity", &resp)
	re := regexp.MustCompile("^" + regexp.QuoteMeta(ns) + `-\d+$`)
	type row struct {
		wait, proc float64
		status     string
		sub, ex    time.Time
	}
	var rows []row
	for _, x := range resp.Tasks {
		if !re.MatchString(x.ComponentKey) {
			continue
		}
		sub, o1 := parseTS(x.SubmittedAt)
		st, o2 := parseTS(x.StartedAt)
		ex, o3 := parseTS(x.ExecutedAt)
		if !(o1 && o2 && o3) {
			continue
		}
		rows = append(rows, row{st.Sub(sub).Seconds(), float64(x.ExecutionTimeMs) / 1000, x.Status, sub, ex})
	}
	if len(rows) == 0 {
		return Metrics{}
	}
	n := len(rows)
	ok := 0
	minSub, maxEx := rows[0].sub, rows[0].ex
	var waits, procs []float64
	for _, rw := range rows {
		if rw.status == "SUCCESS" {
			ok++
		}
		if rw.sub.Before(minSub) {
			minSub = rw.sub
		}
		if rw.ex.After(maxEx) {
			maxEx = rw.ex
		}
		waits = append(waits, rw.wait)
		procs = append(procs, rw.proc)
	}
	sort.Float64s(waits)
	drain := maxEx.Sub(minSub).Seconds()
	sumW, sumP := 0.0, 0.0
	for _, w := range waits {
		sumW += w
	}
	for _, p := range procs {
		sumP += p
	}
	pct := func(a []float64, q float64) float64 {
		idx := int(math.Round(q*float64(len(a)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx > len(a)-1 {
			idx = len(a) - 1
		}
		return a[idx]
	}
	thr := 0.0
	if drain > 0 {
		thr = float64(n) / drain * 3600
	}
	return Metrics{
		N: n, OK: ok, Drain: drain, Thr: thr,
		WaitAvg: sumW / float64(n), WaitP95: pct(waits, .95), WaitMax: waits[n-1],
		ProcAvg: sumP / float64(n),
	}
}

func runTarget(t Target, cfg *Config) Metrics {
	fmt.Printf("\n=== %s  (%s) ===\n", t.Name, t.URL)
	validateToken(t)
	work, _ := os.MkdirTemp("", "sqbench_"+t.Name+"_")
	stage, _ := os.MkdirTemp("", "sqstage_"+t.Name+"_")
	ns := cfg.Namespace
	post(t, "/api/projects/bulk_delete", url.Values{"q": {ns}})
	rep := seedReport(t, cfg, work)
	profiles := fetchProfiles(t)

	w := detectWorkers(t)
	wTotal, wDetail := 0, "?"
	if w != nil {
		wTotal, wDetail = w.Total, w.Detail
	} else if t.Workers > 0 {
		wTotal, wDetail = t.Workers, strconv.Itoa(t.Workers)
	}
	fmt.Printf("  detected CE workers: %s\n", wDetail)

	fmt.Println("  validating one replay ...")
	vkey := ns + "-validate"
	post(t, "/api/projects/create", url.Values{"project": {vkey}, "name": {vkey}})
	if z, err := stageZip(rep, vkey, time.Now().UnixMilli(), profiles, stage); err == nil {
		submit(t, vkey, z)
	} else {
		die("staging failed: %v", err)
	}
	ok, info := validateStatus(t, vkey)
	post(t, "/api/projects/bulk_delete", url.Values{"q": {vkey}})
	if !ok {
		die("  ✗ validation replay failed (%s). Versions/config likely differ — aborting %s.", info, t.Name)
	}
	fmt.Printf("  ✓ replay valid (%s)\n", info)

	N := cfg.N
	keys := make([]string, N)
	for i := 0; i < N; i++ {
		keys[i] = fmt.Sprintf("%s-%03d", ns, i+1)
	}
	fmt.Printf("  pre-creating %d projects ...\n", N)
	precreate(t, keys)
	fmt.Printf("  staging %d reports ...\n", N)
	base := time.Now().UnixMilli()
	zips := make(map[string]string, N)
	for i, k := range keys {
		z, err := stageZip(rep, k, base-int64(N-(i+1))*2000, profiles, stage)
		if err != nil {
			die("staging failed: %v", err)
		}
		zips[k] = z
	}

	stop := startSampler(t)
	fmt.Printf("  firing burst (N=%d, concurrency=%d) ...\n", N, cfg.concurrency())
	fireBurst(t, keys, zips, cfg.concurrency())
	waitDrain(t, 1800)
	time.Sleep(2 * time.Second)
	qAvg, qPeak, series := stop()

	m := collect(t, ns)
	m.QAvg, m.QPeak, m.Series = qAvg, qPeak, series
	m.Workers, m.WorkersDetail = wTotal, wDetail
	if w != nil {
		m.WorkersPerNode, m.AppNodes = w.PerNode, w.AppNodes
	} else {
		m.AppNodes = 1
	}
	fmt.Printf("  → %d/%d ok | drain %.0fs | wait avg %.1fs p95 %.0fs | throughput %.0f/hr\n",
		m.OK, m.N, m.Drain, m.WaitAvg, m.WaitP95, m.Thr)
	if !cfg.KeepProjects {
		post(t, "/api/projects/bulk_delete", url.Values{"q": {ns}})
		os.RemoveAll(stage)
	}
	os.RemoveAll(work)
	return m
}

func orderNames(cfg *Config, results map[string]Metrics) []string {
	var names []string
	seen := map[string]bool{}
	for _, t := range cfg.Targets {
		if _, ok := results[t.Name]; ok {
			names = append(names, t.Name)
			seen[t.Name] = true
		}
	}
	for k := range results {
		if !seen[k] {
			names = append(names, k)
		}
	}
	return names
}

func cmdRun(cfg *Config, only string) {
	fmt.Println("⚠  Non-production benchmark — replaying internal report format via api/ce/submit.")
	rf := cfg.resultsFile()
	results := map[string]Metrics{}
	if b, err := os.ReadFile(rf); err == nil {
		json.Unmarshal(b, &results)
	}
	var targets []Target
	for _, t := range cfg.Targets {
		if only == "" || only == t.Name {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		die("no target named '%s' in config", only)
	}
	for _, t := range targets {
		results[t.Name] = runTarget(t, cfg)
	}
	b, _ := json.MarshalIndent(results, "", "  ")
	os.WriteFile(rf, b, 0o644)
	generateReport(cfg, results, orderNames(cfg, results))
}

func cmdReport(cfg *Config) {
	rf := cfg.resultsFile()
	b, err := os.ReadFile(rf)
	if err != nil {
		die("no results file: %s", rf)
	}
	results := map[string]Metrics{}
	if err := json.Unmarshal(b, &results); err != nil {
		die("cannot parse %s: %v", rf, err)
	}
	generateReport(cfg, results, orderNames(cfg, results))
}

func cmdCleanup(cfg *Config) {
	for _, t := range cfg.Targets {
		r, err := post(t, "/api/projects/bulk_delete", url.Values{"q": {cfg.Namespace}})
		code := 0
		if err == nil {
			code = r.StatusCode
			r.Body.Close()
		}
		fmt.Printf("%s: deleted '%s*' -> HTTP %d\n", t.Name, cfg.Namespace, code)
	}
}
