package experiences

import (
	"math"

	"github.com/casmith/openshelf/internal/db"
)

// Effective is the progress to show for an experience, preferring linked-account
// values with the same rules as the item page: fraction progress (Audiobookshelf)
// becomes a percent for any unit, and playtime (Steam) stands in for hours.
func Effective(unit *db.ProgressUnit, cur, total *float64, hasProgress bool, fraction float64, hasPlaytime bool, minutes int32) (*db.ProgressUnit, *float64, *float64) {
	switch {
	case unit == nil:
	case hasProgress:
		pct, hundred := math.Round(min(max(fraction, 0), 1)*100), 100.0
		u := db.ProgressUnitPercent
		return &u, &pct, &hundred
	case hasPlaytime && *unit == db.ProgressUnitHours:
		h := math.Round(float64(minutes)/6) / 10
		return unit, &h, total
	}
	return unit, cur, total
}
