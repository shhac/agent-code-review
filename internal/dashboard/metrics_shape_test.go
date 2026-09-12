package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/store"
)

// The embedded usageMetric must INLINE into the same JSON object. A tag on the
// embed, or making it a pointer, would nest the figures under a key and break
// the Metrics page with no Go-side error at all.
func TestModelMetricJSONStaysFlat(t *testing.T) {
	got := metricsFor([]store.Review{{
		Model: "m", Effort: "high", EngineVersion: "v1", Verdict: "APPROVED",
		FreshTokens: 10, DurationSecs: 5, ReviewedAt: time.Now(),
	}}, "", "")
	b, err := json.Marshal(got.Models[0])
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	for _, key := range []string{`"model"`, `"effort"`, `"reviews"`, `"fresh_tokens"`, `"cache_read_tokens"`, `"median_duration_secs"`, `"median_cost_usd"`, `"versions"`} {
		if !strings.Contains(raw, key) {
			t.Errorf("missing %s at the top level: %s", key, raw)
		}
	}
	if strings.Contains(raw, `"usageMetric"`) || strings.Contains(raw, `"usage"`) {
		t.Errorf("figures were nested rather than inlined: %s", raw)
	}
	vb, _ := json.Marshal(got.Models[0].Versions[0])
	if !strings.Contains(string(vb), `"engine_version"`) || !strings.Contains(string(vb), `"reviews"`) {
		t.Errorf("version row lost its inlined figures: %s", vb)
	}
}
