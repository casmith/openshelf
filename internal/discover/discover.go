package discover

import (
	"net/http"

	"github.com/riverqueue/river"

	"github.com/casmith/openshelf/internal/deps"
)

// Register adds this package's routes. Owned by task S10 (docs/plan.md).
func Register(mux *http.ServeMux, d *deps.Deps) {}

func Workers(w *river.Workers, d *deps.Deps) {}

func PeriodicJobs(d *deps.Deps) []*river.PeriodicJob { return nil }
