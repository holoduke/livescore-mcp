package main

// MCP tools. Each answers in compact text a model can read in one go, with
// IDs for follow-up calls and a football-mania.com link per match, team,
// player and competition; format=json returns the raw API answer instead.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const serverInstructions = `Football (soccer) data from football-mania.com: live scores, fixtures and results, standings, top scorers, match details, teams and players for 1000+ competitions.

Workflow: use search to turn a team, player or competition name into its ID or league key, then call the specific tool. get_day_fixtures covers any date; get_live_scores only matches in play right now.
All dates and kick-off times are UTC; convert to the user's time zone.
Every answer contains football-mania.com links: cite them when you present the data, so users can open the full match, team or league page.`

// Competitions listed first in day and live overviews; the feed itself is
// alphabetical by country
var priorityLeagues = []string{
	"EurocupsUEFAChampionsLeague", "EurocupsUEFAEuropaLeague", "EurocupsUEFAEuropaConferenceLeague",
	"WorldCupWorldCup", "EurocupsEuropeanChampionship", "EurocupsUEFANationsLeagueLeagueA",
	"EnglandPremierLeague", "SpainPrimeraDivision", "ItalySerieA", "GermanyBundesliga", "FranceLigue1",
	"NetherlandsEredivisie", "PortugalPrimeiraLiga", "BrazilSerieA", "ArgentinaLigaProfesional",
	"USAMLS", "SaudiArabiaProLeague", "TurkeySuperLig", "BelgiumProLeague", "ScotlandPremierLeague",
	"EnglandChampionship", "MexicoLigaMX",
}

func leagueRank(key string) int {
	for i, k := range priorityLeagues {
		if k == key {
			return i
		}
	}
	for _, p := range []string{"WorldCup", "Eurocups", "International"} {
		if strings.HasPrefix(key, p) {
			return 100
		}
	}
	return 200
}

func toolError(err error, what string) *mcp.CallToolResult {
	if errors.Is(err, errNotFound) {
		return mcp.NewToolResultError(fmt.Sprintf("%s not found. Use the search tool to find the right ID or league key.", what))
	}
	return mcp.NewToolResultError(err.Error())
}

// Shared parameters
var (
	langParam   = mcp.WithString("language", mcp.Description("Language for team, competition and country names, and for the football-mania.com links (en, es, de, fr, it, pt, pt-BR, nl, ar, ru, tr, id, …). Default: en"))
	formatParam = mcp.WithString("format", mcp.Enum("text", "json"), mcp.Description("text (default): compact summary with IDs and links. json: the raw data feed, minified and capped at 60,000 characters"))
)

func readOnly(title string) []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithTitleAnnotation(title),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(true),
	}
}

func tool(name, title, description string, opts ...mcp.ToolOption) mcp.Tool {
	all := append([]mcp.ToolOption{mcp.WithDescription(description)}, readOnly(title)...)
	return mcp.NewTool(name, append(all, opts...)...)
}

// run wraps a text formatter: argument parsing, raw JSON mode and errors
func run(what string, fetch func(ctx context.Context, a obj, apiLang string) (any, []byte, error), format func(a obj, lang string, data any) string) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		a := args(req.Params.Arguments)
		lang, apiLang := language(a)
		data, body, err := fetch(ctx, a, apiLang)
		if err != nil {
			return toolError(err, what), nil
		}
		if argStr(a, "format", "text") == "json" {
			return mcp.NewToolResultText(compactJSON(body)), nil
		}
		return mcp.NewToolResultText(format(a, lang, data)), nil
	}
}

// emptyIsNotFound: the API answers an unknown league key with a skeleton
// whose names are "empty"
func emptyIsNotFound(data any, body []byte, err error) (any, []byte, error) {
	if err == nil {
		d, _ := data.(map[string]any)
		if str(d, "filegroup") == "empty" || str(d, "country") == "empty" || (d["info"] == nil && str(sub(d, "table"), "filegroup") == "empty") {
			return nil, nil, errNotFound
		}
	}
	return data, body, err
}

func requireArg(a obj, key string) (string, error) {
	v := argStr(a, key, "")
	if v == "" {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	return v, nil
}

func registerTools(s *server.MCPServer) {
	s.AddTool(tool("get_live_scores", "Live football scores",
		"Football matches being played right now, worldwide, with score and minute, grouped by competition (biggest competitions first). Optional filters by country or league key.",
		mcp.WithString("country", mcp.Description("Only this country, in English (e.g. England, Spain, Brazil); international club and national-team competitions use Eurocups, International or WorldCup")),
		mcp.WithString("league_key", mcp.Description("Only this competition (league key from search, e.g. EnglandPremierLeague)")),
		langParam, formatParam,
	), run("Live scores", func(ctx context.Context, a obj, apiLang string) (any, []byte, error) {
		return fetchJSON(ctx, "fixtures/feed_livenow.json", apiLang, nil)
	}, func(a obj, lang string, data any) string {
		return formatDayFeed(a, lang, data, "Live now", 200)
	}))

	s.AddTool(tool("get_day_fixtures", "Matches on a date",
		"All football matches on a date: results, live scores and kick-off times, grouped by competition (biggest competitions first). A busy day has 1000+ matches, so filter by country or league_key when you can.",
		mcp.WithString("date", mcp.Description("YYYY-MM-DD, DD/MM/YYYY, today, tomorrow or yesterday. Default: today (UTC)")),
		mcp.WithNumber("tzoffset", mcp.Description("Day boundaries in this offset from UTC, in minutes (e.g. 120 for UTC+2). Default: 0")),
		mcp.WithString("country", mcp.Description("Only this country, in English (e.g. England, Spain); Eurocups, International or WorldCup for international competitions")),
		mcp.WithString("league_key", mcp.Description("Only this competition (league key from search)")),
		mcp.WithNumber("limit", mcp.Description("Maximum matches listed. Default: 150, maximum 500")),
		langParam, formatParam,
	), run("Fixtures", func(ctx context.Context, a obj, apiLang string) (any, []byte, error) {
		date, err := parseDate(argStr(a, "date", ""), time.Now().UTC())
		if err != nil {
			return nil, nil, err
		}
		a["_date"] = date
		return fetchJSON(ctx, "fixtures/feed_matches_aggregated.json", apiLang, url.Values{
			"date": {date}, "tzoffset": {fmt.Sprint(argInt(a, "tzoffset", 0))},
		})
	}, func(a obj, lang string, data any) string {
		limit := argInt(a, "limit", 150)
		if limit < 1 || limit > 500 {
			limit = 500
		}
		return formatDayFeed(a, lang, data, "Matches on "+str(a, "_date"), limit)
	}))

	leagueFixtures := run("Competition", func(ctx context.Context, a obj, apiLang string) (any, []byte, error) {
		key := strings.TrimSuffix(argStr(a, "league_key", argStr(a, "competition", "")), "_small")
		if key == "" {
			return nil, nil, fmt.Errorf("missing required argument \"league_key\"")
		}
		a["_key"] = key
		return emptyIsNotFound(fetchJSON(ctx, "fixtures_v2/"+key+"_small.json", apiLang, nil))
	}, formatLeague)
	s.AddTool(tool("get_league_fixtures", "Competition fixtures and results",
		"Fixtures and results of one competition (league or cup): latest results, matches in play and upcoming matches of the current season, or one round. For the table use get_standings.",
		mcp.WithString("league_key", mcp.Required(), mcp.Description("League key from search (e.g. EnglandPremierLeague, SpainPrimeraDivision, EurocupsUEFAChampionsLeague)")),
		mcp.WithString("round", mcp.Description("Only this round/matchday number (e.g. 7)")),
		mcp.WithNumber("limit", mcp.Description("Results and upcoming matches listed, each. Default: 10")),
		langParam, formatParam,
	), leagueFixtures)
	// Kept for clients built against 1.0
	s.AddTool(tool("get_fixtures", "Competition fixtures (alias)",
		"Same as get_league_fixtures; accepts the competition key with or without the _small suffix.",
		mcp.WithString("competition", mcp.Required(), mcp.Description("League key (e.g. EurocupsUEFAChampionsLeague)")),
		mcp.WithString("round", mcp.Description("Only this round/matchday number")),
		mcp.WithNumber("limit", mcp.Description("Results and upcoming matches listed, each. Default: 10")),
		langParam, formatParam,
	), leagueFixtures)

	s.AddTool(tool("get_standings", "League table",
		"Current standings (league table) of a competition: position, played, won, drawn, lost, goals, points, form and the qualification or relegation zone. Group-stage competitions return every group.",
		mcp.WithString("league_key", mcp.Required(), mcp.Description("League key from search (e.g. EnglandPremierLeague)")),
		langParam, formatParam,
	), run("Standings", func(ctx context.Context, a obj, apiLang string) (any, []byte, error) {
		key, err := requireArg(a, "league_key")
		if err != nil {
			return nil, nil, err
		}
		a["_key"] = key
		return emptyIsNotFound(fetchJSON(ctx, "tables/"+key+".json", apiLang, nil))
	}, formatStandings))

	s.AddTool(tool("get_top_scorers", "Top scorers",
		"Top scorers of a competition this season: goals, penalty goals, team and nationality.",
		mcp.WithString("league_key", mcp.Required(), mcp.Description("League key from search (e.g. SpainPrimeraDivision)")),
		mcp.WithNumber("limit", mcp.Description("Players listed. Default: 20")),
		langParam, formatParam,
	), run("Top scorers", func(ctx context.Context, a obj, apiLang string) (any, []byte, error) {
		key, err := requireArg(a, "league_key")
		if err != nil {
			return nil, nil, err
		}
		a["_key"] = key
		return emptyIsNotFound(fetchJSON(ctx, "topscorers/"+key+".json", apiLang, nil))
	}, formatTopScorers))

	s.AddTool(tool("get_match", "Match details",
		"One match in detail: score, status, goals, cards and substitutions with minutes, line-ups, team statistics (possession, shots, corners…), injuries, form, standings of both teams, match report and head-to-head record.",
		mcp.WithString("id", mcp.Required(), mcp.Description("Match ID (from fixtures, live scores or search)")),
		mcp.WithBoolean("h2h", mcp.Description("Include the head-to-head record. Default: true")),
		langParam, formatParam,
	), run("Match", func(ctx context.Context, a obj, apiLang string) (any, []byte, error) {
		id, err := requireArg(a, "id")
		if err != nil {
			return nil, nil, err
		}
		h2h := "1"
		if !argBool(a, "h2h", true) {
			h2h = "0"
		}
		return fetchJSON(ctx, "matches/"+url.PathEscape(id)+".json", apiLang, url.Values{"h2h": {h2h}})
	}, formatMatch))

	s.AddTool(tool("get_team", "Team profile",
		"A team: recent results and upcoming fixtures, league positions, season statistics, squad with appearances and goals, injuries, transfers, coach, stadium and honours.",
		mcp.WithString("id", mcp.Required(), mcp.Description("Team ID from search (e.g. 9002 Arsenal, 13183 Ajax)")),
		mcp.WithBoolean("squad", mcp.Description("Include the full squad. Default: true")),
		langParam, formatParam,
	), run("Team", func(ctx context.Context, a obj, apiLang string) (any, []byte, error) {
		id, err := requireArg(a, "id")
		if err != nil {
			return nil, nil, err
		}
		return fetchJSON(ctx, "team_gs/"+url.PathEscape(id)+".json", apiLang, nil)
	}, formatTeam))

	s.AddTool(tool("get_player", "Player profile",
		"A player: club, position, age, nationality, market value, statistics per season and competition, injuries, transfers and honours.",
		mcp.WithString("id", mcp.Required(), mcp.Description("Player ID from search or a team squad (e.g. 474972)")),
		langParam, formatParam,
	), run("Player", func(ctx context.Context, a obj, apiLang string) (any, []byte, error) {
		id, err := requireArg(a, "id")
		if err != nil {
			return nil, nil, err
		}
		return fetchJSON(ctx, "players/"+url.PathEscape(id)+".json", apiLang, nil)
	}, formatPlayer))

	s.AddTool(tool("search", "Search teams, players, competitions",
		"Find teams, players and competitions by name and get the IDs and league keys the other tools need. Teams come with their last and next match.",
		mcp.WithString("q", mcp.Required(), mcp.Description("Name or part of it (e.g. Arsenal, Mbappé, Premier League)")),
		mcp.WithString("country", mcp.Description("Prefer results from this country (in English)")),
		langParam, formatParam,
	), run("Search", func(ctx context.Context, a obj, apiLang string) (any, []byte, error) {
		q, err := requireArg(a, "q")
		if err != nil {
			return nil, nil, err
		}
		extra := url.Values{"q": {q}}
		if c := argStr(a, "country", ""); c != "" {
			extra.Set("country", c)
		}
		return fetchJSON(ctx, "search_v3", apiLang, extra)
	}, formatSearch))

	s.AddTool(tool("get_team_image", "Team logo",
		"URL of a team's logo (PNG).",
		mcp.WithString("id", mcp.Required(), mcp.Description("Team ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := argStr(args(req.Params.Arguments), "id", "")
		if id == "" {
			return mcp.NewToolResultError("missing required argument \"id\""), nil
		}
		u, _ := url.Parse(baseURL)
		u.Path, _ = url.JoinPath(u.Path, "images", "teams_gs", id+".png")
		r, _ := httpNewHead(ctx, u.String())
		resp, err := httpClient.Do(r)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("checking logo: %v", err)), nil
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return mcp.NewToolResultError(fmt.Sprintf("no logo for team %s", id)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Logo of team %s: %s", id, u.String())), nil
	})

	s.AddTool(tool("health", "Connectivity check",
		"Connectivity check: echoes the message back.",
		mcp.WithString("message", mcp.Description("Text to echo")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok: " + argStr(args(req.Params.Arguments), "message", "pong")), nil
	})
}

// --- Day and live feeds: [{country, leagues: [{league, key, matches}]}] ---

func formatDayFeed(a obj, lang string, data any, heading string, limit int) string {
	country := strings.ToLower(argStr(a, "country", ""))
	key := strings.TrimSuffix(argStr(a, "league_key", ""), "_small")

	type league struct {
		name, country, key string
		matches            []obj
	}
	var leagues []league
	total := 0
	for _, c := range list(data) {
		cn := str(c, "country")
		if country != "" && strings.ToLower(cn) != country {
			continue
		}
		for _, l := range list(c["leagues"]) {
			if key != "" && str(l, "key") != key {
				continue
			}
			ms := list(l["matches"])
			if len(ms) == 0 {
				continue
			}
			leagues = append(leagues, league{str(l, "league"), cn, str(l, "key"), ms})
			total += len(ms)
		}
	}
	sort.SliceStable(leagues, func(i, j int) bool { return leagueRank(leagues[i].key) < leagueRank(leagues[j].key) })

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d matches in %d competitions (times UTC)\n", heading, total, len(leagues))
	if total == 0 {
		b.WriteString("No matches")
		if country != "" || key != "" {
			b.WriteString(" for this filter. Country names are in English; check the league key with search")
		}
		b.WriteString(".\n")
		return b.String()
	}
	shown := 0
	for _, l := range leagues {
		if shown >= limit {
			break
		}
		fmt.Fprintf(&b, "\n%s (%s) [%s] %s\n", l.name, l.country, l.key, leagueLink(lang, l.key, ""))
		for _, m := range l.matches {
			if shown >= limit {
				break
			}
			b.WriteString("- " + matchLine(lang, m, false) + "\n")
			shown++
		}
	}
	if shown < total {
		fmt.Fprintf(&b, "\n…%d more matches not listed. Narrow down with country or league_key, or raise limit.\n", total-shown)
	}
	return b.String()
}

// --- Competition: info, fixtures (whole season), table, topscorers ---

func formatLeague(a obj, lang string, data any) string {
	d, _ := data.(map[string]any)
	info := sub(d, "info")
	key := str(a, "_key")
	fixtures := list(d["fixtures"])
	limit := argInt(a, "limit", 10)
	if limit < 1 {
		limit = 10
	}

	var b strings.Builder
	title := str(info, "title")
	if title == "" {
		title = key
	}
	fmt.Fprintf(&b, "%s (%s) [%s]", title, str(info, "countrygs"), key)
	if season := str(sub(d, "table"), "season"); season != "" {
		fmt.Fprintf(&b, " · season %s", season)
	}
	if w := str(info, "activeWeek"); w != "" {
		fmt.Fprintf(&b, " · current round %s", w)
	}
	fmt.Fprintf(&b, "\n%s\nAll times UTC.\n", leagueLink(lang, key, ""))
	if len(fixtures) == 0 {
		b.WriteString("\nNo fixtures available for this competition.\n")
		return b.String()
	}

	if round := argStr(a, "round", ""); round != "" {
		fmt.Fprintf(&b, "\nRound %s:\n", round)
		n := 0
		for _, m := range fixtures {
			if str(m, "week") == round {
				b.WriteString("- " + matchLine(lang, m, true) + "\n")
				n++
			}
		}
		if n == 0 {
			b.WriteString("No matches in this round.\n")
		}
		return b.String()
	}

	sort.SliceStable(fixtures, func(i, j int) bool {
		ti, _ := kickoff(fixtures[i])
		tj, _ := kickoff(fixtures[j])
		return ti.Before(tj)
	})
	now := time.Now().UTC()
	var past, live, next []obj
	for _, m := range fixtures {
		st := str(m, "status")
		t, _ := kickoff(m)
		switch {
		case isLive(st):
			live = append(live, m)
		case isFinished(st) || (isOff(st) && t.Before(now)):
			past = append(past, m)
		case t.After(now.Add(-3 * time.Hour)):
			next = append(next, m)
		}
	}
	if len(live) > 0 {
		b.WriteString("\nLive now:\n")
		for _, m := range live {
			b.WriteString("- " + matchLine(lang, m, false) + "\n")
		}
	}
	if len(past) > 0 {
		if len(past) > limit {
			past = past[len(past)-limit:]
		}
		b.WriteString("\nLatest results:\n")
		for _, m := range past {
			b.WriteString("- " + roundPrefix(m) + matchLine(lang, m, true) + "\n")
		}
	}
	if len(next) > 0 {
		if len(next) > limit {
			next = next[:limit]
		}
		b.WriteString("\nUpcoming:\n")
		for _, m := range next {
			b.WriteString("- " + roundPrefix(m) + matchLine(lang, m, true) + "\n")
		}
	}
	if str(info, "hasTables") == "1" {
		fmt.Fprintf(&b, "\nStandings: get_standings league_key=%s · %s\n", key, leagueLink(lang, key, "table"))
	}
	return b.String()
}

func roundPrefix(m obj) string {
	if w := str(m, "week"); w != "" {
		return "R" + w + " · "
	}
	return ""
}

// --- Standings: {season, groups: [{group, teams: [...]}]} ---

func formatStandings(a obj, lang string, data any) string {
	d, _ := data.(map[string]any)
	key := str(a, "_key")
	groups := list(d["groups"])
	var b strings.Builder
	fmt.Fprintf(&b, "%s standings (%s) [%s]", str(d, "leaguename"), str(d, "filegroup"), key)
	if s := str(d, "season"); s != "" {
		fmt.Fprintf(&b, " · season %s", s)
	}
	fmt.Fprintf(&b, "\n%s\n", leagueLink(lang, key, "table"))
	if len(groups) == 0 || str(d, "filegroup") == "empty" {
		b.WriteString("\nNo table for this competition (cups and knockout rounds have none).\n")
		return b.String()
	}
	for _, g := range groups {
		if len(groups) > 1 || str(g, "group") != str(d, "leaguename") {
			fmt.Fprintf(&b, "\n%s\n", str(g, "group"))
		}
		b.WriteString("Pos | Team | P | W | D | L | Goals | GD | Pts | Form | Zone\n")
		for _, t := range list(g["teams"]) {
			played := num(t, "totalWon") + num(t, "totalDraw") + num(t, "totalLost")
			zone := str(t, "description_v2")
			if zone == "" {
				zone = str(t, "description")
			}
			live := ""
			if str(t, "isLive") == "1" {
				live = " (live)"
			}
			fmt.Fprintf(&b, "%s | %s (team %s)%s | %d | %s | %s | %s | %s:%s | %s | %s | %s | %s\n",
				str(t, "position"), str(t, "team"), str(t, "id_gs"), live, played,
				str(t, "totalWon"), str(t, "totalDraw"), str(t, "totalLost"),
				str(t, "totalGoalsFor"), str(t, "totalGoalsAgainst"), str(t, "goalDifference"),
				str(t, "points"), str(t, "recentForm"), zone)
		}
	}
	b.WriteString("\nForm: most recent result last. (live) = points include a match in play.\n")
	return b.String()
}

// --- Top scorers: {league, tournaments: [{players: [...]}]} ---

func formatTopScorers(a obj, lang string, data any) string {
	d, _ := data.(map[string]any)
	key := str(a, "_key")
	limit := argInt(a, "limit", 20)
	var b strings.Builder
	fmt.Fprintf(&b, "%s top scorers (%s) [%s]\n%s\n", str(d, "league"), str(d, "country"), key, leagueLink(lang, key, "top-scorers"))
	n := 0
	for _, t := range list(d["tournaments"]) {
		players := list(t["players"])
		if len(list(d["tournaments"])) > 1 {
			fmt.Fprintf(&b, "\n%s\n", str(t, "tournament"))
		}
		for i, p := range players {
			if i >= limit {
				break
			}
			pens := ""
			if pg := num(p, "penalty_goals"); pg > 0 {
				pens = fmt.Sprintf(" (%d pen.)", pg)
			}
			fmt.Fprintf(&b, "%s. %s (%s) · %s · %s goals%s · player %s\n", str(p, "pos"), str(p, "name"), str(p, "nationality"), str(p, "team"), str(p, "goals"), pens, str(p, "id"))
			n++
		}
	}
	if n == 0 {
		b.WriteString("\nNo scorers recorded yet.\n")
	}
	return b.String()
}

// --- Match ---

func formatMatch(a obj, lang string, data any) string {
	m, _ := data.(map[string]any)
	if m == nil || str(m, "id") == "" {
		return "Match not found. Use get_day_fixtures, get_league_fixtures or search to find the match ID."
	}
	home, away := str(m, "localteam"), str(m, "visitorteam")
	var b strings.Builder
	sc := score(m)
	if sc == "" {
		sc = "vs"
	}
	fmt.Fprintf(&b, "%s %s %s · %s\n", home, sc, away, statusLabel(m))
	fmt.Fprintf(&b, "%s · %s %s UTC", str(m, "leaguename"), str(m, "date"), str(m, "time"))
	if w := str(m, "week"); w != "" {
		fmt.Fprintf(&b, " · round %s", w)
	}
	if v := str(m, "venue"); v != "" {
		fmt.Fprintf(&b, " · %s", v)
	}
	if r := str(m, "referee"); r != "" {
		fmt.Fprintf(&b, " · referee %s", r)
	}
	fmt.Fprintf(&b, "\nCompetition key %s · home team %s · away team %s\n%s\n", str(m, "leagueKey"), str(m, "gs_localteamid"), str(m, "gs_visitorteamid"), matchLink(lang, m))
	if ht := score(obj{"scoretime": str(m, "ht")}); ht != "" {
		fmt.Fprintf(&b, "Half-time: %s\n", ht)
	}
	if c1, c2 := str(m, "coachLocal"), str(m, "coachVisitor"); c1 != "" || c2 != "" {
		fmt.Fprintf(&b, "Coaches: %s / %s\n", c1, c2)
	}

	side := func(team string) string {
		if team == "localteam" {
			return home
		}
		return away
	}
	if evs := list(m["events"]); len(evs) > 0 {
		b.WriteString("\nEvents:\n")
		for _, e := range evs {
			min := str(e, "minute")
			if x := str(e, "extra_min"); x != "" && x != "0" {
				min += "+" + x
			}
			line := fmt.Sprintf("%s' %s %s (%s)", min, eventName(str(e, "type")), str(e, "player"), side(str(e, "team")))
			if r := str(e, "result"); r != "" {
				line += " " + r
			}
			if as := str(e, "assist"); as != "" {
				line += ", assist " + as
			}
			b.WriteString("- " + line + "\n")
		}
	}
	comm := sub(m, "commentaries")
	if subs := sub(comm, "substitutions"); len(subs) > 0 {
		type change struct {
			min  int
			line string
		}
		var changes []change
		for _, t := range []string{"localteam", "visitorteam"} {
			for _, s := range list(subs[t]) {
				changes = append(changes, change{num(s, "minute"), fmt.Sprintf("- %s' %s on for %s (%s)", str(s, "minute"), str(s, "on"), str(s, "off"), side(t))})
			}
		}
		sort.SliceStable(changes, func(i, j int) bool { return changes[i].min < changes[j].min })
		var lines []string
		for _, c := range changes {
			lines = append(lines, c.line)
		}
		if len(lines) > 0 {
			b.WriteString("\nSubstitutions:\n" + strings.Join(lines, "\n") + "\n")
		}
	}
	if lu := sub(m, "lineups"); len(list(lu["localteam"])) > 0 {
		b.WriteString("\nLine-ups:\n")
		for _, t := range []string{"localteam", "visitorteam"} {
			var names []string
			for _, p := range list(lu[t]) {
				names = append(names, fmt.Sprintf("%s %s", str(p, "number"), str(p, "name")))
			}
			fmt.Fprintf(&b, "%s: %s\n", side(t), strings.Join(names, ", "))
		}
	}
	if st := sub(comm, "stats"); len(sub(st, "localteam")) > 0 {
		l, v := sub(st, "localteam"), sub(st, "visitorteam")
		b.WriteString("\nStatistics (home - away):\n")
		for _, f := range [][2]string{
			{"possestiontime", "Possession"}, {"shotstotal", "Shots"}, {"shotsgoal", "Shots on target"},
			{"corners", "Corners"}, {"fouls", "Fouls"}, {"offsides", "Offsides"}, {"yellowcards", "Yellow cards"},
			{"redcards", "Red cards"}, {"saves", "Saves"}, {"passestotal", "Passes"}, {"passesaccurate", "Accurate passes"},
		} {
			if str(l, f[0]) != "" || str(v, f[0]) != "" {
				fmt.Fprintf(&b, "- %s: %s - %s\n", f[1], orZero(str(l, f[0])), orZero(str(v, f[0])))
			}
		}
	}
	if inj := sub(m, "injuries"); len(list(inj["ltsl"]))+len(list(inj["vtsl"])) > 0 {
		b.WriteString("\nUnavailable players:\n")
		for _, t := range [][2]string{{"ltsl", home}, {"vtsl", away}} {
			var names []string
			for _, p := range list(inj[t[0]]) {
				names = append(names, fmt.Sprintf("%s (%s)", str(p, "name"), str(p, "status")))
			}
			if len(names) > 0 {
				fmt.Fprintf(&b, "%s: %s\n", t[1], strings.Join(names, ", "))
			}
		}
	}
	if f1, f2 := str(m, "localteamshape"), str(m, "visitorteamshape"); f1 != "" || f2 != "" {
		fmt.Fprintf(&b, "\nForm (last 5, most recent last): %s %s · %s %s\n", home, f1, away, f2)
	}
	for _, s := range []struct{ key, team string }{{"localteamStanding", home}, {"visitorteamStanding", away}} {
		if t := sub(m, s.key); str(t, "position") != "" {
			fmt.Fprintf(&b, "Table: %s %s. with %s pts (%s-%s-%s, goals %s:%s)\n", s.team, str(t, "position"), str(t, "points"),
				str(t, "totalWon"), str(t, "totalDraw"), str(t, "totalLost"), str(t, "totalGoalsFor"), str(t, "totalGoalsAgainst"))
		}
	}
	if story := str(sub(m, "matchnews"), "story"); story != "" {
		fmt.Fprintf(&b, "\nMatch report: %s\n", truncate(stripTags(story), 1200))
	}
	if h := sub(m, "stats"); len(h) > 0 && argBool(a, "h2h", true) {
		fmt.Fprintf(&b, "\nHead-to-head (all meetings): %s %s wins, %s draws, %s %s wins; goals %s-%s\n",
			home, str(h, "total_localteam_won"), str(h, "total_draws"), away, str(h, "total_visitorteam_won"),
			str(h, "total_localteam_scored"), str(h, "total_visitorteam_scored"))
		h2h := list(m["h2hMatches"])
		if len(h2h) > 0 {
			b.WriteString("Recent meetings:\n")
			for i, x := range h2h {
				if i >= 6 {
					break
				}
				line := strings.Join(strings.Fields(fmt.Sprintf("%s %s %s %s %s", str(x, "date"), str(x, "localteam"), score(x), str(x, "visitorteam"), penalties(x))), " ")
				fmt.Fprintf(&b, "- %s (%s)\n", line, str(x, "leaguename"))
			}
		}
	}
	return b.String()
}

func penalties(m obj) string {
	if p := str(m, "penaltyLocalTeam"); p != "" {
		return fmt.Sprintf("(pens %s-%s)", p, str(m, "penaltyVisitorTeam"))
	}
	return ""
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func eventName(t string) string {
	switch t {
	case "goal":
		return "Goal"
	case "yellowcard":
		return "Yellow card"
	case "redcard":
		return "Red card"
	case "yellowred":
		return "Second yellow (red)"
	case "subst":
		return "Substitution"
	case "var":
		return "VAR"
	}
	return t
}

// --- Team ---

var positionNames = map[string]string{"G": "Goalkeepers", "D": "Defenders", "M": "Midfielders", "A": "Forwards", "F": "Forwards"}

func formatTeam(a obj, lang string, data any) string {
	t, _ := data.(map[string]any)
	if t == nil || str(t, "teamname") == "" {
		return "Team not found. Use search to find the team ID."
	}
	id, name := str(t, "id_gs"), str(t, "teamname")
	var b strings.Builder
	fmt.Fprintf(&b, "%s (team %s) · %s\n%s\n", name, id, str(t, "country"), teamLink(lang, id, name))
	var facts []string
	if f := str(t, "founded"); f != "" {
		facts = append(facts, "founded "+f)
	}
	if v := str(t, "venue"); v != "" {
		s := "stadium " + v
		if c := str(t, "venuecapacity"); c != "" {
			s += " (" + c + ")"
		}
		if c := str(t, "venuecity"); c != "" {
			s += ", " + c
		}
		facts = append(facts, s)
	}
	if c := str(t, "coach"); c != "" {
		facts = append(facts, "coach "+c)
	}
	if len(facts) > 0 {
		b.WriteString(strings.Join(facts, " · ") + "\n")
	}
	if about := str(t, "about"); about != "" {
		b.WriteString("\n" + truncate(about, 600) + "\n")
	}

	if ls := list(t["leagues"]); len(ls) > 0 {
		b.WriteString("\nCompetitions this season:\n")
		for _, l := range ls {
			pos := ""
			if p := str(l, "position"); p != "" && p != "0" {
				pos = " · position " + p
			}
			fmt.Fprintf(&b, "- %s (%s) [%s]%s\n", str(l, "league"), str(l, "country"), str(l, "key"), pos)
		}
	}
	if st := sub(t, "stats"); str(st, "winTotal") != "" {
		fmt.Fprintf(&b, "\nLeague season: won %s, drawn %s, lost %s · goals %s:%s · clean sheets %s · failed to score %s\n",
			str(st, "winTotal"), str(st, "drawTotal"), str(st, "lostTotal"), str(st, "goalsForTotal"), str(st, "goalsAgainstTotal"),
			str(st, "cleanSheetTotal"), str(st, "failedToScoreTotal"))
	}
	if f := str(t, "shape"); f != "" {
		fmt.Fprintf(&b, "Form (most recent last): %s\n", f)
	}

	now := time.Now().UTC()
	var past, next []obj
	for _, m := range list(t["fixtures"]) {
		k, _ := kickoff(m)
		if isFinished(str(m, "status")) || (k.Before(now) && !isLive(str(m, "status"))) {
			past = append(past, m)
		} else {
			next = append(next, m)
		}
	}
	if len(past) > 0 {
		if len(past) > 6 {
			past = past[len(past)-6:]
		}
		b.WriteString("\nRecent results (UTC):\n")
		for _, m := range past {
			fmt.Fprintf(&b, "- %s (%s)\n", matchLine(lang, m, true), str(m, "leaguename"))
		}
	}
	if len(next) > 0 {
		if len(next) > 6 {
			next = next[:6]
		}
		b.WriteString("\nNext matches (UTC):\n")
		for _, m := range next {
			fmt.Fprintf(&b, "- %s (%s)\n", matchLine(lang, m, true), str(m, "leaguename"))
		}
	}

	if argBool(a, "squad", true) {
		groups := map[string][]string{}
		for _, p := range list(t["squad"]) {
			pos := positionNames[str(p, "position")]
			if pos == "" {
				pos = "Others"
			}
			s := str(p, "name")
			if n := str(p, "number"); n != "" {
				s = n + " " + s
			}
			var bits []string
			if v := str(p, "age"); v != "" {
				bits = append(bits, v+"y")
			}
			if v := str(p, "appearences"); v != "" && v != "0" {
				bits = append(bits, v+" apps")
			}
			if v := str(p, "goals"); v != "" && v != "0" {
				bits = append(bits, v+" goals")
			}
			if str(p, "injured") == "True" {
				bits = append(bits, "injured")
			}
			bits = append(bits, "player "+str(p, "id"))
			groups[pos] = append(groups[pos], s+" ("+strings.Join(bits, ", ")+")")
		}
		if len(groups) > 0 {
			b.WriteString("\nSquad:\n")
			for _, pos := range []string{"Goalkeepers", "Defenders", "Midfielders", "Forwards", "Others"} {
				if ps := groups[pos]; len(ps) > 0 {
					fmt.Fprintf(&b, "%s: %s\n", pos, strings.Join(ps, "; "))
				}
			}
		}
	}
	if side := list(t["sidelined"]); len(side) > 0 {
		var names []string
		for _, p := range side {
			if str(p, "enddate") == "" {
				names = append(names, fmt.Sprintf("%s (%s, since %s)", str(p, "name"), str(p, "description"), str(p, "startdate")))
			}
		}
		if len(names) > 0 {
			b.WriteString("\nInjured/suspended: " + strings.Join(names, "; ") + "\n")
		}
	}
	tr := sub(t, "transfers")
	for _, dir := range [][2]string{{"in", "Transfers in"}, {"out", "Transfers out"}} {
		var lines []string
		for i, x := range list(tr[dir[0]]) {
			if i >= 8 {
				break
			}
			other := str(x, "from")
			if dir[0] == "out" {
				other = str(x, "to")
			}
			lines = append(lines, fmt.Sprintf("%s (%s, %s, %s)", str(x, "name"), other, str(x, "type"), str(x, "date")))
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", dir[1], strings.Join(lines, "; "))
		}
	}
	if tro := list(t["trophies"]); len(tro) > 0 {
		var lines []string
		for _, x := range tro {
			if str(x, "status") == "Winner" {
				lines = append(lines, fmt.Sprintf("%s ×%s", str(x, "league"), str(x, "count")))
			}
		}
		if len(lines) > 0 {
			b.WriteString("\nHonours: " + strings.Join(lines, ", ") + "\n")
		}
	}
	return b.String()
}

// --- Player ---

func formatPlayer(a obj, lang string, data any) string {
	p, _ := data.(map[string]any)
	if p == nil || str(p, "id") == "" {
		return "Player not found. Use search to find the player ID."
	}
	name := strings.TrimSpace(str(p, "firstname") + " " + str(p, "lastname"))
	if name == "" {
		name = str(p, "name")
	}
	id := str(p, "id")
	var b strings.Builder
	slugName := str(p, "name")
	if (slugName == "" || abbreviatedRe.MatchString(slugName)) && str(p, "firstname") != "" && str(p, "lastname") != "" {
		slugName = name
	}
	fmt.Fprintf(&b, "%s (player %s)\n%s\n", name, id, playerLink(lang, id, slugName))
	var facts []string
	for _, f := range [][2]string{{"position", ""}, {"age", "age "}, {"nationality", ""}, {"height", "height "}, {"preferredFoot", "foot "}} {
		if v := str(p, f[0]); v != "" {
			if f[0] == "height" {
				v += " cm"
			}
			facts = append(facts, f[1]+v)
		}
	}
	if v := str(p, "birthdate"); v != "" {
		facts = append(facts, "born "+v+" in "+strings.Trim(str(p, "birthplace")+", "+str(p, "birthcountry"), ", "))
	}
	b.WriteString(strings.Join(facts, " · ") + "\n")
	if team := str(p, "team"); team != "" {
		fmt.Fprintf(&b, "Club: %s (team %s) %s\n", team, str(p, "teamid"), teamLink(lang, str(p, "teamid"), team))
	}
	if mv := num(p, "marketvalue"); mv > 0 {
		fmt.Fprintf(&b, "Market value: €%s\n", humanMoney(mv))
	}
	if about := str(p, "about"); about != "" {
		b.WriteString("\n" + truncate(about, 600) + "\n")
	}

	for _, sec := range [][2]string{{"statistics", "League"}, {"statisticscups", "Domestic cups"}, {"statisticscupsintl", "International club competitions"}, {"statisticsintl", "National team"}} {
		rows := list(p[sec[0]])
		if len(rows) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s (season · competition · team: apps, goals, assists, minutes, rating):\n", sec[1])
		seen := map[string]bool{}
		shown := 0
		for _, s := range rows {
			if shown >= 6 {
				break
			}
			rowKey := str(s, "season") + "|" + str(s, "league") + "|" + str(s, "name")
			if seen[rowKey] {
				continue
			}
			seen[rowKey] = true
			shown++
			rating := str(s, "rating")
			if len(rating) > 4 {
				rating = rating[:4]
			}
			fmt.Fprintf(&b, "- %s · %s · %s: %s apps, %s goals, %s assists, %s min", str(s, "season"), str(s, "league"), str(s, "name"),
				orZero(str(s, "appearences")), orZero(str(s, "goals")), orZero(str(s, "assists")), orZero(str(s, "minutes")))
			if rating != "" {
				b.WriteString(", rating " + rating)
			}
			b.WriteString("\n")
		}
	}
	if side := list(p["sidelined"]); len(side) > 0 {
		var lines []string
		for i, s := range side {
			if i >= 5 {
				break
			}
			lines = append(lines, fmt.Sprintf("%s (%s – %s)", str(s, "type"), str(s, "date_start"), str(s, "date_end")))
		}
		b.WriteString("\nRecent injuries: " + strings.Join(lines, "; ") + "\n")
	}
	if tr := list(p["transfers"]); len(tr) > 0 {
		var lines []string
		for _, x := range tr {
			lines = append(lines, fmt.Sprintf("%s %s → %s (%s)", str(x, "date"), str(x, "from"), str(x, "to"), str(x, "type")))
		}
		b.WriteString("Transfers: " + strings.Join(lines, "; ") + "\n")
	}
	if tro := list(p["trophies"]); len(tro) > 0 {
		var lines []string
		for _, x := range tro {
			if str(x, "status") == "Winner" {
				lines = append(lines, fmt.Sprintf("%s ×%s", str(x, "league"), str(x, "count")))
			}
		}
		if len(lines) > 0 {
			b.WriteString("Honours: " + strings.Join(lines, ", ") + "\n")
		}
	}
	return b.String()
}

var abbreviatedRe = regexp.MustCompile(`^\p{L}{1,2}\.\s`)

func humanMoney(v int) string {
	switch {
	case v >= 1_000_000:
		return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", float64(v)/1e6), "0"), ".") + "m"
	case v >= 1_000:
		return fmt.Sprintf("%dk", v/1000)
	}
	return fmt.Sprint(v)
}

// --- Search: {result: [{type: t|p|l, ...}]} ---

func formatSearch(a obj, lang string, data any) string {
	d, _ := data.(map[string]any)
	results := list(d["result"])
	var b strings.Builder
	fmt.Fprintf(&b, "Search results for %q:\n", argStr(a, "q", ""))
	if len(results) == 0 {
		b.WriteString("Nothing found. Try another spelling or a shorter name.\n")
		return b.String()
	}
	var teams, players, leagues []string
	for _, r := range results {
		switch str(r, "type") {
		case "t":
			line := fmt.Sprintf("- %s (%s) · team %s · %s", str(r, "name"), str(r, "country"), str(r, "id_gs"), teamLink(lang, str(r, "id_gs"), str(r, "name")))
			for _, m := range list(r["matches"]) {
				line += "\n    " + matchLine(lang, m, true) + " (" + str(m, "leaguename") + ")"
			}
			teams = append(teams, line)
		case "p":
			players = append(players, fmt.Sprintf("- %s (%s) · %s, %s · player %s · %s", str(r, "name"), str(r, "country_player"),
				str(r, "team_name"), str(r, "league_name"), str(r, "id_gs"), playerLink(lang, str(r, "id_gs"), str(r, "name"))))
		case "l":
			leagues = append(leagues, fmt.Sprintf("- %s (%s) · league_key %s · %s", str(r, "name"), str(r, "country"), str(r, "keygs"), leagueLink(lang, str(r, "keygs"), "")))
		}
	}
	for _, sec := range []struct {
		title string
		lines []string
	}{{"Competitions", leagues}, {"Teams", teams}, {"Players", players}} {
		if len(sec.lines) > 0 {
			fmt.Fprintf(&b, "\n%s:\n%s\n", sec.title, strings.Join(sec.lines, "\n"))
		}
	}
	return b.String()
}
