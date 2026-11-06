package quota

import (
	"encoding/json"

	"czapi/internal/domain"
)

// usageEnvelope is the wire format of a metering event on the usage topic.
// It is versioned so the worker can evolve independently of the API process.
type usageEnvelope struct {
	V int `json:"v"`
	domain.UsageRecord
}

// marshalUsage serialises a usage record for the queue.
func marshalUsage(r domain.UsageRecord) ([]byte, error) {
	return json.Marshal(usageEnvelope{V: 1, UsageRecord: r})
}

// UnmarshalUsage decodes a metering event. It accepts both the enveloped form
// and a bare record so older queue entries keep working.
func UnmarshalUsage(payload []byte) (domain.UsageRecord, error) {
	var env usageEnvelope
	if err := json.Unmarshal(payload, &env); err == nil && env.V > 0 {
		return env.UsageRecord, nil
	}
	var rec domain.UsageRecord
	err := json.Unmarshal(payload, &rec)
	return rec, err
}

// MarshalUsageForTest exposes the production encoder to the worker tests.
//
// The consumer must be exercised against the real wire format, not a look-alike:
// a divergence between encoder and consumer is exactly the kind of bug that would
// surface only as missing revenue in production.
func MarshalUsageForTest(r domain.UsageRecord) ([]byte, error) { return marshalUsage(r) }
