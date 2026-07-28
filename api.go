package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var httpClient = &http.Client{}

const maxBody = 16 << 20 // 16 MiB — headroom for large api/ce/activity responses

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// newRequest builds a request with basic-token auth and the headers that keep
// ngrok / proxies from returning an HTML interstitial instead of JSON.
func newRequest(ctx context.Context, method, u, token string, body io.Reader) *http.Request {
	req, _ := http.NewRequestWithContext(ctx, method, u, body)
	req.SetBasicAuth(token, "")
	req.Header.Set("ngrok-skip-browser-warning", "true")
	req.Header.Set("User-Agent", "sq-benchmark/1.0")
	return req
}

func targetURL(t Target, path string, q url.Values) string {
	u := strings.TrimRight(t.URL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func get(t Target, path string, q url.Values) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return httpClient.Do(newRequest(ctx, "GET", targetURL(t, path, q), t.Token, nil))
}

func post(t Target, path string, q url.Values) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return httpClient.Do(newRequest(ctx, "POST", targetURL(t, path, q), t.Token, nil))
}

// getJSON does a GET and decodes JSON into v, RETRYING transient failures
// (connection resets, empty/truncated bodies, invalid JSON, 5xx/429) — common on
// flaky tunnels like ngrok's free tier under load. Auth failures (401/403) are fatal
// immediately. Used for read-only calls whose result we can't afford to lose (e.g.
// collecting metrics after the burst).
func getJSON(t Target, path string, q url.Values, what string, v any) {
	last := "unknown error"
	for attempt := 1; attempt <= 5; attempt++ {
		r, err := get(t, path, q)
		if err != nil {
			last = err.Error()
		} else {
			body, rerr := io.ReadAll(io.LimitReader(r.Body, maxBody))
			ct := r.Header.Get("Content-Type")
			r.Body.Close()
			switch {
			case r.StatusCode == 401 || r.StatusCode == 403:
				die("[%s] HTTP %d from %s — the token needs admin (create-project + execute-analysis).", what, r.StatusCode, r.Request.URL)
			case rerr != nil:
				last = "reading response body: " + rerr.Error()
			case r.StatusCode >= 500 || r.StatusCode == 429:
				last = fmt.Sprintf("HTTP %d: %s", r.StatusCode, snippet(body))
			case r.StatusCode >= 400 || !strings.Contains(strings.ToLower(ct), "json"):
				die("[%s] expected JSON from %s but got HTTP %d (%s): %s", what, r.Request.URL, r.StatusCode, ct, snippet(body))
			case len(bytes.TrimSpace(body)) == 0:
				last = "empty response body"
			default:
				if e := json.Unmarshal(body, v); e != nil {
					last = "truncated/invalid JSON: " + e.Error()
				} else {
					return // success
				}
			}
		}
		if attempt < 5 {
			time.Sleep(time.Duration(attempt) * time.Second) // linear backoff: 1s,2s,3s,4s
		}
	}
	die("[%s] failed after 5 attempts (last: %s).\n"+
		"  If this is an ngrok tunnel, the free tier can drop large responses under load — retry, or "+
		"use a direct URL / paid tunnel.", what, last)
}

// validateToken is the preflight: token authenticates AND has global Administer System.
func validateToken(t Target) {
	var v struct {
		Valid bool `json:"valid"`
	}
	// getJSON retries transient tunnel/LB blips (empty/truncated bodies, 5xx) so the
	// preflight doesn't abort the whole run on a single hiccup.
	getJSON(t, "/api/authentication/validate", nil, "authentication/validate", &v)
	if !v.Valid {
		die("[%s] token is INVALID for %s — check the target's token in bench.yaml (right instance? revoked/expired?).", t.Name, t.URL)
	}
	var cur struct {
		Permissions struct {
			Global []string `json:"global"`
		} `json:"permissions"`
	}
	getJSON(t, "/api/users/current", nil, "users/current", &cur)
	for _, p := range cur.Permissions.Global {
		if p == "admin" {
			return
		}
	}
	have := "none"
	if len(cur.Permissions.Global) > 0 {
		have = strings.Join(cur.Permissions.Global, ", ")
	}
	die("[%s] token lacks the required 'Administer System' permission.\n"+
		"  This benchmark creates and deletes projects, and reads CE activity, worker count and system\n"+
		"  health — all admin-only endpoints. Global permissions on this token: %s.\n"+
		"  -> Generate the token as a user with global 'Administer System', then set it in bench.yaml.", t.Name, have)
}

func fetchProfiles(t Target) map[string]string {
	var resp struct {
		Profiles []struct {
			Language string `json:"language"`
			Key      string `json:"key"`
		} `json:"profiles"`
	}
	getJSON(t, "/api/qualityprofiles/search", url.Values{"defaults": {"true"}}, "qualityprofiles/search", &resp)
	m := make(map[string]string, len(resp.Profiles))
	for _, p := range resp.Profiles {
		m[p.Language] = p.Key
	}
	return m
}

// Workers holds the detected CE worker layout.
type Workers struct {
	PerNode  int
	AppNodes int
	Total    int
	Detail   string
}

// detectWorkers reads the real CE worker count: per-node × application nodes.
func detectWorkers(t Target) *Workers {
	var wc struct {
		Value *int `json:"value"`
	}
	r, err := get(t, "/api/ce/worker_count", nil)
	if err != nil {
		return nil
	}
	// tolerate non-JSON here (some editions restrict it) rather than exiting
	defer r.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if json.Unmarshal(body, &wc) != nil || wc.Value == nil {
		return nil
	}
	nodes := 1
	var health struct {
		Nodes []struct {
			Type string `json:"type"`
		} `json:"nodes"`
	}
	if hr, herr := get(t, "/api/system/health", nil); herr == nil {
		hb, _ := io.ReadAll(io.LimitReader(hr.Body, 1<<20))
		hr.Body.Close()
		if json.Unmarshal(hb, &health) == nil {
			app := 0
			for _, n := range health.Nodes {
				if n.Type == "APPLICATION" {
					app++
				}
			}
			if app > 0 {
				nodes = app
			}
		}
	}
	per := *wc.Value
	total := per * nodes
	detail := fmt.Sprintf("%d", total)
	if nodes > 1 {
		detail = fmt.Sprintf("%d (%d/node × %d)", total, per, nodes)
	}
	return &Workers{PerNode: per, AppNodes: nodes, Total: total, Detail: detail}
}
