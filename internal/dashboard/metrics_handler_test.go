package dashboard

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

func TestHandleMetricsAppliesFiltersAndRange(t *testing.T) {
	fs := &handlerStore{reviews: []store.Review{
		{Model: "gpt-5.5", Effort: "high", Verdict: "APPROVED", ReviewedAt: time.Now()},
		{Model: "gpt-5.6-terra", Effort: "medium", Verdict: "COMMENTED", ReviewedAt: time.Now()},
	}}
	code, resp := doJSON(t, newTestServer(fs, config.Config{}).handleMetrics, http.MethodGet, "/api/metrics?range=7d&model=gpt-5.5&effort=high", "")
	if code != http.StatusOK || resp["summary"].(map[string]any)["reviews"] != float64(1) {
		t.Fatalf("metrics = %d %v", code, resp)
	}
	if age := time.Since(fs.since); age < 5*24*time.Hour || age > 8*24*time.Hour {
		t.Errorf("range cutoff age = %s, want about 7 days", age)
	}
}

func TestHandleMetricsStoreError(t *testing.T) {
	fs := &handlerStore{sinceErr: errors.New("duckdb down")}
	if code, _ := doJSON(t, newTestServer(fs, config.Config{}).handleMetrics, http.MethodGet, "/api/metrics", ""); code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500", code)
	}
}
