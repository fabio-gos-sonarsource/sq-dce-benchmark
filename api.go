package main

import (
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

// jsonInto validates the response is a 2xx JSON body and decodes it, or exits
// with an actionable message when a response isn't JSON.
func jsonInto(r *http.Response, err error, what string, v any) {
	if err != nil {
		die("[%s] request failed: %v", what, err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	ct := r.Header.Get("Content-Type")
	if r.StatusCode >= 400 || !strings.Contains(strings.ToLower(ct), "json") {
		snippet := strings.Join(strings.Fields(string(body)), " ")
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		hint := "\n  -> Check the instance URL is reachable and returns the SonarQube API (not a proxy page)."
		if strings.Contains(r.Request.URL.Host, "ngrok") {
			hint = "\n  -> This is an ngrok tunnel; an HTML body is usually ngrok's browser-warning/error page. Confirm the tunnel is up and the token is valid."
		} else if r.StatusCode == 401 || r.StatusCode == 403 {
			hint = fmt.Sprintf("\n  -> HTTP %d: the token needs admin (create-project + execute-analysis).", r.StatusCode)
		}
		die("[%s] expected JSON from %s but got HTTP %d (%s). First bytes: %q%s",
			what, r.Request.URL, r.StatusCode, ct, snippet, hint)
	}
	if err := json.Unmarshal(body, v); err != nil {
		die("[%s] invalid JSON from %s: %v", what, r.Request.URL, err)
	}
}

// validateToken is the preflight: token authenticates AND has global Administer System.
func validateToken(t Target) {
	var v struct {
		Valid bool `json:"valid"`
	}
	r, err := get(t, "/api/authentication/validate", nil)
	if err != nil {
		die("[%s] cannot reach %s (%v). Check the URL is correct and the instance is up and reachable from here.", t.Name, t.URL, err)
	}
	jsonInto(r, nil, "authentication/validate", &v)
	if !v.Valid {
		die("[%s] token is INVALID for %s — check the target's token in bench.yaml (right instance? revoked/expired?).", t.Name, t.URL)
	}
	var cur struct {
		Permissions struct {
			Global []string `json:"global"`
		} `json:"permissions"`
	}
	rr, err := get(t, "/api/users/current", nil)
	jsonInto(rr, err, "users/current", &cur)
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
	r, err := get(t, "/api/qualityprofiles/search", url.Values{"defaults": {"true"}})
	jsonInto(r, err, "qualityprofiles/search", &resp)
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
