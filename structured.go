package main

import (
	"sort"
	"strconv"
	"strings"
)

// Structured tool output.
//
// Every tool keeps returning the same prose in `content` — that is what the
// models actually read, and changing it would break existing clients. These
// types are emitted alongside it as `structuredContent`, so a client that
// wants fields instead of text can have them without parsing English.
//
// The extractors below deliberately reuse the same helpers as the formatters
// (str, num, list, sub, score, isLive…) so the two views cannot drift apart.

// --- shared shapes ---

// Match is one fixture in a list.
type Match struct {
	ID          string `json:"id" jsonschema_description:"Match id, usable with get_match"`
	Home        string `json:"home" jsonschema_description:"Home team name"`
	Away        string `json:"away" jsonschema_description:"Away team name"`
	HomeScore   *int   `json:"homeScore,omitempty" jsonschema_description:"Goals for the home team; absent before kick-off"`
	AwayScore   *int   `json:"awayScore,omitempty" jsonschema_description:"Goals for the away team; absent before kick-off"`
	Status      string `json:"status" jsonschema_description:"Raw status: a minute while live, FT/HT/Postp. otherwise, or the kick-off time"`
	State       string `json:"state" jsonschema_description:"One of scheduled, live, finished, other"`
	Minute      int    `json:"minute,omitempty" jsonschema_description:"Minute played, only while live"`
	KickoffUTC  string `json:"kickoffUtc,omitempty" jsonschema_description:"Kick-off in RFC3339 UTC"`
	Country     string `json:"country,omitempty" jsonschema_description:"Country or confederation of the competition"`
	Competition string `json:"competition,omitempty" jsonschema_description:"Competition name"`
	LeagueKey   string `json:"leagueKey,omitempty" jsonschema_description:"Competition key, usable with get_standings and get_league_fixtures"`
	URL         string `json:"url,omitempty" jsonschema_description:"Match page on football-mania.com"`
}

// MatchList is the output of the fixture and live-score tools.
type MatchList struct {
	Total        int     `json:"total" jsonschema_description:"Matches found before any limit was applied"`
	Returned     int     `json:"returned" jsonschema_description:"Matches present in this response"`
	Competitions int     `json:"competitions" jsonschema_description:"Distinct competitions represented"`
	Timezone     string  `json:"timezone" jsonschema_description:"Time zone the kick-off times were rendered in"`
	Matches      []Match `json:"matches"`
}

// StandingsRow is one line of a league table.
type StandingsRow struct {
	Position int    `json:"position"`
	Group    string `json:"group,omitempty" jsonschema_description:"Group or stage name, when the competition has more than one table"`
	Team     string `json:"team"`
	TeamID   string `json:"teamId,omitempty" jsonschema_description:"Usable with get_team"`
	Played   int    `json:"played"`
	Won      int    `json:"won"`
	Drawn    int    `json:"drawn"`
	Lost     int    `json:"lost"`
	GoalsFor int    `json:"goalsFor"`
	GoalsAg  int    `json:"goalsAgainst"`
	GoalDiff int    `json:"goalDifference"`
	Points   int    `json:"points"`
	Form     string `json:"form,omitempty" jsonschema_description:"Recent results, most recent last, as W/D/L"`
	Note     string `json:"note,omitempty" jsonschema_description:"Qualification or relegation note, when the feed carries one"`
	Live     bool   `json:"live,omitempty" jsonschema_description:"true when these points include a match still in play"`
}

// Standings is the output of get_standings.
type Standings struct {
	Competition string         `json:"competition,omitempty"`
	LeagueKey   string         `json:"leagueKey,omitempty"`
	Country     string         `json:"country,omitempty" jsonschema_description:"Country the competition is filed under, or the confederation for international competitions"`
	Season      string         `json:"season,omitempty"`
	Groups      []string       `json:"groups,omitempty" jsonschema_description:"Group names, when the competition has more than one table"`
	Table       []StandingsRow `json:"table" jsonschema_description:"Every group's rows in order; each row names its group when there is more than one"`
	URL         string         `json:"url,omitempty"`
}

