package igdb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const externalBatch = 500

// GamesBySteamApp maps Steam appids to IGDB game IDs via external_games. Appids
// without a match are absent from the result. Requests go through the provider's
// rate limiter (4 req/s), at most 500 appids per query.
func (p *Provider) GamesBySteamApp(ctx context.Context, appIDs []string) (map[string]string, error) {
	out := map[string]string{}
	if !p.Configured() {
		return out, errors.New("igdb: client ID and secret are not configured")
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(appIDs))
	for _, id := range appIDs {
		if _, err := strconv.ParseUint(id, 10, 64); err != nil || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for start := 0; start < len(ids); start += externalBatch {
		batch := ids[start:min(start+externalBatch, len(ids))]
		var rows []struct {
			Game int64  `json:"game"`
			UID  string `json:"uid"`
		}
		body := fmt.Sprintf(`fields game,uid; where uid = ("%s") & external_game_source = %d; limit %d;`,
			strings.Join(batch, `","`), steamSource, externalBatch)
		if err := p.query(ctx, "external_games", body, &rows); err != nil {
			return out, err
		}
		for _, r := range rows {
			if r.Game > 0 && r.UID != "" && seen[r.UID] {
				if _, dup := out[r.UID]; !dup {
					out[r.UID] = strconv.FormatInt(r.Game, 10)
				}
			}
		}
	}
	return out, nil
}
