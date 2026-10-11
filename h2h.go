package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerH2H(s *server.MCPServer) {
	s.AddTool(tool("get_head_to_head", "Head-to-head record",
		"All-time head-to-head record of two teams: wins, draws, goals, their recent meetings and the next scheduled meeting. Teams by ID or name.",
		mcp.WithString("team_a", mcp.Required(), mcp.Description("First team: ID (e.g. 9002) or name (e.g. Arsenal)")),
		mcp.WithString("team_b", mcp.Required(), mcp.Description("Second team: ID or name")),
		mcp.WithNumber("limit", mcp.Description("Meetings listed. Default: 10")),
		langParam, tzParam, formatParam,
	), handleH2H)
}

func handleH2H(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	a := args(req.Params.Arguments)
	v, apiLang, err := newView(a)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var ids [2]string
	for i, key := range []string{"team_a", "team_b"} {
		raw, err := requireArg(a, key)
		if err == nil {
			ids[i], err = resolveID(ctx, apiLang, untyped(raw), "t")
		}
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
	}
	if ids[0] == ids[1] {
		return mcp.NewToolResultError("team_a and team_b are the same team"), nil
	}
	idA, idB := ids[0], ids[1]

	data, body, err := fetchJSON(ctx, "h2h/"+idA+"_"+idB+".json", apiLang, nil)
	if err == errNotFound {
		data, body, err = fetchJSON(ctx, "h2h/"+idB+"_"+idA+".json", apiLang, nil)
	}
	if err != nil && err != errNotFound {
		return toolError(err, "Head-to-head"), nil
	}
	if argStr(a, "format", "text") == "json" && body != nil {
		return mcp.NewToolResultText(compactJSON(body)), nil
	}

	// Meetings of exactly this pair, newest first
	pair := func(m obj) bool {
		h, w := str(m, "gs_localteamid"), str(m, "gs_visitorteamid")
		return (h == idA && w == idB) || (h == idB && w == idA)
	}
	var meetings []obj
	d, _ := data.(map[string]any)
	for _, m := range list(d["history_matches"]) {
		if pair(m) && str(m, "id") != "" {
			meetings = append(meetings, m)
		}
	}
	sort.SliceStable(meetings, func(i, j int) bool {
		ti, _ := kickoff(meetings[i])
		tj, _ := kickoff(meetings[j])
		return ti.After(tj)
	})

	// Next meeting: from team A's fixture list
	var next obj
	if team, _, err := fetchJSON(ctx, "team_gs/"+idA+".json", apiLang, nil); err == nil {
		t, _ := team.(map[string]any)
		for _, m := range list(t["fixtures"]) {
			st := str(m, "status")
			if pair(m) && !isFinished(st) && !isOff(st) {
				next = m
				break
			}
		}
	}

	nameOf := func(id string) string {
		for _, m := range append(meetings, next) {
			if m == nil {
				continue
			}
			if str(m, "gs_localteamid") == id {
				return str(m, "localteam")
			}
			if str(m, "gs_visitorteamid") == id {
				return str(m, "visitorteam")
			}
		}
		return "team " + id
	}
	nameA, nameB := nameOf(idA), nameOf(idB)
	if len(meetings) == 0 && next == nil {
		return mcp.NewToolResultText(fmt.Sprintf("No meetings found between %s and %s.", nameA, nameB)), nil
	}

	var winsA, winsB, draws, goalsA, goalsB, played int
	for _, m := range meetings {
		if !isFinished(str(m, "status")) {
			continue
		}
		var h, w int
		if _, err := fmt.Sscanf(score(m), "%d - %d", &h, &w); err != nil {
			continue
		}
		if str(m, "gs_localteamid") != idA {
			h, w = w, h
		}
		played++
		goalsA += h
		goalsB += w
		switch {
		case h > w:
			winsA++
		case w > h:
			winsB++
		default:
			draws++ // includes matches decided on penalties
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s vs %s: head-to-head (team %s vs team %s)\n%s\n", nameA, nameB, idA, idB, h2hLink(v.lang, idA, nameA, idB, nameB))
	if played > 0 {
		fmt.Fprintf(&b, "%d meetings on record: %s %d wins, %d draws, %s %d wins · goals %d-%d\n", played, nameA, winsA, draws, nameB, winsB, goalsA, goalsB)
		b.WriteString("(Draws include cup ties decided on penalties.)\n")
	}
	if next != nil {
		fmt.Fprintf(&b, "\nNext meeting (%s): %s (%s)\n", v.tz, v.matchLine(next, true), str(next, "leaguename"))
	}
	limit := argInt(a, "limit", 10)
	if len(meetings) > 0 {
		fmt.Fprintf(&b, "\nRecent meetings (%s):\n", v.tz)
		for i, m := range meetings {
			if i >= limit {
				break
			}
			fmt.Fprintf(&b, "- %s (%s)\n", v.matchLine(m, true), str(m, "leaguename"))
		}
	}
	return mcp.NewToolResultText(b.String()), nil
}

// h2hLink: one page per pair, lower team ID first
func h2hLink(lang, idA, nameA, idB, nameB string) string {
	if len(idB) < len(idA) || (len(idA) == len(idB) && idB < idA) {
		idA, nameA, idB, nameB = idB, nameB, idA, nameA
	}
	slug := slugify(nameA + " vs " + nameB)
	if slug != "" {
		slug += "-"
	}
	return link(lang, fmt.Sprintf("/h2h/%s%s-%s", slug, idA, idB))
}
