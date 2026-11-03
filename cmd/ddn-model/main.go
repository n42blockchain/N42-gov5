// ddn-model trains, inspects and executes repository-native DDN models.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ddn/native"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	"github.com/n42blockchain/N42/internal/ddn/transformer"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out, diagnostic io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ddn-model train|inspect|predict [flags]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(diagnostic)
	algorithm := fs.String("algorithm", "bayes", "bayes or transformer")
	model := fs.String("model", "", "local artifact file")
	input := fs.String("input", "", "UTF-8 text for predict")
	data := fs.String("data", "", "training JSONL file: label,text")
	name := fs.String("name", "N42-local-classifier", "model name")
	version := fs.String("version", "1", "model version")
	task := fs.String("task", "node.anomaly", "task")
	schema := fs.String("schema", "health-v1", "schema")
	confidence := fs.Uint("confidence-ppm", 800000, "abstention threshold")
	coverage := fs.Uint("coverage-ppm", 500000, "Bayes vocabulary coverage threshold")
	hidden := fs.Int("hidden", 32, "Transformer hidden dimension")
	heads := fs.Int("heads", 4, "Transformer attention heads")
	layers := fs.Int("layers", 2, "Transformer layers")
	ff := fs.Int("feed-forward", 64, "Transformer FF dimension")
	sequence := fs.Int("sequence", 128, "max UTF-8 bytes including CLS")
	epochs := fs.Int("epochs", 20, "Transformer training epochs")
	lr := fs.Float64("learning-rate", 0.001, "AdamW learning rate")
	seed := fs.Int64("seed", 42, "initialization seed")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *confidence > d.PPM || *coverage > d.PPM {
		return errors.New("threshold exceeds ppm bound")
	}
	if *model == "" {
		return errors.New("-model is required")
	}
	var backend provider.DecisionProvider
	var artifactHash chain.Hash
	switch args[0] {
	case "train":
		if *data == "" {
			return errors.New("-data is required")
		}
		examples, err := readExamples(ctx, *data)
		if err != nil {
			return err
		}
		var encoded []byte
		switch *algorithm {
		case "bayes":
			a, err := native.Train(ctx, *name, *version, *task, *schema, examples, uint32(*confidence), uint32(*coverage))
			if err != nil {
				return err
			}
			encoded, err = a.Bytes()
			if err != nil {
				return err
			}
		case "transformer":
			labels := map[string]bool{}
			ts := make([]transformer.Example, len(examples))
			for i, e := range examples {
				labels[e.Label] = true
				ts[i] = transformer.Example{Label: e.Label, Text: e.Text}
			}
			ls := make([]string, 0, len(labels))
			for l := range labels {
				ls = append(ls, l)
			}
			sort.Strings(ls)
			a, err := transformer.Initialize(*name, *version, *task, *schema, ls, transformer.Config{Hidden: *hidden, Heads: *heads, Layers: *layers, FeedForward: *ff, MaxSequence: *sequence}, uint32(*confidence), *seed)
			if err != nil {
				return err
			}
			a, report, err := transformer.Train(ctx, a, ts, transformer.TrainConfig{Epochs: *epochs, LearningRate: *lr, WeightDecay: 0.01, ClipNorm: 1})
			if err != nil {
				return err
			}
			if err = json.NewEncoder(diagnostic).Encode(report); err != nil {
				return err
			}
			encoded, err = a.Bytes()
			if err != nil {
				return err
			}
		default:
			return errors.New("unsupported training algorithm")
		}
		if err := save(*model, encoded); err != nil {
			return err
		}
	case "inspect", "predict":
	default:
		return errors.New("unknown command")
	}
	switch *algorithm {
	case "bayes":
		a, err := native.Load(*model)
		if err != nil {
			return err
		}
		artifactHash, _ = a.Hash()
		backend, err = provider.NewNativeModel("did:n42:local", a)
		if err != nil {
			return err
		}
	case "transformer":
		a, err := transformer.Load(*model)
		if err != nil {
			return err
		}
		artifactHash, _ = a.Hash()
		backend, err = provider.NewTransformer("did:n42:local", a)
		if err != nil {
			return err
		}
	default:
		return errors.New("unsupported model algorithm")
	}
	if args[0] == "predict" {
		id := backend.Identity()
		r, err := backend.Decide(ctx, d.DecisionRequest{Task: id.Tasks[0], SchemaID: id.Schemas[0]}, *input)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(r)
	}
	labels := []string{}
	if p, ok := backend.(interface{ Labels() []string }); ok {
		labels = p.Labels()
	}
	return json.NewEncoder(out).Encode(struct {
		Labels       []string          `json:"labels"`
		Identity     provider.Identity `json:"identity"`
		ArtifactHash chain.Hash        `json:"artifact_hash"`
	}{labels, backend.Identity(), artifactHash})
}
func readExamples(ctx context.Context, path string) ([]native.Example, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const budget = 64 << 20
	scanner := bufio.NewScanner(io.LimitReader(f, budget+1))
	scanner.Buffer(make([]byte, 4096), 128<<10)
	examples := []native.Example{}
	used := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := scanner.Bytes()
		used += len(line) + 1
		if used > budget {
			return nil, errors.New("training data exceeds 64 MiB")
		}
		var e native.Example
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			return nil, err
		}
		if dec.Decode(new(any)) != io.EOF {
			return nil, errors.New("trailing training data")
		}
		examples = append(examples, e)
		if len(examples) > 10000 {
			return nil, errors.New("CLI training set exceeds 10000 examples")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return examples, nil
}
func save(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ddn-model-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
