package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	libcli "github.com/shhac/lib-agent-cli/cli"

	"github.com/shhac/agent-code-review/internal/config"
	"github.com/shhac/agent-code-review/internal/store"
)

// captureStdout routes records into a buffer under the given -f format.
func captureStdout(t *testing.T, format string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldOut, oldGlobals := stdout, globals
	stdout, globals = &buf, &libcli.Globals{Format: format}
	t.Cleanup(func() { stdout, globals = oldOut, oldGlobals })
	return &buf
}

type listed struct {
	HeadSHA string `json:"head_sha"`
}

// -f json has to be ONE document. Emitting each record on its own printed N
// pretty objects back to back, which no JSON parser reads as a whole.
func TestListUnderJSONIsOneDocument(t *testing.T) {
	buf := captureStdout(t, "json")
	if err := emitEach([]listed{{"a"}, {"b"}}, nil); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Data []listed `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("-f json list is not one JSON document (%v):\n%s", err, buf)
	}
	if len(doc.Data) != 2 || doc.Data[0].HeadSHA != "a" || doc.Data[1].HeadSHA != "b" {
		t.Errorf("data = %+v, want both records in order", doc.Data)
	}
}

func TestEmptyListUnderJSONIsAnEmptyArray(t *testing.T) {
	buf := captureStdout(t, "json")
	if err := emitEach([]listed{}, nil); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if data, ok := doc["data"].([]any); !ok || len(data) != 0 {
		t.Errorf("data = %#v, want an empty array, not null", doc["data"])
	}
}

// yaml marshals Go structs by FIELD name, so each record still has to pass
// through JSON first to keep the tag names every other format uses.
func TestListUnderYAMLIsOneDocumentWithJSONKeys(t *testing.T) {
	buf := captureStdout(t, "yaml")
	if err := emitEach([]listed{{"a"}, {"b"}}, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "data:") || strings.Count(out, "data:") != 1 {
		t.Errorf("-f yaml list is not one data envelope:\n%s", out)
	}
	if strings.Count(out, "head_sha:") != 2 || strings.Contains(out, "HeadSHA") {
		t.Errorf("records lost their json key names:\n%s", out)
	}
}

// The default format is the one scripts read, so moving to the list writer
// must not change a byte of it.
func TestListUnderNDJSONMatchesOneRecordPerLine(t *testing.T) {
	items := []listed{{"a"}, {"b"}}
	buf := captureStdout(t, "")
	if err := emitEach(items, nil); err != nil {
		t.Fatal(err)
	}
	listedOut := buf.String()
	buf.Reset()
	for _, it := range items {
		if err := emit(it); err != nil {
			t.Fatal(err)
		}
	}
	if listedOut != buf.String() {
		t.Errorf("NDJSON changed:\n got %q\nwant %q", listedOut, buf.String())
	}
}

// A configured gh_user is the answer, not a hint: it must win without asking
// gh, which may be logged in as somebody else or not at all.
func TestResolveGHUserPrefersConfig(t *testing.T) {
	warned := false
	got := resolveGHUser(context.Background(), config.Config{GHUser: "reviewer-bot"}, func(string, string) { warned = true })
	if got != "reviewer-bot" || warned {
		t.Errorf("resolveGHUser = %q (warned %v), want the configured login and no warning", got, warned)
	}
}

// The summary is metadata about the run, not one more outcome: a bare record
// looked like a malformed outcome to anything reading records.
func TestRunSummaryRidesAsMetadata(t *testing.T) {
	outcomes := []store.Review{{Repo: "o/r", Number: 1, Verdict: store.VerdictApproved}}

	t.Run("ndjson", func(t *testing.T) {
		buf := captureStdout(t, "")
		if err := emitRun(outcomes, time.Minute); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) != 2 {
			t.Fatalf("want one outcome line and one @summary line, got:\n%s", buf)
		}
		var trailer map[string]map[string]any
		if err := json.Unmarshal([]byte(lines[1]), &trailer); err != nil {
			t.Fatal(err)
		}
		if got := trailer["@summary"]["outcomes"]; got != float64(1) {
			t.Errorf("@summary line = %s, want outcomes 1", lines[1])
		}
	})

	t.Run("json", func(t *testing.T) {
		buf := captureStdout(t, "json")
		if err := emitRun(outcomes, time.Minute); err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Data    []store.Review `json:"data"`
			Summary struct {
				Outcomes     int `json:"outcomes"`
				DurationSecs int `json:"duration_secs"`
			} `json:"@summary"`
		}
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			t.Fatalf("-f json run output is not one document (%v):\n%s", err, buf)
		}
		if len(doc.Data) != 1 || doc.Summary.Outcomes != 1 || doc.Summary.DurationSecs != 60 {
			t.Errorf("run document = %+v", doc)
		}
	})
}
