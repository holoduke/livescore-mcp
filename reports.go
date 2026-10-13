package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Two feeds the server already had access to but never exposed.
//
//   - matchnews/<id>.json and matchpreview/<id>.json carry a written report or
//     preview, generated per match for selected competitions and translated into
//     nine languages. The language lives in the filename (<id>_nl.json), not in a
//     query parameter, which is why these do not go through the usual lang path.
//   - commentaries/<id>.json carries the minute-by-minute ticker, the goal and
//     card events, substitutions and the line-ups.
//
// Both are read-only like every other tool here.

// reportLangs are the languages the generator writes besides English. The
// codes are the app's own, not ISO, because they are the file suffix the
// generator produces: see TRANSLATE_LANGS in run_matchnews_generator.js.
var reportLangs = map[string]bool{
	"es": true, "de": true, "fr": true, "it": true, "pt": true,
	"nl": true, "ru": true, "ar": true, "tr": true,
	"br": true, "hk": true, "th": true, "pl": true,
	"id": true, "vi": true, "dk": true,
}

// reportLang gives the suffix to look for, or "" when only English exists.
// Brazilian Portuguese is the one case where it differs from the language the
// rest of the API is called with: the generator writes a separate br text, so
// a pt-BR reader gets the Brazilian wording while names still come back as pt.
func reportLang(site, api string) string {
	if site == "pt-BR" {
		return "br"
	}
	if reportLangs[api] {
		return api
	}
	return ""
}

// fetchLocalised tries <dir>/<id>_<lang>.json and falls back to the English
// <dir>/<id>.json. Returns the data and the language actually served.
func fetchLocalised(ctx context.Context, dir, id, lang string) (obj, string, error) {
	if lang != "" {
		if data, _, err := fetchJSON(ctx, fmt.Sprintf("%s/%s_%s.json", dir, id, lang), lang, nil); err == nil {
			if d, ok := data.(map[string]any); ok {
				return d, lang, nil
			}
		}
	}
	data, _, err := fetchJSON(ctx, fmt.Sprintf("%s/%s.json", dir, id), "en", nil)
	if err != nil {
		return nil, "", err
	}
	d, ok := data.(map[string]any)
	if !ok {
		return nil, "", errNotFound
	}
	return d, "en", nil
}

