package cli

import (
	"testing"

	"github.com/shhac/agent-code-review/internal/pricing"
)

// TestEstimatorRefusesToGuess pins the two ways estimator must decline. Both
// mean "cannot estimate", and both must report false rather than a zero the
// caller would record as a genuinely free review.
func TestEstimatorRefusesToGuess(t *testing.T) {
	// An empty cache lists no model, which is the unlisted-model case.
	est := estimator(pricing.Open(t.TempDir()))

	if _, ok := est("gpt-5.6", 1000, 200, 0, 0); ok {
		t.Error("a model the price table does not list must not be estimated")
	}
	if _, ok := est("gpt-5.6", 0, 0, 5000, 900000); ok {
		t.Error("a review with no input/output split must not be estimated, even with cache tokens")
	}
}
