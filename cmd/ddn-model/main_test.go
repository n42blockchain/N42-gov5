package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrainInspectPredict(t *testing.T) {
	for _, algorithm := range []string{"bayes", "transformer"} {
		t.Run(algorithm, func(t *testing.T) {
			dir := t.TempDir()
			dataset := filepath.Join(dir, "training.jsonl")
			model := filepath.Join(dir, "model.json")
			if err := os.WriteFile(dataset, []byte("{\"label\":\"RED\",\"text\":\"red\"}\n{\"label\":\"BLUE\",\"text\":\"blue\"}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diagnostic bytes.Buffer
			args := []string{"train", "-algorithm", algorithm, "-data", dataset, "-model", model, "-task", "test", "-schema", "test-v1", "-hidden", "4", "-heads", "1", "-layers", "1", "-feed-forward", "8", "-sequence", "16", "-epochs", "30", "-learning-rate", "0.02", "-confidence-ppm", "0"}
			if err := run(context.Background(), args, &out, &diagnostic); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "artifact_hash") {
				t.Fatal(out.String())
			}
			out.Reset()
			if err := run(context.Background(), []string{"inspect", "-algorithm", algorithm, "-model", model}, &out, &diagnostic); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			if err := run(context.Background(), []string{"predict", "-algorithm", algorithm, "-model", model, "-input", "red"}, &out, &diagnostic); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), `"label":"RED"`) {
				t.Fatal(out.String())
			}
		})
	}
}
func TestInvalidDataDoesNotWriteModel(t *testing.T) {
	dir := t.TempDir()
	dataset := filepath.Join(dir, "bad.jsonl")
	model := filepath.Join(dir, "model.json")
	os.WriteFile(dataset, []byte("{\"label\":\"RED\",\"text\":\"red\",\"extra\":true}"), 0600)
	if err := run(context.Background(), []string{"train", "-data", dataset, "-model", model}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown fields accepted")
	}
	if _, err := os.Stat(model); !os.IsNotExist(err) {
		t.Fatal("invalid training wrote model")
	}
}
