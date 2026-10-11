package main

// search and fetch in the shape ChatGPT connectors and deep research expect:
// search(query) → {results: [{id, title, url}]}, fetch(id) → {id, title,
// text, url, metadata}, each as structuredContent and as JSON text. IDs are
// typed ("team:9002", "player:474972", "league:EnglandPremierLeague",
// "match:3868859"); every other tool accepts them too.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type searchResult struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
	Text  string `json:"text,omitempty"`
}

type fetchResult struct {
	ID       string            `json:"id"`
	Title    string            `json:"title"`
	Text     string            `json:"text"`
	URL      string            `json:"url"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

var typedIDRe = regexp.MustCompile(`^(team|player|league|match):`)

// untyped strips a "team:" style prefix from an ID
func untyped(id string) string { return typedIDRe.ReplaceAllString(strings.TrimSpace(id), "") }

func structured(v any) *mcp.CallToolResult {
	b, _ := json.Marshal(v)
	return mcp.NewToolResultStructured(v, string(b))
}

func registerSearchFetch(s *server.MCPServer) {
	s.AddTool(tool("search", "Search teams, players, competitions",
		"Find teams, players and competitions by name. Returns results with an id (team:…, player:…, league:…) for fetch or the other tools, a title, a football-mania.com url and a short summary; teams include their last and next match.",
		mcp.WithString("query", mcp.Required(), mcp.Description("Name or part of it (e.g. Arsenal, Mbappé, Premier League)")),
		mcp.WithString("country", mcp.Description("Prefer results from this country (in English)")),
		langParam, tzParam, formatParam,
	), handleSearch)

	s.AddTool(tool("fetch", "Fetch a team, player, competition or match",
		"Full details for an id from search: team:<id>, player:<id>, league:<league key> or match:<id>. Returns the title, the full text, and the football-mania.com url.",
		mcp.WithString("id", mcp.Required(), mcp.Description("Typed id from search, e.g. team:9002, player:474972, league:EnglandPremierLeague, match:3868859")),
		langParam, tzParam,
	), handleFetch)
}

func handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	a := args(req.Params.Arguments)
	v, apiLang, err := newView(a)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	q := argStr(a, "query", argStr(a, "q", ""))
	if q == "" {
		return mcp.NewToolResultError("missing required argument \"query\""), nil
	}
	extra := url.Values{"q": {q}}
	if c := argStr(a, "country", ""); c != "" {
		extra.Set("country", c)
	}
	data, body, err := fetchJSON(ctx, "search_v3", apiLang, extra)
	if err != nil {
		return toolError(err, "Search"), nil
	}
	if argStr(a, "format", "text") == "json" {
		return mcp.NewToolResultText(compactJSON(body)), nil
	}

	d, _ := data.(map[string]any)
	results := []searchResult{}
	now := time.Now()
	for _, r := range list(d["result"]) {
		switch str(r, "type") {
		case "l":
			key := str(r, "keygs")
			results = append(results, searchResult{
				ID: "league:" + key, Title: fmt.Sprintf("%s (%s)", str(r, "name"), str(r, "country")),
				URL: leagueLink(v.lang, key, ""), Text: "Competition · league_key " + key,
			})
		case "t":
			id := str(r, "id_gs")
			text := fmt.Sprintf("Team · %s · team id %s", str(r, "country"), id)
			var last, next obj
			for _, m := range list(r["matches"]) {
				k, _ := kickoff(m)
				if isFinished(str(m, "status")) || (k.Before(now) && !isLive(str(m, "status"))) {
					last = m
				} else if next == nil {
					next = m
				}
			}
			if last != nil {
				text += " · last: " + v.matchLine(last, true)
			}
			if next != nil {
				text += " · next: " + v.matchLine(next, true)
			}
			results = append(results, searchResult{
				ID: "team:" + id, Title: fmt.Sprintf("%s (%s)", str(r, "name"), str(r, "country")),
				URL: teamLink(v.lang, id, str(r, "name")), Text: text,
			})
		case "p":
			id := str(r, "id_gs")
			results = append(results, searchResult{
				ID: "player:" + id, Title: fmt.Sprintf("%s (%s)", str(r, "name"), str(r, "team_name")),
				URL:  playerLink(v.lang, id, str(r, "name")),
				Text: fmt.Sprintf("Player · %s · %s, %s · player id %s", str(r, "country_player"), str(r, "team_name"), str(r, "league_name"), id),
			})
		}
	}
	return structured(map[string]any{"results": results}), nil
}

var firstLinkRe = regexp.MustCompile(`https://football-mania\.com/\S+`)

func handleFetch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	a := args(req.Params.Arguments)
	v, apiLang, err := newView(a)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	a["_view"] = v
	id, err := requireArg(a, "id")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	kind := "match"
	if m := typedIDRe.FindStringSubmatch(id); m != nil {
		kind = m[1]
	} else if !idRe.MatchString(id) {
		kind = "league"
	}
	raw := untyped(id)

	var data any
	var format func(a obj, v view, data any) string
	switch kind {
	case "team":
		data, _, err = fetchJSON(ctx, "team_gs/"+url.PathEscape(raw)+".json", apiLang, nil)
		format = formatTeam
	case "player":
		data, _, err = fetchJSON(ctx, "players/"+url.PathEscape(raw)+".json", apiLang, nil)
		format = formatPlayer
	case "league":
		data, _, err = withLeague(ctx, a, apiLang, raw, func(key string) (any, []byte, error) {
			return emptyIsNotFound(fetchJSON(ctx, "fixtures_v2/"+key+"_small.json", apiLang, nil))
		})
		format = formatLeagueWithTable
	default:
		data, _, err = fetchJSON(ctx, "matches/"+url.PathEscape(raw)+".json", apiLang, url.Values{"h2h": {"1"}})
		format = formatMatch
	}
	if err != nil {
		return toolError(err, strings.ToUpper(kind[:1])+kind[1:]), nil
	}
	text := format(a, v, data)
	title, _, _ := strings.Cut(text, "\n")
	return structured(fetchResult{
		ID: kind + ":" + raw, Title: title, Text: text,
		URL: firstLinkRe.FindString(text), Metadata: map[string]string{"type": kind, "source": "football-mania.com"},
	}), nil
}

// formatLeagueWithTable: fixtures plus the table the same feed carries
func formatLeagueWithTable(a obj, v view, data any) string {
	text := formatLeague(a, v, data)
	if d, _ := data.(map[string]any); len(list(sub(d, "table")["groups"])) > 0 {
		text += "\n" + formatStandings(a, v, sub(d, "table"))
	}
	return text
}
