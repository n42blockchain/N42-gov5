// Package transformer implements a trainable encoder and reverse-mode
// differentiation entirely in Go. It requires no external inference runtime.
package transformer

import (
	"context"
	"math"
)

// tensor is local to one forward/backward call; model weights are copied into
// leaves, so concurrent inference never shares mutable graph state.
type tensor struct {
	ctx        context.Context
	v, g       []float64
	rows, cols int
	parents    []*tensor
	backward   func()
}

func leaf(v []float64, r, c int) *tensor {
	return &tensor{v: append([]float64{}, v...), g: make([]float64, len(v)), rows: r, cols: c}
}
func node(r, c int, p ...*tensor) *tensor {
	var ctx context.Context
	if len(p) > 0 {
		ctx = p[0].ctx
	}
	return &tensor{v: make([]float64, r*c), g: make([]float64, r*c), rows: r, cols: c, parents: p, ctx: ctx}
}
func cancelled(t *tensor) bool { return t.ctx != nil && t.ctx.Err() != nil }

func back(root *tensor) {
	seen := map[*tensor]bool{}
	var order []*tensor
	var visit func(*tensor)
	visit = func(t *tensor) {
		if seen[t] {
			return
		}
		seen[t] = true
		for _, p := range t.parents {
			visit(p)
		}
		order = append(order, t)
	}
	visit(root)
	root.g[0] = 1
	for i := len(order) - 1; i >= 0; i-- {
		if cancelled(order[i]) {
			return
		}
		if order[i].backward != nil {
			order[i].backward()
		}
	}
}
func matmul(a, b *tensor) *tensor {
	o := node(a.rows, b.cols, a, b)
	for i := 0; i < a.rows; i++ {
		if cancelled(a) {
			return o
		}
		for k := 0; k < a.cols; k++ {
			v := a.v[i*a.cols+k]
			for j := 0; j < b.cols; j++ {
				o.v[i*b.cols+j] += v * b.v[k*b.cols+j]
			}
		}
	}
	o.backward = func() {
		for i := 0; i < a.rows; i++ {
			for k := 0; k < a.cols; k++ {
				for j := 0; j < b.cols; j++ {
					g := o.g[i*b.cols+j]
					a.g[i*a.cols+k] += g * b.v[k*b.cols+j]
					b.g[k*b.cols+j] += g * a.v[i*a.cols+k]
				}
			}
		}
	}
	return o
}
func add(a, b *tensor) *tensor {
	o := node(a.rows, a.cols, a, b)
	broadcast := b.rows == 1
	for i := range o.v {
		j := i
		if broadcast {
			j = i % a.cols
		}
		o.v[i] = a.v[i] + b.v[j]
	}
	o.backward = func() {
		for i, g := range o.g {
			j := i
			if broadcast {
				j = i % a.cols
			}
			a.g[i] += g
			b.g[j] += g
		}
	}
	return o
}
func gelu(a *tensor) *tensor {
	o := node(a.rows, a.cols, a)
	const k = 0.7978845608028654
	for i, x := range a.v {
		if i%64 == 0 && cancelled(a) {
			return o
		}
		o.v[i] = 0.5 * x * (1 + math.Tanh(k*(x+0.044715*x*x*x)))
	}
	o.backward = func() {
		for i, x := range a.v {
			t := math.Tanh(k * (x + 0.044715*x*x*x))
			a.g[i] += o.g[i] * (0.5*(1+t) + 0.5*x*(1-t*t)*k*(1+3*0.044715*x*x))
		}
	}
	return o
}
func norm(a, scale, bias *tensor) *tensor {
	o := node(a.rows, a.cols, a, scale, bias)
	z := make([]float64, len(a.v))
	inv := make([]float64, a.rows)
	for r := 0; r < a.rows; r++ {
		if cancelled(a) {
			return o
		}
		var mean, variance float64
		for j := 0; j < a.cols; j++ {
			mean += a.v[r*a.cols+j]
		}
		mean /= float64(a.cols)
		for j := 0; j < a.cols; j++ {
			x := a.v[r*a.cols+j] - mean
			variance += x * x
		}
		inv[r] = 1 / math.Sqrt(variance/float64(a.cols)+1e-5)
		for j := 0; j < a.cols; j++ {
			i := r*a.cols + j
			z[i] = (a.v[i] - mean) * inv[r]
			o.v[i] = z[i]*scale.v[j] + bias.v[j]
		}
	}
	o.backward = func() {
		for r := 0; r < a.rows; r++ {
			var sum, sumZ float64
			for j := 0; j < a.cols; j++ {
				i := r*a.cols + j
				g := o.g[i] * scale.v[j]
				sum += g
				sumZ += g * z[i]
				scale.g[j] += o.g[i] * z[i]
				bias.g[j] += o.g[i]
			}
			for j := 0; j < a.cols; j++ {
				i := r*a.cols + j
				a.g[i] += inv[r] * (o.g[i]*scale.v[j] - sum/float64(a.cols) - z[i]*sumZ/float64(a.cols))
			}
		}
	}
	return o
}

