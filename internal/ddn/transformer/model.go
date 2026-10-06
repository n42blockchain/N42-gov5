package transformer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/rand"
	"os"
	"sort"
	"unicode/utf8"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

const MaxModelBytes = 32 << 20
const maxParameters = 1000000

// Config describes a bidirectional pre-norm Transformer encoder. UTF-8 bytes
// are token IDs 0..255, with a leading CLS token 256. There is no remote tokenizer.
type Config struct {
	Hidden      int `json:"hidden"`
	Heads       int `json:"heads"`
	Layers      int `json:"layers"`
	FeedForward int `json:"feed_forward"`
	MaxSequence int `json:"max_sequence"`
}
type Parameter struct {
	Name   string    `json:"name"`
	Rows   int       `json:"rows"`
	Cols   int       `json:"cols"`
	Values []float64 `json:"values"`
}
type Artifact struct {
	Format           uint32      `json:"format"`
	Algorithm        string      `json:"algorithm"`
	Name             string      `json:"name"`
	Version          string      `json:"version"`
	Task             string      `json:"task"`
	Schema           string      `json:"schema"`
	MinConfidencePPM uint32      `json:"min_confidence_ppm"`
	Config           Config      `json:"config"`
	Labels           []string    `json:"labels"`
	Parameters       []Parameter `json:"parameters"`
}