// Scorer is one row of a top-scorer list.
type Scorer struct {
	Rank        int    `json:"rank"`
	Player      string `json:"player"`
	PlayerID    string `json:"playerId,omitempty" jsonschema_description:"Usable with get_player"`
	Team        string `json:"team,omitempty"`
	TeamID      string `json:"teamId,omitempty" jsonschema_description:"Usable with get_team"`
	Nationality string `json:"nationality,omitempty"`
	Goals       int    `json:"goals"`
	Penalties   int    `json:"penalties,omitempty" jsonschema_description:"Of which scored from the penalty spot"`
	Tournament  string `json:"tournament,omitempty" jsonschema_description:"Stage name, when the competition splits into more than one"`
}

// TopScorers is the output of get_top_scorers.
type TopScorers struct {
	Competition string   `json:"competition,omitempty"`
	LeagueKey   string   `json:"leagueKey,omitempty"`
	Country     string   `json:"country,omitempty"`
	Scorers     []Scorer `json:"scorers"`
	URL         string   `json:"url,omitempty"`
}

// MatchReport is the output of get_match_report.
type MatchReport struct {
	MatchID     string   `json:"matchId"`
	Kind        string   `json:"kind" jsonschema_description:"report for a finished match, preview for one still to come"`
	Home        string   `json:"home"`
	Away        string   `json:"away"`
	Score       string   `json:"score,omitempty"`
	Competition string   `json:"competition,omitempty"`
	LeagueKey   string   `json:"leagueKey,omitempty"`
	Date        string   `json:"date,omitempty"`
	Language    string   `json:"language" jsonschema_description:"Language the text was returned in"`
	Text        string   `json:"text" jsonschema_description:"The report or preview, plain text"`
	GeneratedAt string   `json:"generatedAt,omitempty"`
	Sources     []string `json:"sources,omitempty" jsonschema_description:"URLs the report drew on, when recorded"`
	URL         string   `json:"url,omitempty"`
}

// CommentaryEvent is one entry of the live ticker.
type CommentaryEvent struct {
	Minute string `json:"minute,omitempty"`
	Type   string `json:"type,omitempty"`
	Team   string `json:"team,omitempty"`
	Player string `json:"player,omitempty"`
	Text   string `json:"text,omitempty"`
}

// Commentary is the output of get_match_commentary.
type Commentary struct {
	MatchID     string            `json:"matchId"`
	LastUpdated string            `json:"lastUpdated,omitempty"`
	Events      []CommentaryEvent `json:"events,omitempty" jsonschema_description:"Goals, cards and substitutions in order"`
	Ticker      []CommentaryEvent `json:"ticker,omitempty" jsonschema_description:"Minute-by-minute written commentary, when the competition has it"`
	URL         string            `json:"url,omitempty"`
}

// --- extractors ---

func intPtr(n int) *int { return &n }

// splitScore turns "2 - 1" into two pointers; returns nils when there is no score yet.
func splitScore(m obj) (*int, *int) {
	parts := strings.Split(score(m), "-")
	if len(parts) != 2 {
		return nil, nil
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	a, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return nil, nil
	}
	return intPtr(h), intPtr(a)
}

func matchState(m obj) (string, int) {
	st := str(m, "status")
	switch {
	case isLive(st):
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(st), "'"))
		return "live", n
	case isFinished(st):
		return "finished", 0
	case isOff(st):
		return "other", 0
	}
	return "scheduled", 0
}

func structMatch(v view, m obj, country, competition, key string) Match {
	state, minute := matchState(m)
	h, a := splitScore(m)
	out := Match{
		ID:          str(m, "id"),
		Home:        str(m, "localteam"),
		Away:        str(m, "visitorteam"),
		HomeScore:   h,
		AwayScore:   a,
		Status:      str(m, "status"),
		State:       state,
		Minute:      minute,
		Country:     country,
		Competition: competition,
		LeagueKey:   key,
		URL:         matchLink(v.lang, m),
	}
	if t, ok := kickoff(m); ok {
		out.KickoffUTC = t.UTC().Format("2006-01-02T15:04:05Z")
	}
	return out
}

