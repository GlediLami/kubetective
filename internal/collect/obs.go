package collect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/GlediLami/kubetective/internal/model"
)

// NewObservation builds a normalized Observation with a stable content-hashed
// ID so identical facts from different queries dedup to the same ID (the
// timeline builder and graph rely on this).
func NewObservation(kind string, source model.SourceRef, ts time.Time, res model.ResourceRef, payload map[string]any, confidence float64) model.Observation {
	o := model.Observation{
		Kind:       kind,
		Source:     source,
		Timestamp:  ts,
		Resource:   res,
		Payload:    payload,
		Confidence: confidence,
	}
	if o.Confidence == 0 {
		o.Confidence = 1.0
	}
	o.ID = obsID(o)
	return o
}

func obsID(o model.Observation) string {
	b, err := json.Marshal(o.Payload)
	if err != nil {
		b = []byte(fmt.Sprintf("%v", o.Payload))
	}
	h := sha256.New()
	h.Write([]byte(o.Kind))
	h.Write([]byte{0})
	h.Write([]byte(o.Resource.Kind))
	h.Write([]byte{0})
	h.Write([]byte(o.Resource.Namespace))
	h.Write([]byte{0})
	h.Write([]byte(o.Resource.Name))
	h.Write([]byte{0})
	h.Write([]byte(o.Timestamp.UTC().Format(time.RFC3339Nano)))
	h.Write([]byte{0})
	h.Write(b)
	return "obs-" + hex.EncodeToString(h.Sum(nil)[:8])
}
