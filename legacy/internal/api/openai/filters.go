package openai

import (
	"encoding/json"
	"net/http"
	"time"

	"czapi/internal/store"
)

// internalUsageFilter is the local mirror of store.UsageFilter used by console
// handlers so the view layer stays independent of the repository signature.
type internalUsageFilter struct {
	AccountID uint64
	APIKeyID  uint64
	Model     string
	From      time.Time
	To        time.Time
	Limit     int
	Offset    int
}

// toStore converts to the repository filter.
func (f internalUsageFilter) toStore() store.UsageFilter {
	return store.UsageFilter{
		AccountID: f.AccountID,
		APIKeyID:  f.APIKeyID,
		Model:     f.Model,
		From:      f.From,
		To:        f.To,
		Limit:     f.Limit,
		Offset:    f.Offset,
	}
}

// decodeBody parses a JSON body into dst with a size cap.
func decodeBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	return dec.Decode(dst)
}
