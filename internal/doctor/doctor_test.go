package doctor

import "testing"

// Blocking is what the exit code and the boot warning both key off, so it
// must report only genuine blockers.
func TestBlockingSelectsFailedBlockers(t *testing.T) {
	got := Blocking([]Check{
		{Name: "gh", OK: true, Blocking: true},
		{Name: "engine:codex", OK: false, Blocking: true, Detail: "not on PATH"},
		{Name: "cosmetic", OK: false, Blocking: false},
	})
	if len(got) != 1 || got[0].Name != "engine:codex" {
		t.Errorf("Blocking() = %+v, want only the failed blocking check", got)
	}
}

func TestBlockingEmptyWhenHealthy(t *testing.T) {
	if got := Blocking([]Check{{Name: "gh", OK: true, Blocking: true}}); len(got) != 0 {
		t.Errorf("Blocking() = %+v, want none", got)
	}
}

// A missing binary must be diagnosed rather than reported as a version, and
// must carry a hint: the whole point is telling the operator what to do.
func TestBinaryCheckOnMissingBinary(t *testing.T) {
	c := binaryCheck(t.Context(), "engine:nope", "definitely-not-a-real-binary-xyz", "--version", "install it")
	if c.OK || !c.Blocking || c.Hint == "" {
		t.Errorf("check = %+v, want a blocking failure with a hint", c)
	}
}
