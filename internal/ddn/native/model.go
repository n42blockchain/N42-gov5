// Package native implements DDN classification and training in Go without an
// external inference runtime. Model probabilities are estimates, not proofs.
package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

const (
	MaxModelBytes = 16 << 20
	MaxInputBytes = 65536
	MaxVocabulary = 8192
	MaxLabels     = 32
	MaxExamples   = 100000
	MaxCount      = 1000000000
)

// Artifact contains integer sufficient statistics for multinomial naive Bayes.
// Ordered labels/vocabulary and the tokenizer version are part of its identity.
type Artifact struct {
	Format           uint32     `json:"format"`
	Algorithm        string     `json:"algorithm"`
	Name             string     `json:"name"`
	Version          string     `json:"version"`
	Task             string     `json:"task"`
	Schema           string     `json:"schema"`
	MinConfidencePPM uint32     `json:"min_confidence_ppm"`
	MinCoveragePPM   uint32     `json:"min_coverage_ppm"`
	Labels           []string   `json:"labels"`
	Vocabulary       []string   `json:"vocabulary"`
	Documents        []uint64   `json:"documents"`
	Counts           [][]uint64 `json:"counts"`
}
type Example struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

func tokens(input string) ([]string, error) {
	if len(input) > MaxInputBytes || !utf8.ValidString(input) {
		return nil, errors.New("invalid native input")
	}
	return strings.FieldsFunc(strings.ToLower(input), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }), nil
}
func (a Artifact) Validate() error {
	if a.Format != 1 || a.Algorithm != "multinomial-nb-unicode-v1" || a.MinConfidencePPM > d.PPM || a.MinCoveragePPM == 0 || a.MinCoveragePPM > d.PPM {
		return errors.New("unsupported native model or thresholds")
	}
	for _, s := range []string{a.Name, a.Version, a.Task, a.Schema} {
		if s == "" || len(s) > 128 || !utf8.ValidString(s) {
			return errors.New("invalid model metadata")
		}
	}
	if len(a.Labels) < 2 || len(a.Labels) > MaxLabels || len(a.Vocabulary) < 1 || len(a.Vocabulary) > MaxVocabulary || len(a.Documents) != len(a.Labels) || len(a.Counts) != len(a.Labels) {
		return errors.New("invalid model dimensions")
	}
	for _, ss := range [][]string{a.Labels, a.Vocabulary} {
		for i, s := range ss {
			if s == "" || len(s) > 128 || !utf8.ValidString(s) || (i > 0 && ss[i-1] >= s) {
				return errors.New("model strings must be sorted and unique")
			}
		}
	}
	for _, w := range a.Vocabulary {
		ts, _ := tokens(w)
		if len(ts) != 1 || ts[0] != w {
			return errors.New("vocabulary does not match tokenizer")
		}
	}
	var docs uint64
	for i, n := range a.Documents {
		if n == 0 || n > MaxExamples || len(a.Counts[i]) != len(a.Vocabulary) {
			return errors.New("invalid model statistics")
		}
		docs += n
		var total uint64
		for _, c := range a.Counts[i] {
			if c > MaxCount {
				return errors.New("model count exceeds bound")
			}
			total += c
		}
		if total == 0 || total > MaxCount {
			return errors.New("invalid token total")
		}
	}
	if docs > MaxExamples {
		return errors.New("too many training documents")
	}
	return nil
}
func (a Artifact) Bytes() ([]byte, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(a)
}
func (a Artifact) Hash() (chain.Hash, error) {
	b, err := a.Bytes()
	if err != nil {
		return chain.Hash{}, err
	}
	return crypto.Keccak256Hash(b), nil
}
func Decode(r io.Reader) (Artifact, error) {
	var a Artifact
	b, err := io.ReadAll(io.LimitReader(r, MaxModelBytes+1))
	if err != nil || len(b) > MaxModelBytes {
		return a, errors.New("native model too large or unreadable")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&a); err != nil {
		return a, err
	}
	if dec.Decode(new(any)) != io.EOF {
		return a, errors.New("trailing model data")
	}
	return a, a.Validate()
}
func Load(path string) (Artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	return Decode(f)
}

