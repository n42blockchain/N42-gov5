package metrics

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type Counter interface {
	prometheus.Counter
	ValueGetter
	AddInt(v int)
	AddUint64(v uint64)
}

type counter struct {
	prometheus.Counter
}

func (c *counter) GetValue() float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		panic(fmt.Errorf("calling GetValue with invalid metric: %w", err))
	}
	return m.GetCounter().GetValue()
}

func (c *counter) GetValueUint64() uint64 {
	return uint64(c.GetValue())
}

// AddInt adds an int value (cast to float64). Safe for values up to 2^53.
func (c *counter) AddInt(v int) { c.Add(float64(v)) }

// AddUint64 adds a uint64 value (cast to float64). Safe for values up to 2^53.
func (c *counter) AddUint64(v uint64) { c.Add(float64(v)) }
