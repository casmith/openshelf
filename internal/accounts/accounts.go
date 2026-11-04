package accounts

import (
	"net/http"

	"github.com/casmith/openshelf/internal/deps"
)

// Register adds this package's routes. Owned by task S08 (docs/plan.md).
func Register(mux *http.ServeMux, d *deps.Deps) {}