// structDayFeed mirrors formatDayFeed: same filters, same ordering, same limit.
func structDayFeed(a obj, v view, data any, limit int) MatchList {
	country := strings.ToLower(argStr(a, "country", ""))
	key := strings.TrimSuffix(argStr(a, "league_key", ""), "_small")

	type grp struct {
		name, country, key string
		matches            []obj
	}
	var groups []grp
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
			groups = append(groups, grp{str(l, "league"), cn, str(l, "key"), ms})
			total += len(ms)
		}
	}
	sort.SliceStable(groups, func(i, j int) bool { return leagueRank(groups[i].key) < leagueRank(groups[j].key) })

	out := MatchList{Total: total, Competitions: len(groups), Timezone: v.tz, Matches: []Match{}}
	for _, g := range groups {
		for _, m := range g.matches {
			if len(out.Matches) >= limit {
				break
			}
			out.Matches = append(out.Matches, structMatch(v, m, g.country, g.name, g.key))
		}
	}
	out.Returned = len(out.Matches)
	return out
}

// dayLimit mirrors the limit clamp used by get_day_fixtures' text formatter so
// the structured list and the prose always contain the same matches.
func dayLimit(a obj) int {
	limit := argInt(a, "limit", 150)
	if limit < 1 || limit > 500 {
		limit = 500
	}
	return limit
}

// structStandings mirrors formatStandings: same group handling, same "no table"
// case (which yields an empty Table rather than an error), same zone field
// preference (description_v2 over description).
func structStandings(a obj, v view, data any) any {
	d, _ := data.(map[string]any)
	key := str(a, "_key")
	out := Standings{
		Competition: str(d, "leaguename"),
		LeagueKey:   key,
		Country:     str(d, "filegroup"),
		Season:      str(d, "season"),
		Table:       []StandingsRow{},
		URL:         leagueLink(v.lang, key, "table"),
	}
	groups := list(d["groups"])
	if str(d, "filegroup") == "empty" {
		return out
	}
	multi := len(groups) > 1
	for _, g := range groups {
		name := str(g, "group")
		if multi {
			out.Groups = append(out.Groups, name)
		}
		for _, t := range list(g["teams"]) {
			zone := str(t, "description_v2")
			if zone == "" {
				zone = str(t, "description")
			}
			row := StandingsRow{
				Position: num(t, "position"),
				Team:     str(t, "team"),
				TeamID:   str(t, "id_gs"),
				Played:   num(t, "totalWon") + num(t, "totalDraw") + num(t, "totalLost"),
				Won:      num(t, "totalWon"),
				Drawn:    num(t, "totalDraw"),
				Lost:     num(t, "totalLost"),
				GoalsFor: num(t, "totalGoalsFor"),
				GoalsAg:  num(t, "totalGoalsAgainst"),
				GoalDiff: num(t, "goalDifference"),
				Points:   num(t, "points"),
				Form:     str(t, "recentForm"),
				Note:     zone,
				Live:     str(t, "isLive") == "1",
			}
			if multi {
				row.Group = name
			}
			out.Table = append(out.Table, row)
		}
	}
	return out
}

// structTopScorers mirrors formatTopScorers, including the per-tournament limit:
// the text formatter applies `limit` to each tournament separately, so this does
// too, or the two views would list different players.
func structTopScorers(a obj, v view, data any) any {
	d, _ := data.(map[string]any)
	key := str(a, "_key")
	out := TopScorers{
		Competition: str(d, "league"),
		LeagueKey:   key,
		Country:     str(d, "country"),
		Scorers:     []Scorer{},
		URL:         leagueLink(v.lang, key, "top-scorers"),
	}
	limit := argInt(a, "limit", 20)
	tours := list(d["tournaments"])
	for _, t := range tours {
		name := ""
		if len(tours) > 1 {
			name = str(t, "tournament")
		}
		for i, p := range list(t["players"]) {
			if i >= limit {
				break
			}
			out.Scorers = append(out.Scorers, Scorer{
				Rank:        num(p, "pos"),
				Player:      str(p, "name"),
				PlayerID:    str(p, "id"),
				Team:        str(p, "team"),
				TeamID:      str(p, "team_id_gs"),
				Nationality: str(p, "nationality"),
				Goals:       num(p, "goals"),
				Penalties:   num(p, "penalty_goals"),
				Tournament:  name,
			})
		}
	}
	return out
}
