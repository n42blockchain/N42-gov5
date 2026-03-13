// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

// Package core provides core abstractions and utilities for the N42 blockchain.
// This includes the service container for lifecycle management and dependency wiring.
package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ServiceStatus represents the lifecycle state of a service.
type ServiceStatus int

const (
	StatusStopped ServiceStatus = iota
	StatusStarting
	StatusRunning
	StatusStopping
	StatusFailed
)

// String returns the string representation of the service status.
func (s ServiceStatus) String() string {
	switch s {
	case StatusStopped:
		return "stopped"
	case StatusStarting:
		return "starting"
	case StatusRunning:
		return "running"
	case StatusStopping:
		return "stopping"
	case StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Common service errors
var (
	ErrServiceAlreadyRegistered = errors.New("service already registered")
	ErrServiceNotFound          = errors.New("service not found")
	ErrServiceStartFailed       = errors.New("service start failed")
	ErrServiceStopFailed        = errors.New("service stop failed")
	ErrContainerNotRunning      = errors.New("container not running")
	ErrContainerAlreadyRunning  = errors.New("container already running")
)

// Service defines the interface that all managed services must implement.
type Service interface {
	// Name returns the unique identifier for this service.
	Name() string

	// Start initializes and starts the service.
	// The context can be used for cancellation and timeout.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the service.
	// The context can be used for cancellation and timeout.
	Stop(ctx context.Context) error

	// Status returns the current status of the service.
	Status() ServiceStatus
}

// ServiceInfo contains runtime information about a service.
type ServiceInfo struct {
	Name      string
	Status    ServiceStatus
	StartTime time.Time
	StopTime  time.Time
	Error     error
}

// ServiceContainer manages the lifecycle of multiple services.
// It ensures services are started in registration order and stopped in reverse order.
type ServiceContainer struct {
	mu       sync.RWMutex
	services map[string]Service
	order    []string
	running  bool
	ctx      context.Context
	cancel   context.CancelFunc
}

// NewServiceContainer creates a new service container.
func NewServiceContainer() *ServiceContainer {
	return &ServiceContainer{
		services: make(map[string]Service),
		order:    make([]string, 0),
	}
}

// Register adds a service to the container.
// Services must be registered before the container is started.
func (c *ServiceContainer) Register(s Service) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return ErrContainerAlreadyRunning
	}

	name := s.Name()
	if _, exists := c.services[name]; exists {
		return fmt.Errorf("%w: %s", ErrServiceAlreadyRegistered, name)
	}

	c.services[name] = s
	c.order = append(c.order, name)
	return nil
}

// Get retrieves a service by name.
func (c *ServiceContainer) Get(name string) (Service, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	s, ok := c.services[name]
	return s, ok
}

// StartAll starts all registered services in registration order.
// If any service fails to start, previously started services are stopped.
func (c *ServiceContainer) StartAll(ctx context.Context) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return ErrContainerAlreadyRunning
	}
	c.running = true
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.mu.Unlock()

	var started []string
	for _, name := range c.order {
		s := c.services[name]
		if err := s.Start(c.ctx); err != nil {
			// Rollback: stop already started services in reverse order
			c.stopServicesReverse(started)
			c.mu.Lock()
			c.running = false
			c.mu.Unlock()
			return fmt.Errorf("%w: %s: %v", ErrServiceStartFailed, name, err)
		}
		started = append(started, name)
	}

	return nil
}

// StopAll stops all running services in reverse registration order.
func (c *ServiceContainer) StopAll(ctx context.Context) error {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	c.cancel()
	c.mu.Unlock()

	// Snapshot the service order for safe iteration
	names := make([]string, len(c.order))
	copy(names, c.order)

	errs := c.stopServicesReverse(names)

	c.mu.Lock()
	c.running = false
	c.mu.Unlock()

	if len(errs) > 0 {
		return fmt.Errorf("%w: %v", ErrServiceStopFailed, errs)
	}
	return nil
}

// stopServicesReverse stops services in reverse order of the given slice.
func (c *ServiceContainer) stopServicesReverse(names []string) []error {
	var errs []error
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for i := len(names) - 1; i >= 0; i-- {
		name := names[i]
		if s, ok := c.services[name]; ok {
			if err := s.Stop(stopCtx); err != nil {
				errs = append(errs, fmt.Errorf("%s: %v", name, err))
			}
		}
	}
	return errs
}

// IsRunning returns whether the container is currently running.
func (c *ServiceContainer) IsRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.running
}

// ServiceCount returns the number of registered services.
func (c *ServiceContainer) ServiceCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.services)
}

// Services returns information about all registered services.
func (c *ServiceContainer) Services() []ServiceInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	infos := make([]ServiceInfo, 0, len(c.order))
	for _, name := range c.order {
		s := c.services[name]
		infos = append(infos, ServiceInfo{
			Name:   name,
			Status: s.Status(),
		})
	}
	return infos
}
