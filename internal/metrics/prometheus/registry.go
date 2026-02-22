package prometheus

import (
	"fmt"
	"reflect"
	"sync"

	metrics2 "github.com/VictoriaMetrics/metrics"

	"github.com/n42blockchain/N42/log"
)

// DuplicateMetric is the error returned by Registry.Register when a metric
// already exists. If you mean to Register that metric you must first
// Unregister the existing metric.
type DuplicateMetric string

func (err DuplicateMetric) Error() string {
	return fmt.Sprintf("duplicate metric: %s", string(err))
}

// Registry holds references to a set of metrics by name and can iterate
// over them, calling callback functions provided by the user.
type Registry interface {
	// Each calls the given function for each registered metric.
	Each(func(string, interface{}))

	// Get returns the metric by the given name or nil if none is registered.
	Get(string) interface{}

	// GetOrRegister returns an existing metric or registers the given one.
	// The interface can be the metric to register if not found in registry,
	// or a function returning the metric for lazy instantiation.
	GetOrRegister(string, interface{}) interface{}

	// Register adds the given metric under the given name.
	Register(string, interface{}) error

	// Unregister removes the metric with the given name.
	Unregister(string)

	// UnregisterAll removes all metrics. (Mostly for testing.)
	UnregisterAll()
}

// StandardRegistry is the standard implementation of a Registry,
// backed by a mutex-protected map of names to metrics.
type StandardRegistry struct {
	metrics map[string]interface{}
	mutex   sync.Mutex
}

// NewRegistry creates a new empty Registry.
func NewRegistry() Registry {
	return &StandardRegistry{metrics: make(map[string]interface{})}
}

func (r *StandardRegistry) Each(f func(string, interface{})) {
	for name, i := range r.registered() {
		f(name, i)
	}
}

func (r *StandardRegistry) Get(name string) interface{} {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.metrics[name]
}

func (r *StandardRegistry) GetOrRegister(name string, i interface{}) interface{} {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	if metric, ok := r.metrics[name]; ok {
		return metric
	}
	if v := reflect.ValueOf(i); v.Kind() == reflect.Func {
		i = v.Call(nil)[0].Interface()
	}
	r.register(name, i)
	return i
}

func (r *StandardRegistry) Register(name string, i interface{}) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	return r.register(name, i)
}

func (r *StandardRegistry) Unregister(name string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.stop(name)
	delete(r.metrics, name)
}

func (r *StandardRegistry) UnregisterAll() {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	for name := range r.metrics {
		r.stop(name)
		delete(r.metrics, name)
	}
}

func (r *StandardRegistry) register(name string, i interface{}) error {
	if _, ok := r.metrics[name]; ok {
		return DuplicateMetric(name)
	}
	switch i.(type) {
	case *metrics2.Counter, *metrics2.Gauge, *metrics2.FloatCounter, *metrics2.Histogram, *metrics2.Summary:
		r.metrics[name] = i
	default:
		log.Info("Type not registered (metrics won't show)", "type", reflect.TypeOf(i))
	}
	return nil
}

// registered returns a snapshot copy of all registered metrics.
func (r *StandardRegistry) registered() map[string]interface{} {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	snapshot := make(map[string]interface{}, len(r.metrics))
	for name, i := range r.metrics {
		snapshot[name] = i
	}
	return snapshot
}

func (r *StandardRegistry) stop(name string) {
	if i, ok := r.metrics[name]; ok {
		if s, ok := i.(Stoppable); ok {
			s.Stop()
		}
	}
}

// Stoppable defines metrics that must be stopped when unregistered.
type Stoppable interface {
	Stop()
}

// Package-level registries.
var (
	DefaultRegistry    = NewRegistry()
	EphemeralRegistry  = NewRegistry()
	AccountingRegistry = NewRegistry()
)

// Get returns the metric by the given name from the DefaultRegistry,
// or nil if none is registered.
func Get(name string) interface{} {
	return DefaultRegistry.Get(name)
}