// Train fits Laplace-smoothed token statistics; no pretrained weights are fetched.
func Train(ctx context.Context, name, version, task, schema string, examples []Example, confidence, coverage uint32) (Artifact, error) {
	a := Artifact{Format: 1, Algorithm: "multinomial-nb-unicode-v1", Name: name, Version: version, Task: task, Schema: schema, MinConfidencePPM: confidence, MinCoveragePPM: coverage}
	if len(examples) == 0 || len(examples) > MaxExamples {
		return a, errors.New("invalid training set size")
	}
	labels := map[string]bool{}
	vocab := map[string]bool{}
	for _, e := range examples {
		if err := ctx.Err(); err != nil {
			return a, err
		}
		if e.Label == "" || len(e.Label) > 128 {
			return a, errors.New("invalid training label")
		}
		labels[e.Label] = true
		ts, err := tokens(e.Text)
		if err != nil || len(ts) == 0 {
			return a, errors.New("empty or invalid training text")
		}
		for _, t := range ts {
			if len(t) > 128 {
				return a, errors.New("training token too long")
			}
			vocab[t] = true
		}
		if len(labels) > MaxLabels || len(vocab) > MaxVocabulary {
			return a, errors.New("training dimensions exceed bounds")
		}
	}
	for l := range labels {
		a.Labels = append(a.Labels, l)
	}
	for w := range vocab {
		a.Vocabulary = append(a.Vocabulary, w)
	}
	sort.Strings(a.Labels)
	sort.Strings(a.Vocabulary)
	a.Documents = make([]uint64, len(a.Labels))
	a.Counts = make([][]uint64, len(a.Labels))
	for i := range a.Counts {
		a.Counts[i] = make([]uint64, len(a.Vocabulary))
	}
	for _, e := range examples {
		if err := ctx.Err(); err != nil {
			return a, err
		}
		i := sort.SearchStrings(a.Labels, e.Label)
		a.Documents[i]++
		ts, _ := tokens(e.Text)
		for _, t := range ts {
			a.Counts[i][sort.SearchStrings(a.Vocabulary, t)]++
		}
	}
	return a, a.Validate()
}

type Classifier struct {
	artifact      Artifact
	hash          chain.Hash
	words         map[string]int
	logPrior      []float64
	logLikelihood [][]float64
}

func New(a Artifact) (*Classifier, error) {
	b, err := a.Bytes()
	if err != nil {
		return nil, err
	}
	// Deep copy: callers cannot mutate a running model or its pinned identity.
	var owned Artifact
	if err = json.Unmarshal(b, &owned); err != nil {
		return nil, err
	}
	c := &Classifier{artifact: owned, hash: crypto.Keccak256Hash(b), words: map[string]int{}, logPrior: make([]float64, len(a.Labels)), logLikelihood: make([][]float64, len(a.Labels))}
	for i, w := range a.Vocabulary {
		c.words[w] = i
	}
	var total uint64
	for _, n := range a.Documents {
		total += n
	}
	for i, n := range a.Documents {
		c.logPrior[i] = math.Log(float64(n+1) / float64(total+uint64(len(a.Labels))))
		var sum uint64
		for _, v := range a.Counts[i] {
			sum += v
		}
		c.logLikelihood[i] = make([]float64, len(a.Vocabulary))
		for j, v := range a.Counts[i] {
			c.logLikelihood[i][j] = math.Log(float64(v+1) / float64(sum+uint64(len(a.Vocabulary))))
		}
	}
	return c, nil
}
func (c *Classifier) Hash() chain.Hash { return c.hash }
func (c *Classifier) Metadata() (name, version, task, schema string) {
	a := c.artifact
	return a.Name, a.Version, a.Task, a.Schema
}
func (c *Classifier) Labels() []string { return append([]string{}, c.artifact.Labels...) }
func unknown() d.DecisionResult {
	return d.DecisionResult{Label: "UNKNOWN", NeedEscalation: true, ProbabilitiesPPM: []uint32{}, Answers: []d.QuantizedAnswer{}}
}
func (c *Classifier) Predict(ctx context.Context, input string) (d.DecisionResult, error) {
	if err := ctx.Err(); err != nil {
		return d.DecisionResult{}, err
	}
	ts, err := tokens(input)
	if err != nil {
		return d.DecisionResult{}, err
	}
	if len(ts) == 0 {
		return unknown(), nil
	}
	scores := append([]float64{}, c.logPrior...)
	known := 0
	for k, t := range ts {
		if k%64 == 0 {
			if err := ctx.Err(); err != nil {
				return d.DecisionResult{}, err
			}
		}
		j, ok := c.words[t]
		if !ok {
			continue
		}
		known++
		for i := range scores {
			scores[i] += c.logLikelihood[i][j]
		}
	}
	if uint64(known)*d.PPM < uint64(len(ts))*uint64(c.artifact.MinCoveragePPM) {
		return unknown(), nil
	}
	best := 0
	for i := range scores {
		if scores[i] > scores[best] {
			best = i
		}
	}
	max := scores[best]
	var sum float64
	for i := range scores {
		scores[i] = math.Exp(scores[i] - max)
		sum += scores[i]
	}
	ppm := make([]uint32, len(scores))
	var allocated uint32
	for i, s := range scores {
		ppm[i] = uint32(math.Floor(s / sum * d.PPM))
		allocated += ppm[i]
	}
	ppm[best] += d.PPM - allocated
	r := d.DecisionResult{Label: c.artifact.Labels[best], ProbabilitiesPPM: ppm, ConfidencePPM: ppm[best], Answers: []d.QuantizedAnswer{}}
	r.Answers = []d.QuantizedAnswer{{Kind: 1, Selected: uint8(best), ConfidencePPM: r.ConfidencePPM, ProbabilitiesPPM: append([]uint32{}, r.ProbabilitiesPPM...)}}
	if r.ConfidencePPM < c.artifact.MinConfidencePPM {
		r.Label = "ABSTAIN"
		r.NeedEscalation = true
	}
	return r, r.Validate()
}
