package prometheus

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/VictoriaMetrics/metrics"
)

// Prometheus text format templates.
var (
	typeGaugeTpl   = "\n# TYPE %s gauge\n"
	typeCounterTpl = "\n# TYPE %s counter\n"
	typeSummaryTpl = "\n# TYPE %s summary\n"

	keyValueTpl                      = "%s %v\n"
	keyQuantileTagValueTpl           = "%s {quantile=\"%s\"} %v\n"
	keyQuantileTagValueWithLabelsTpl = "%s,quantile=\"%s\"} %v\n"
)

// collector aggregates Prometheus-formatted metric reports into a byte buffer.
type collector struct {
	buff *bytes.Buffer
}

func newCollector() *collector {
	return &collector{
		buff: &bytes.Buffer{},
	}
}

func (c *collector) writeFloatCounter(name string, m *metrics.FloatCounter, withType bool) {
	c.writeCounter(name, m.Get(), withType)
}

func (c *collector) writeHistogram(name string, m *metrics.Histogram, withType bool) {
	c.writeTypeHeader(typeSummaryTpl, name, withType)
	c.writeSummarySum(name, fmt.Sprintf("%f", m.GetSum()))
	c.writeSummaryCounter(name, len(m.GetDecimalBuckets()))
}

func (c *collector) writeTimer(name string, m *metrics.Summary, withType bool) {
	pv := m.GetQuantiles()
	ps := m.GetQuantileValues()

	c.writeTypeHeader(typeSummaryTpl, name, withType)

	var sum float64
	for i := range pv {
		c.writeSummaryPercentile(name, strconv.FormatFloat(pv[i], 'f', -1, 64), ps[i])
		sum += ps[i]
	}

	c.writeSummaryTime(name, fmt.Sprintf("%f", m.GetTime().Seconds()))
	c.writeSummarySum(name, fmt.Sprintf("%f", sum))
	c.writeSummaryCounter(name, len(ps))
}

func (c *collector) writeGauge(name string, value interface{}, withType bool) {
	c.writeTypeHeader(typeGaugeTpl, name, withType)
	c.buff.WriteString(fmt.Sprintf(keyValueTpl, name, value))
}

func (c *collector) writeCounter(name string, value interface{}, withType bool) {
	c.writeTypeHeader(typeCounterTpl, name, withType)
	c.buff.WriteString(fmt.Sprintf(keyValueTpl, name, value))
}

// writeTypeHeader writes a TYPE annotation line when withType is true.
func (c *collector) writeTypeHeader(tpl, name string, withType bool) {
	if withType {
		c.buff.WriteString(fmt.Sprintf(tpl, stripLabels(name)))
	}
}

func stripLabels(name string) string {
	if idx := strings.IndexByte(name, '{'); idx >= 0 {
		return name[:idx]
	}
	return name
}

func splitLabels(name string) (string, string) {
	if idx := strings.IndexByte(name, '{'); idx >= 0 {
		return name[:idx], name[idx:]
	}
	return name, ""
}

func (c *collector) writeSummaryCounter(name string, value interface{}) {
	name, labels := splitLabels(name)
	c.buff.WriteString(fmt.Sprintf(keyValueTpl, name+"_count"+labels, value))
}

func (c *collector) writeSummaryPercentile(name, p string, value interface{}) {
	name, labels := splitLabels(name)
	if len(labels) > 0 {
		c.buff.WriteString(fmt.Sprintf(keyQuantileTagValueWithLabelsTpl, name+strings.TrimSuffix(labels, "}"), p, value))
	} else {
		c.buff.WriteString(fmt.Sprintf(keyQuantileTagValueTpl, name, p, value))
	}
}

func (c *collector) writeSummarySum(name string, value string) {
	name, labels := splitLabels(name)
	c.buff.WriteString(fmt.Sprintf(keyValueTpl, name+"_sum"+labels, value))
}

func (c *collector) writeSummaryTime(name string, value string) {
	name, labels := splitLabels(name)
	c.buff.WriteString(fmt.Sprintf(keyValueTpl, name+"_time"+labels, value))
}