func registerReportTools(s *server.MCPServer) {
	s.AddTool(tool("get_match_report", "Match report or preview",
		"A written report of a finished match, or a preview of one still to come, with the story and the sources it drew on. "+
			"Available for selected competitions rather than every match; call get_match first if you are not sure the match has one. "+
			"Translated into es, de, fr, it, pt, nl, ru, ar and tr; other languages fall back to English.",
		mcp.WithString("match_id", mcp.Required(), mcp.Description("Match id from get_live_scores, get_day_fixtures or search")),
		mcp.WithString("kind", mcp.Enum("auto", "report", "preview"),
			mcp.Description("auto (default): report if one exists, otherwise preview. report: only a finished-match report. preview: only a pre-match preview")),
		langParam,
		mcp.WithOutputSchema[MatchReport](),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		a := args(req.Params.Arguments)
		v, _, err := newView(a)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		id, err := requireArg(a, "match_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		site, apiLang := language(a)
		wantLang := reportLang(site, apiLang)

		kind := argStr(a, "kind", "auto")
		order := []string{"matchnews", "matchpreview"}
		switch kind {
		case "report":
			order = []string{"matchnews"}
		case "preview":
			order = []string{"matchpreview"}
		}

		for _, dir := range order {
			d, servedLang, err := fetchLocalised(ctx, dir, id, wantLang)
			if err != nil {
				continue
			}
			field := "news"
			label := "report"
			if dir == "matchpreview" {
				field, label = "preview", "preview"
			}
			story := stripTags(str(sub(d, field), "story"))
			if story == "" {
				continue
			}
			out := MatchReport{
				MatchID:     id,
				Kind:        label,
				Home:        str(d, "localTeam"),
				Away:        str(d, "visitorTeam"),
				Score:       str(d, "score"),
				Competition: str(d, "league"),
				LeagueKey:   str(d, "leagueKey"),
				Date:        str(d, "date"),
				Language:    servedLang,
				Text:        story,
				GeneratedAt: str(d, "generatedAt"),
				URL:         link(v.lang, slugPath("match", id, str(d, "localTeam")+"-"+str(d, "visitorTeam"))),
			}
			if raw, ok := d["sources"].([]any); ok {
				for _, s := range raw {
					if u, ok := s.(string); ok {
						out.Sources = append(out.Sources, u)
					}
				}
			}

			var b strings.Builder
			head := out.Home + " – " + out.Away
			if out.Score != "" {
				head = out.Home + " " + out.Score + " " + out.Away
			}
			fmt.Fprintf(&b, "%s (%s)\n", head, label)
			if out.Competition != "" {
				fmt.Fprintf(&b, "%s", out.Competition)
				if out.Date != "" {
					fmt.Fprintf(&b, " · %s", out.Date)
				}
				b.WriteString("\n")
			}
			if pred := str(d, "prediction"); pred != "" {
				fmt.Fprintf(&b, "Predicted score: %s\n", pred)
			}
			fmt.Fprintf(&b, "\n%s\n", story)
			if out.URL != "" {
				fmt.Fprintf(&b, "\n%s\n", out.URL)
			}
			if len(out.Sources) > 0 {
				fmt.Fprintf(&b, "\nSources: %s\n", strings.Join(out.Sources, ", "))
			}
			if wantLang != "" && servedLang != wantLang {
				b.WriteString("\n(no translation for this match yet; returned in English)\n")
			}
			return mcp.NewToolResultStructured(out, b.String()), nil
		}
		return mcp.NewToolResultError(
			"No report or preview exists for match " + id +
				". These are written for selected competitions only; use get_match for the events, line-ups and statistics."), nil
	})

	s.AddTool(tool("get_match_commentary", "Live commentary and events",
		"Minute-by-minute written commentary for a match, plus the goals, cards and substitutions in order. "+
			"Useful while a match is in play, and afterwards to see how it unfolded.",
		mcp.WithString("match_id", mcp.Required(), mcp.Description("Match id from get_live_scores, get_day_fixtures or search")),
		mcp.WithNumber("limit", mcp.Description("Maximum ticker entries, most recent last. Default: 60, maximum 300")),
		mcp.WithBoolean("events_only", mcp.Description("true: only goals, cards and substitutions, skipping the running commentary. Default: false")),
		langParam, formatParam,
		mcp.WithOutputSchema[Commentary](),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		a := args(req.Params.Arguments)
		_, apiLang, err := newView(a)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		id, err := requireArg(a, "match_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		data, body, err := fetchJSON(ctx, "commentaries/"+id+".json", apiLang, nil)
		if err != nil {
			return toolError(err, "Commentary"), nil
		}
		if argStr(a, "format", "text") == "json" {
			return mcp.NewToolResultText(compactJSON(body)), nil
		}
		d, _ := data.(map[string]any)

		limit := argInt(a, "limit", 60)
		if limit < 1 || limit > 300 {
			limit = 300
		}

		out := Commentary{MatchID: id, LastUpdated: str(d, "lastUpdated")}

		for _, e := range list(d["events"]) {
			side := "home"
			if str(e, "team") == "visitorteam" {
				side = "away"
			}
			out.Events = append(out.Events, CommentaryEvent{
				Minute: str(e, "minute"),
				Type:   str(e, "type"),
				Team:   side,
				Player: str(e, "player"),
				Text:   strings.TrimSpace(str(e, "assist")),
			})
		}
		for _, sb := range list(d["substitutions"]) {
			side := "home"
			if str(sb, "team") == "visitorteam" {
				side = "away"
			}
			out.Events = append(out.Events, CommentaryEvent{
				Minute: str(sb, "minute"),
				Type:   "substitution",
				Team:   side,
				Player: str(sb, "player_in"),
				Text:   "for " + str(sb, "player_out"),
			})
		}
		sort.SliceStable(out.Events, func(i, j int) bool {
			return minuteValue(out.Events[i].Minute) < minuteValue(out.Events[j].Minute)
		})

		if !argBool(a, "events_only", false) {
			// The feed is already in reading order, oldest first.
			for _, t := range list(d["liveticker"]) {
				out.Ticker = append(out.Ticker, CommentaryEvent{
					Minute: str(t, "minute"),
					Text:   stripTags(str(t, "comment")),
				})
			}
			if len(out.Ticker) > limit {
				out.Ticker = out.Ticker[len(out.Ticker)-limit:]
			}
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Match %s commentary", id)
		if out.LastUpdated != "" {
			fmt.Fprintf(&b, " (updated %s)", out.LastUpdated)
		}
		b.WriteString("\n")
		if len(out.Events) == 0 && len(out.Ticker) == 0 {
			b.WriteString("\nNo commentary for this match. Not every competition carries it; get_match has the score, line-ups and statistics.\n")
			return mcp.NewToolResultStructured(out, b.String()), nil
		}
		if len(out.Events) > 0 {
			b.WriteString("\nEvents\n")
			for _, e := range out.Events {
				fmt.Fprintf(&b, "- %s' %s %s (%s)", e.Minute, e.Type, e.Player, e.Team)
				if e.Text != "" {
					fmt.Fprintf(&b, " %s", e.Text)
				}
				b.WriteString("\n")
			}
		}
		if len(out.Ticker) > 0 {
			fmt.Fprintf(&b, "\nCommentary (%d entries)\n", len(out.Ticker))
			for _, t := range out.Ticker {
				min := t.Minute
				if min == "" || min == "-" {
					min = " "
				}
				fmt.Fprintf(&b, "%4s  %s\n", min, t.Text)
			}
		}
		return mcp.NewToolResultStructured(out, b.String()), nil
	})
}

// minuteValue turns "45+2" or "90" into something sortable; non-numeric
// minutes (the "-" the ticker uses for kick-off) sort first.
func minuteValue(s string) int {
	s = strings.TrimSpace(strings.TrimSuffix(s, "'"))
	if s == "" || s == "-" {
		return -1
	}
	base := s
	extra := 0
	if i := strings.Index(s, "+"); i >= 0 {
		base = s[:i]
		extra, _ = strconv.Atoi(s[i+1:])
	}
	n, err := strconv.Atoi(base)
	if err != nil {
		return -1
	}
	return n*100 + extra
}
