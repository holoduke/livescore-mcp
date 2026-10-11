package main

// Football Mania API access and the small helpers the formatters share.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const siteURL = "https://football-mania.com"

// Raw JSON answers are capped: a day of fixtures is over 600 KB, far more
// than a model's context can use
const maxJSONChars = 60000

var httpClient = &http.Client{Timeout: 20 * time.Second}

// Languages football-mania.com serves under /<lang>/ (English has no prefix)
var pathLangs = map[string]bool{}

func init() {
	for _, l := range strings.Fields(`es de fr it pt nl ar ru cs da el fa hi hr id is ja ko no pa pl ro sv th tr uk vi
		zh-CN zh-HK zh-TW hu fi ms he bn sw sr bg sq az mk am uz kk ka hy sk sl ha so pt-BR my km ne ur ml ta`) {
		pathLangs[l] = true
	}
}

type obj = map[string]any

// --- Arguments ---

func args(req any) obj {
	if m, ok := req.(map[string]any); ok {
		return m
	}
	return obj{}
}

func argStr(a obj, key, fallback string) string {
	switch v := a[key].(type) {
	case string:
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fallback
}

func argInt(a obj, key string, fallback int) int {
	switch v := a[key].(type) {
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return fallback
}

func argBool(a obj, key string, fallback bool) bool {
	switch v := a[key].(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v == "true" || v == "1" || v == "yes"
	}
	return fallback
}

// language returns the requested language; the API has no regional variants
func language(a obj) (site, api string) {
	site = argStr(a, "language", defaultLang)
	if site == "pt-br" {
		site = "pt-BR"
	}
	api = site
	if api == "pt-BR" {
		api = "pt"
	}
	return site, api
}

// --- HTTP ---

func fetchJSON(ctx context.Context, path string, lang string, extra url.Values) (any, []byte, error) {
	u, _ := url.Parse(baseURL)
	u.Path, _ = url.JoinPath(u.Path, path)
	q := url.Values{}
	q.Set("lang", lang)
	q.Set("version", strconv.Itoa(defaultVersion))
	for k, vs := range extra {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("football data service unreachable: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("reading response: %v", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("football data service answered HTTP %d", resp.StatusCode)
	}
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, nil, fmt.Errorf("unexpected response from football data service")
	}
	return data, body, nil
}

var errNotFound = fmt.Errorf("not found")

// userAgent identifies this server upstream; UPSTREAM_USER_AGENT overrides it
// for local development
func userAgent() string {
	if ua := os.Getenv("UPSTREAM_USER_AGENT"); ua != "" {
		return ua
	}
	return "LiveScore-MCP/" + serverVersion
}

func httpNewHead(ctx context.Context, u string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", u, nil)
	if err == nil {
		req.Header.Set("User-Agent", userAgent())
	}
	return req, err
}

// compactJSON returns the raw API answer, minified and capped
func compactJSON(body []byte) string {
	var b bytes.Buffer
	if err := json.Compact(&b, body); err != nil {
		b.Reset()
		b.Write(body)
	}
	s := b.String()
	if len(s) > maxJSONChars {
		return s[:maxJSONChars] + fmt.Sprintf("\n…[truncated: %d of %d characters shown; use the default text format or narrower filters]", maxJSONChars, len(s))
	}
	return s
}

// --- Values ---

func str(m obj, key string) string {
	switch v := m[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "1"
		}
		return "0"
	}
	return ""
}

func num(m obj, key string) int {
	n, _ := strconv.Atoi(str(m, key))
	return n
}

func list(v any) []obj {
	arr, _ := v.([]any)
	out := make([]obj, 0, len(arr))
	for _, x := range arr {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func sub(m obj, key string) obj {
	o, _ := m[key].(map[string]any)
	if o == nil {
		return obj{}
	}
	return o
}

// --- Links to football-mania.com ---

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(name) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(b.String()), "-"), "-")
}

func link(lang, path string) string {
	if lang != "en" && pathLangs[lang] {
		return siteURL + "/" + lang + path
	}
	return siteURL + path
}

func slugPath(kind, id, name string) string {
	if s := slugify(name); s != "" {
		return fmt.Sprintf("/%s/%s-%s", kind, s, id)
	}
	return fmt.Sprintf("/%s/%s", kind, id)
}

