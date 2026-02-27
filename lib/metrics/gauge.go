package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type Gauge interface {
	prometheus.Gauge
	ValueGetter
	SetUint32(v uint32)
	SetUint64(v uint64)
	SetInt(v int)
}

type gauge struct {
	prometheus.Gauge
}

func (g *gauge) GetValue() float64 {
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		panic(fmt.Errorf("calling GetValue with invalid metric: %w", err))
	}
	return m.GetGauge().GetValue()
}

func (g *gauge) GetValueUint64() uint64 {
	return uint64(g.GetValue())
}

// SetUint32 sets the gauge from a uint32 (cast to float64 internally).
func (g *gauge) SetUint32(v uint32) { g.Set(float64(v)) }

// SetUint64 sets the gauge from a uint64 (cast to float64 internally).
// Safe for values up to 2^53.
func (g *gauge) SetUint64(v uint64) { g.Set(float64(v)) }

// SetInt sets the gauge from an int (cast to float64 internally).
// Safe for values up to 2^53.
func (g *gauge) SetInt(v int) { g.Set(float64(v)) }
