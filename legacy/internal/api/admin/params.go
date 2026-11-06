package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// chiParam reads a path parameter.
func chiParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}