func shapes(c Config, labels int) []Parameter {
	out := []Parameter{{Name: "token", Rows: 257, Cols: c.Hidden}, {Name: "position", Rows: c.MaxSequence, Cols: c.Hidden}}
	for l := 0; l < c.Layers; l++ {
		prefix := layerName(l)
		for _, n := range []string{"norm1_scale", "norm1_bias", "norm2_scale", "norm2_bias"} {
			out = append(out, Parameter{Name: prefix + n, Rows: 1, Cols: c.Hidden})
		}
		for _, n := range []string{"query", "key", "value", "output"} {
			out = append(out, Parameter{Name: prefix + n, Rows: c.Hidden, Cols: c.Hidden})
		}
		out = append(out, Parameter{Name: prefix + "ff1", Rows: c.Hidden, Cols: c.FeedForward}, Parameter{Name: prefix + "ff1_bias", Rows: 1, Cols: c.FeedForward}, Parameter{Name: prefix + "ff2", Rows: c.FeedForward, Cols: c.Hidden}, Parameter{Name: prefix + "ff2_bias", Rows: 1, Cols: c.Hidden})
	}
	return append(out, Parameter{Name: "final_scale", Rows: 1, Cols: c.Hidden}, Parameter{Name: "final_bias", Rows: 1, Cols: c.Hidden}, Parameter{Name: "classifier", Rows: c.Hidden, Cols: labels}, Parameter{Name: "classifier_bias", Rows: 1, Cols: labels})
}
func layerName(i int) string { return "layer" + string(rune('0'+i)) + "_" }
func (c Config) validate() error {
	if c.Hidden < 4 || c.Hidden > 128 || c.Heads < 1 || c.Heads > 8 || c.Hidden%c.Heads != 0 || c.Layers < 1 || c.Layers > 4 || c.FeedForward < c.Hidden || c.FeedForward > 512 || c.MaxSequence < 2 || c.MaxSequence > 256 {
		return errors.New("invalid bounded transformer configuration")
	}
	return nil
}
func (a Artifact) Validate() error {
	if a.Format != 1 || a.Algorithm != "n42-byte-transformer-v1" || a.MinConfidencePPM > d.PPM {
		return errors.New("unsupported transformer artifact")
	}
	if err := a.Config.validate(); err != nil {
		return err
	}
	for _, s := range []string{a.Name, a.Version, a.Task, a.Schema} {
		if s == "" || len(s) > 128 || !utf8.ValidString(s) {
			return errors.New("invalid transformer metadata")
		}
	}
	if len(a.Labels) < 2 || len(a.Labels) > 32 {
		return errors.New("invalid label count")
	}
	for i, s := range a.Labels {
		if s == "" || len(s) > 128 || !utf8.ValidString(s) || (i > 0 && a.Labels[i-1] >= s) {
			return errors.New("labels must be sorted and unique")
		}
	}
	expected := shapes(a.Config, len(a.Labels))
	if len(expected) != len(a.Parameters) {
		return errors.New("missing transformer parameters")
	}
	count := 0
	for i, p := range a.Parameters {
		e := expected[i]
		if p.Name != e.Name || p.Rows != e.Rows || p.Cols != e.Cols || len(p.Values) != e.Rows*e.Cols {
			return errors.New("invalid transformer parameter shape")
		}
		count += len(p.Values)
		for _, v := range p.Values {
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 10000 {
				return errors.New("invalid transformer parameter value")
			}
		}
	}
	if count > maxParameters {
		return errors.New("transformer parameter budget exceeded")
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
		return a, errors.New("transformer artifact exceeds bound")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&a); err != nil {
		return a, err
	}
	if dec.Decode(new(any)) != io.EOF {
		return a, errors.New("trailing transformer artifact")
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
func Initialize(name, version, task, schema string, labels []string, c Config, confidence uint32, seed int64) (Artifact, error) {
	a := Artifact{Format: 1, Algorithm: "n42-byte-transformer-v1", Name: name, Version: version, Task: task, Schema: schema, Config: c, Labels: append([]string{}, labels...), MinConfidencePPM: confidence}
	if err := c.validate(); err != nil {
		return a, err
	}
	sort.Strings(a.Labels)
	if len(a.Labels) < 2 || len(a.Labels) > 32 {
		return a, errors.New("invalid initialization label count")
	}
	for i, label := range a.Labels {
		if label == "" || len(label) > 128 || !utf8.ValidString(label) || (i > 0 && a.Labels[i-1] == label) {
			return a, errors.New("invalid initialization labels")
		}
	}
	a.Parameters = shapes(c, len(labels))
	rng := rand.New(rand.NewSource(seed))
	for i := range a.Parameters {
		p := &a.Parameters[i]
		p.Values = make([]float64, p.Rows*p.Cols)
		for j := range p.Values {
			if len(p.Name) >= 5 && p.Name[len(p.Name)-5:] == "scale" {
				p.Values[j] = 1
			} else if len(p.Name) >= 4 && p.Name[len(p.Name)-4:] == "bias" {
				p.Values[j] = 0
			} else {
				p.Values[j] = rng.NormFloat64() * math.Sqrt(2/float64(p.Rows+p.Cols))
			}
		}
	}
	return a, a.Validate()
}
func graph(ctx context.Context, a Artifact, input string) (*tensor, []*tensor, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if input == "" || !utf8.ValidString(input) || len(input)+1 > a.Config.MaxSequence {
		return nil, nil, errors.New("transformer input empty, invalid or exceeds sequence bound")
	}
	ps := make([]*tensor, len(a.Parameters))
	byName := map[string]*tensor{}
	for i, p := range a.Parameters {
		ps[i] = leaf(p.Values, p.Rows, p.Cols)
		ps[i].ctx = ctx
		byName[p.Name] = ps[i]
	}
	ids := make([]int, len(input)+1)
	ids[0] = 256
	for i, b := range []byte(input) {
		ids[i+1] = int(b)
	}
	embedding, position := byName["token"], byName["position"]
	h := node(len(ids), a.Config.Hidden, embedding, position)
	for i, id := range ids {
		for j := 0; j < h.cols; j++ {
			h.v[i*h.cols+j] = embedding.v[id*h.cols+j] + position.v[i*h.cols+j]
		}
	}
	initial := h
	initial.backward = func() {
		for i, id := range ids {
			for j := 0; j < initial.cols; j++ {
				g := initial.g[i*initial.cols+j]
				embedding.g[id*initial.cols+j] += g
				position.g[i*initial.cols+j] += g
			}
		}
	}
	for l := 0; l < a.Config.Layers; l++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		p := layerName(l)
		n := norm(h, byName[p+"norm1_scale"], byName[p+"norm1_bias"])
		q, k, v := matmul(n, byName[p+"query"]), matmul(n, byName[p+"key"]), matmul(n, byName[p+"value"])
		h = add(h, matmul(attention(q, k, v, a.Config.Heads), byName[p+"output"]))
		n = norm(h, byName[p+"norm2_scale"], byName[p+"norm2_bias"])
		ff := gelu(add(matmul(n, byName[p+"ff1"]), byName[p+"ff1_bias"]))
		h = add(h, add(matmul(ff, byName[p+"ff2"]), byName[p+"ff2_bias"]))
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	h = norm(h, byName["final_scale"], byName["final_bias"])
	logits := add(matmul(pool(h), byName["classifier"]), byName["classifier_bias"])
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return logits, ps, nil
}

type Model struct {
	artifact Artifact
	hash     chain.Hash
}

func New(a Artifact) (*Model, error) {
	b, err := a.Bytes()
	if err != nil {
		return nil, err
	}
	var owned Artifact
	if err = json.Unmarshal(b, &owned); err != nil {
		return nil, err
	}
	return &Model{owned, crypto.Keccak256Hash(b)}, nil
}
func (m *Model) Hash() chain.Hash { return m.hash }
func (m *Model) Metadata() (name, version, task, schema string) {
	a := m.artifact
	return a.Name, a.Version, a.Task, a.Schema
}
func (m *Model) Labels() []string { return append([]string{}, m.artifact.Labels...) }
func (m *Model) Predict(ctx context.Context, input string) (d.DecisionResult, error) {
	logits, _, err := graph(ctx, m.artifact, input)
	if err != nil {
		return d.DecisionResult{}, err
	}
	if err = ctx.Err(); err != nil {
		return d.DecisionResult{}, err
	}
	best := 0
	for i, x := range logits.v {
		if x > logits.v[best] {
			best = i
		}
	}
	max := logits.v[best]
	var sum float64
	for i, x := range logits.v {
		logits.v[i] = math.Exp(x - max)
		sum += logits.v[i]
	}
	p := make([]uint32, len(logits.v))
	var used uint32
	for i, x := range logits.v {
		p[i] = uint32(math.Floor(x / sum * d.PPM))
		used += p[i]
	}
	p[best] += d.PPM - used
	r := d.DecisionResult{Label: m.artifact.Labels[best], ConfidencePPM: p[best], ProbabilitiesPPM: p, Answers: []d.QuantizedAnswer{}}
	r.Answers = []d.QuantizedAnswer{{Kind: 1, Selected: uint8(best), ConfidencePPM: r.ConfidencePPM, ProbabilitiesPPM: append([]uint32{}, r.ProbabilitiesPPM...)}}
	if r.ConfidencePPM < m.artifact.MinConfidencePPM {
		r.Label = "ABSTAIN"
		r.NeedEscalation = true
	}
	return r, r.Validate()
}