// attention computes scaled dot-product attention over all unpadded tokens.
func attention(q, k, v *tensor, heads int) *tensor {
	n, d := q.rows, q.cols
	hd := d / heads
	scale := 1 / math.Sqrt(float64(hd))
	o := node(n, d, q, k, v)
	p := make([]float64, heads*n*n)
	for h := 0; h < heads; h++ {
		for i := 0; i < n; i++ {
			if cancelled(q) {
				return o
			}
			max := math.Inf(-1)
			for j := 0; j < n; j++ {
				var score float64
				for x := 0; x < hd; x++ {
					score += q.v[i*d+h*hd+x] * k.v[j*d+h*hd+x]
				}
				score *= scale
				p[(h*n+i)*n+j] = score
				if score > max {
					max = score
				}
			}
			var sum float64
			for j := 0; j < n; j++ {
				idx := (h*n+i)*n + j
				p[idx] = math.Exp(p[idx] - max)
				sum += p[idx]
			}
			for j := 0; j < n; j++ {
				idx := (h*n+i)*n + j
				p[idx] /= sum
				for x := 0; x < hd; x++ {
					o.v[i*d+h*hd+x] += p[idx] * v.v[j*d+h*hd+x]
				}
			}
		}
	}
	o.backward = func() {
		for h := 0; h < heads; h++ {
			for i := 0; i < n; i++ {
				dp := make([]float64, n)
				var dot float64
				for j := 0; j < n; j++ {
					idx := (h*n+i)*n + j
					for x := 0; x < hd; x++ {
						g := o.g[i*d+h*hd+x]
						dp[j] += g * v.v[j*d+h*hd+x]
						v.g[j*d+h*hd+x] += p[idx] * g
					}
					dot += dp[j] * p[idx]
				}
				for j := 0; j < n; j++ {
					ds := p[(h*n+i)*n+j] * (dp[j] - dot) * scale
					for x := 0; x < hd; x++ {
						q.g[i*d+h*hd+x] += ds * k.v[j*d+h*hd+x]
						k.g[j*d+h*hd+x] += ds * q.v[i*d+h*hd+x]
					}
				}
			}
		}
	}
	return o
}
func pool(a *tensor) *tensor {
	o := node(1, a.cols, a)
	for i, x := range a.v {
		o.v[i%a.cols] += x / float64(a.rows)
	}
	o.backward = func() {
		for i := range a.g {
			a.g[i] += o.g[i%a.cols] / float64(a.rows)
		}
	}
	return o
}
func crossEntropy(logits *tensor, target int) *tensor {
	o := node(1, 1, logits)
	max := logits.v[0]
	for _, x := range logits.v {
		if x > max {
			max = x
		}
	}
	p := make([]float64, len(logits.v))
	var sum float64
	for i, x := range logits.v {
		p[i] = math.Exp(x - max)
		sum += p[i]
	}
	o.v[0] = math.Log(sum) + max - logits.v[target]
	o.backward = func() {
		for i := range p {
			g := p[i] / sum
			if i == target {
				g--
			}
			logits.g[i] += o.g[0] * g
		}
	}
	return o
}