func matchLink(lang string, m obj) string {
	id, home, away := str(m, "id"), str(m, "localteam"), str(m, "visitorteam")
	if slugify(home) == "" || slugify(away) == "" {
		return link(lang, "/match/"+id)
	}
	return link(lang, fmt.Sprintf("/match/%s-%s", slugify(home+" vs "+away), id))
}

func teamLink(lang, id, name string) string   { return link(lang, slugPath("team", id, name)) }
func playerLink(lang, id, name string) string { return link(lang, slugPath("player", id, name)) }
func leagueLink(lang, key, tab string) string {
	if tab != "" {
		return link(lang, "/fixtures/"+key+"/"+tab)
	}
	return link(lang, "/fixtures/"+key)
}

// --- Match status ---

var clockRe = regexp.MustCompile(`^\d{1,2}:\d{2}$`)

func isFinished(status string) bool {
	switch status {
	case "FT", "AET", "Pen.", "After Pen.", "Awarded", "Aw.", "Abandoned", "Aban.":
		return true
	}
	return false
}

func isLive(status string) bool {
	if status == "HT" || status == "ET" || status == "Break" || status == "Pen" || status == "P" {
		return true
	}
	n, err := strconv.Atoi(strings.TrimSuffix(status, "+"))
	return err == nil && n >= 0 && n <= 130
}

func isOff(status string) bool {
	switch status {
	case "Postp.", "Canc.", "Cancl.", "Susp.", "Int.", "Delayed", "TBA":
		return true
	}
	return false
}

// kickoff parses the UTC date ("21/08/2026") and time ("19:00") of a match
func kickoff(m obj) (time.Time, bool) {
	t, err := time.Parse("02/01/2006 15:04", str(m, "date")+" "+str(m, "time"))
	if err != nil {
		t, err = time.Parse("02/01/2006", str(m, "date"))
		if err != nil {
			return time.Time{}, false
		}
	}
	return t, true
}

func score(m obj) string {
	s := strings.Join(strings.Fields(str(m, "scoretime")), " ")
	if s == "-" || s == "" || s == "? - ?" {
		return ""
	}
	return s
}

// statusLabel: "FT", "67'", "HT", "19:00 UTC", "Postponed"…
func statusLabel(m obj) string {
	st := str(m, "status")
	switch {
	case isLive(st) && st != "HT" && st != "Break":
		return "LIVE " + st + "'"
	case st == "HT":
		return "LIVE HT"
	case isFinished(st) || isOff(st):
		return st
	case st == "Not Started" || clockRe.MatchString(st) || st == "":
		if t := str(m, "time"); t != "" {
			return t + " UTC"
		}
		return "not started"
	}
	return st
}

// matchLine: "FT · Arsenal 3 - 0 Coventry City · match 3868802 · <link>"
func matchLine(lang string, m obj, withDate bool) string {
	middle := " vs "
	if sc := score(m); sc != "" && str(m, "status") != "Not Started" && !clockRe.MatchString(str(m, "status")) {
		middle = " " + sc + " "
	}
	extra := ""
	if p := strings.TrimSpace(str(m, "penaltyLocalTeam")); p != "" {
		extra = fmt.Sprintf(" (pens %s-%s)", p, str(m, "penaltyVisitorTeam"))
	}
	when := statusLabel(m)
	if withDate {
		when = str(m, "date") + " " + when
	}
	return fmt.Sprintf("%s · %s%s%s%s · match %s · %s", when, str(m, "localteam"), middle, str(m, "visitorteam"), extra, str(m, "id"), matchLink(lang, m))
}

// --- Dates ---

// parseDate accepts today/tomorrow/yesterday, YYYY-MM-DD and DD/MM/YYYY and
// returns the API's DD/MM/YYYY
func parseDate(s string, now time.Time) (string, error) {
	switch strings.ToLower(s) {
	case "", "today":
		return now.Format("02/01/2006"), nil
	case "tomorrow":
		return now.AddDate(0, 0, 1).Format("02/01/2006"), nil
	case "yesterday":
		return now.AddDate(0, 0, -1).Format("02/01/2006"), nil
	}
	for _, layout := range []string{"2006-01-02", "02/01/2006", "2/1/2006", "02-01-2006", "02.01.2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("02/01/2006"), nil
		}
	}
	return "", fmt.Errorf("date %q not understood; use YYYY-MM-DD, DD/MM/YYYY, today, tomorrow or yesterday", s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string { return strings.TrimSpace(tagRe.ReplaceAllString(s, "")) }
