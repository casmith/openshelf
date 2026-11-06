package steam

import (
	"net/http"

	"github.com/casmith/openshelf/internal/deps"
)

// Register serves /connections/steam/* (S20).
func Register(mux *http.ServeMux, d *deps.Deps) {}
