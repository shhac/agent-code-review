package cli

// The codex model-catalog client: `codex debug models` spawned and parsed
// for the model/effort vocabularies. Consumed by shell completion and the
// config-key completions; kept apart from the cobra completion plumbing
// because it is the one piece that runs a subprocess and parses external
// JSON.

import (
	"context"
	"encoding/json"
	"os/exec"

	"github.com/shhac/agent-code-review/internal/config"
)

type codexModel struct {
	Slug                     string `json:"slug"`
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

func codexModels(ctx context.Context) ([]codexModel, error) {
	bin := config.Read().Review.Codex.Bin
	if bin == "" {
		bin = "codex"
	}
	ctx, cancel := context.WithTimeout(ctx, completionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "debug", "models").Output()
	if err != nil {
		return nil, err
	}
	return parseCodexModels(out)
}

func parseCodexModels(data []byte) ([]codexModel, error) {
	var response struct {
		Models []codexModel `json:"models"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return response.Models, nil
}

func codexModelSlugs(ctx context.Context) []string {
	models, err := codexModels(ctx)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(models))
	for _, model := range models {
		values = append(values, model.Slug)
	}
	return values
}

func codexModelEfforts(ctx context.Context, modelSlug string) []string {
	models, err := codexModels(ctx)
	if err != nil {
		return nil
	}
	var values []string
	for _, model := range models {
		if modelSlug != "" && model.Slug != modelSlug {
			continue
		}
		for _, level := range model.SupportedReasoningLevels {
			values = append(values, level.Effort)
		}
	}
	return values
}
