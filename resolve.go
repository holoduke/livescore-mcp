package main

// Names instead of IDs: "Arsenal", "Vinicius", "Eredivisie" are looked up
// with search, so a model can skip the search step.

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
)

var (
	idRe        = regexp.MustCompile(`^\d+(_[a-z0-9]+)*$`)
	leagueKeyRe = regexp.MustCompile(`^[A-Za-z0-9]+$`)
)

// searchFirst returns the best search hit of a type (t team, p player,
// l league): its ID (league key for leagues) and name
func searchFirst(ctx context.Context, apiLang, q, typ string) (id, name string, err error) {
	data, _, err := fetchJSON(ctx, "search_v3", apiLang, url.Values{"q": {q}})
	if err != nil {
		return "", "", err
	}
	d, _ := data.(map[string]any)
	for _, r := range list(d["result"]) {
		if str(r, "type") != typ {
			continue
		}
		if typ == "l" {
			return str(r, "keygs"), str(r, "name"), nil
		}
		return str(r, "id_gs"), str(r, "name"), nil
	}
	kind := map[string]string{"t": "team", "p": "player", "l": "competition"}[typ]
	return "", "", fmt.Errorf("no %s found for %q; try the search tool with another spelling", kind, q)
}

// resolveID passes IDs through and looks names up
func resolveID(ctx context.Context, apiLang, v, typ string) (string, error) {
	if idRe.MatchString(v) {
		return v, nil
	}
	id, _, err := searchFirst(ctx, apiLang, v, typ)
	return id, err
}

// withLeague fetches by league key; a name ("La Liga"), or a key the API
// does not know, is looked up with search first
func withLeague(ctx context.Context, a obj, apiLang, raw string, fetch func(key string) (any, []byte, error)) (any, []byte, error) {
	if raw == "" {
		return nil, nil, fmt.Errorf("missing required argument \"league_key\"")
	}
	if leagueKeyRe.MatchString(raw) {
		a["_key"] = raw
		data, body, err := fetch(raw)
		if err != errNotFound {
			return data, body, err
		}
	}
	key, _, err := searchFirst(ctx, apiLang, raw, "l")
	if err != nil {
		return nil, nil, err
	}
	a["_key"] = key
	return fetch(key)
}
