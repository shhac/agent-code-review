package usage

import (
	"strings"
	"testing"
)

func TestParseRateLimits(t *testing.T) {
	// A realistic stream: init response (id=1), a notification, then the
	// rate-limit response (id=2) — captured shape from codex 0.138.0.
	stream := `{"id":1,"result":{"userAgent":"x"}}
{"jsonrpc":"2.0","method":"some/notification","params":{}}
not even json
{"id":2,"result":{"rateLimits":{"planType":"prolite","primary":{"usedPercent":46,"windowDurationMins":300,"resetsAt":1783438334},"secondary":{"usedPercent":7,"windowDurationMins":10080,"resetsAt":1784025134}}}}
`
	snap, err := parseRateLimits(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Plan != "prolite" {
		t.Errorf("plan = %q", snap.Plan)
	}
	if snap.Primary == nil || snap.Primary.UsedPercent != 46 || snap.Primary.WindowMins != 300 {
		t.Errorf("primary = %+v", snap.Primary)
	}
	if snap.Secondary == nil || snap.Secondary.WindowMins != 10080 {
		t.Errorf("secondary = %+v", snap.Secondary)
	}
	if snap.FetchedAt.IsZero() {
		t.Error("FetchedAt must be stamped")
	}
}

func TestParseRateLimitsErrorAndEOF(t *testing.T) {
	if _, err := parseRateLimits(strings.NewReader(`{"id":2,"error":{"message":"nope"}}` + "\n")); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("rpc error must surface, got %v", err)
	}
	if _, err := parseRateLimits(strings.NewReader(`{"id":1,"result":{}}` + "\n")); err == nil {
		t.Error("stream ending without id=2 must error")
	}
	// Missing windows → nil pointers, no panic.
	snap, err := parseRateLimits(strings.NewReader(`{"id":2,"result":{"rateLimits":{"planType":"x"}}}` + "\n"))
	if err != nil || snap.Primary != nil || snap.Secondary != nil {
		t.Errorf("windowless snapshot mishandled: %+v err=%v", snap, err)
	}
}
