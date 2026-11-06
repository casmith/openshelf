package igdb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/casmith/openshelf/internal/providers"
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

type timeToBeat struct {
	GameID     int64  `json:"game_id"`
	Hastily    *int64 `json:"hastily"`
	Normally   *int64 `json:"normally"`
	Completely *int64 `json:"completely"`
	Count      int32  `json:"count"`
}

// TimesToBeat returns community time-to-beat estimates keyed by IGDB game ID.
// Games without an estimate are absent. At most 500 IDs go in each query.
func (p *Provider) TimesToBeat(ctx context.Context, gameIDs []string) (map[string]providers.TimeToBeat, error) {
	out := map[string]providers.TimeToBeat{}
	if !p.Configured() {
		return out, errors.New("igdb: client ID and secret are not configured")
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(gameIDs))
	for _, id := range gameIDs {
		if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for start := 0; start < len(ids); start += externalBatch {
		batch := ids[start:min(start+externalBatch, len(ids))]
		var rows []timeToBeat
		body := fmt.Sprintf("fields game_id,hastily,normally,completely,count; where game_id = (%s); limit %d;",
			strings.Join(batch, ","), externalBatch)
		if err := p.query(ctx, "game_time_to_beats", body, &rows); err != nil {
			return out, err
		}
		for _, r := range rows {
			t := providers.TimeToBeat{
				HastilyMinutes: minutes(r.Hastily), NormallyMinutes: minutes(r.Normally),
				CompletelyMinutes: minutes(r.Completely), Submissions: r.Count,
			}
			if t.HastilyMinutes == nil && t.NormallyMinutes == nil && t.CompletelyMinutes == nil {
				continue
			}
			out[strconv.FormatInt(r.GameID, 10)] = t
		}
	}
	return out, nil
}

func minutes(seconds *int64) *int32 {
	if seconds == nil || *seconds <= 0 {
		return nil
	}
	m := int32(min((*seconds+30)/60, 1<<31-1))
	return &m
}
