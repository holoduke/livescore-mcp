package main

// Prompts: ready-made requests clients offer as slash commands or menu items

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type promptDef struct {
	name, description string
	args              [][3]string // name, description, "required" or ""
	text              func(a map[string]string) string
}

var prompts = []promptDef{
	{
		name:        "match_preview",
		description: "Preview of the next match between two teams: form, injuries, table positions and head-to-head",
		args:        [][3]string{{"team_a", "First team (name or ID)", "required"}, {"team_b", "Second team (name or ID)", "required"}},
		text: func(a map[string]string) string {
			return fmt.Sprintf(`Write a preview of the next match between %s and %s.
Use get_head_to_head for their record and the date of the next meeting, get_team for each team's recent results, injuries and league position, and get_standings for the table if they play in the same league.
Cover: kick-off time, current form, key absentees, the table situation and the head-to-head history. End with a short, hedged outlook. Cite the football-mania.com links from the tool answers.`, a["team_a"], a["team_b"])
		},
	},
	{
		name:        "league_roundup",
		description: "Round-up of a competition: latest results, the table and the top scorers",
		args:        [][3]string{{"league", "Competition name or league key (e.g. Premier League)", "required"}},
		text: func(a map[string]string) string {
			return fmt.Sprintf(`Give a round-up of %s.
Use get_league_fixtures for the latest results and the next fixtures, get_standings for the table and get_top_scorers for the scorers.
Cover: the standout results, the title race and relegation battle, the leading scorers and the biggest upcoming matches. Cite the football-mania.com links from the tool answers.`, a["league"])
		},
	},
	{
		name:        "team_report",
		description: "Report on a team: results, upcoming fixtures, squad news and season statistics",
		args:        [][3]string{{"team", "Team name or ID", "required"}},
		text: func(a map[string]string) string {
			return fmt.Sprintf(`Write a short report on %s.
Use get_team for results, upcoming fixtures, injuries, transfers and season statistics, and get_standings for the league table.
Cover: recent form, league position, injury news and the next three matches. Cite the football-mania.com links from the tool answers.`, a["team"])
		},
	},
	{
		name:        "live_now",
		description: "What is happening in football right now",
		args:        [][3]string{{"country", "Only this country (optional, in English)", ""}},
		text: func(a map[string]string) string {
			where := "worldwide"
			if c := strings.TrimSpace(a["country"]); c != "" {
				where = "in " + c + " (pass country to get_live_scores)"
			}
			return fmt.Sprintf(`Tell me what is happening in football right now %s.
Use get_live_scores and highlight the biggest matches and any surprising scores; for one or two key matches use get_match for goals and red cards. If nothing is live, use get_day_fixtures for today's remaining kick-offs. Cite the football-mania.com links from the tool answers.`, where)
		},
	},
}

func registerPrompts(s *server.MCPServer) {
	for _, p := range prompts {
		opts := []mcp.PromptOption{mcp.WithPromptDescription(p.description)}
		for _, arg := range p.args {
			argOpts := []mcp.ArgumentOption{mcp.ArgumentDescription(arg[1])}
			if arg[2] == "required" {
				argOpts = append(argOpts, mcp.RequiredArgument())
			}
			opts = append(opts, mcp.WithArgument(arg[0], argOpts...))
		}
		p := p
		s.AddPrompt(mcp.NewPrompt(p.name, opts...), func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			for _, arg := range p.args {
				if arg[2] == "required" && strings.TrimSpace(req.Params.Arguments[arg[0]]) == "" {
					return nil, fmt.Errorf("missing argument %q", arg[0])
				}
			}
			return mcp.NewGetPromptResult(p.description, []mcp.PromptMessage{
				mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(p.text(req.Params.Arguments))),
			}), nil
		})
	}
}
