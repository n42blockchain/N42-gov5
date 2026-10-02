package jsonrpc

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestFormatName(t *testing.T) {
	if formatName("GetBalance") != "getBalance" {
		t.Errorf("got %q", formatName("GetBalance"))
	}
	if formatName("") != "" {
		t.Errorf("expected empty string")
	}
	if formatName("x") != "x" {
		t.Errorf("got %q", formatName("x"))
	}
}

func TestIsContextErrorSubscriptionType(t *testing.T) {
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	if !isContextType(ctxType) {
		t.Error("expected context type")
	}
	if isContextType(stringType) {
		t.Error("string should not be context type")
	}

	errT := reflect.TypeOf((*error)(nil)).Elem()
	if !isErrorType(errT) {
		t.Error("expected error type")
	}
	if isErrorType(stringType) {
		t.Error("string should not be error type")
	}

	subT := reflect.TypeOf(Subscription{})
	if !isSubscriptionType(subT) {
		t.Error("expected subscription type")
	}
	if isSubscriptionType(stringType) {
		t.Error("string should not be subscription type")
	}
}

type covTestService struct{}

func (covTestService) Add(a, b int) int { return a + b }

func (covTestService) Fail() error { return errors.New("fail") }

func (covTestService) WithCtx(ctx context.Context, x string) (string, error) {
	return x, nil
}

func TestServiceRegistryRegisterAndCallback(t *testing.T) {
	r := &serviceRegistry{}
	if err := r.registerName("test", covTestService{}); err != nil {
		t.Fatalf("registerName failed: %v", err)
	}

	cb := r.callback("test_add")
	if cb == nil {
		t.Fatal("expected callback for test_add")
	}
	if len(cb.argTypes) != 2 {
		t.Errorf("argTypes = %v", cb.argTypes)
	}

	res, err := cb.call(context.Background(), "test_add", []reflect.Value{
		reflect.ValueOf(2), reflect.ValueOf(3),
	})
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if res.(int) != 5 {
		t.Errorf("got %v want 5", res)
	}

	cbCtx := r.callback("test_withCtx")
	if cbCtx == nil || !cbCtx.hasCtx {
		t.Fatalf("expected hasCtx callback, got %+v", cbCtx)
	}

	if r.callback("nomethod") != nil {
		t.Error("expected nil for malformed method name")
	}
	if r.callback("unknown_service") != nil {
		t.Error("expected nil for unknown service")
	}

	// registering with empty name fails
	if err := r.registerName("", covTestService{}); err == nil {
		t.Error("expected error for empty service name")
	}
}

func TestCallbackRecoversPanic(t *testing.T) {
	r := &serviceRegistry{}
	// Use a receiver with a method that panics.
	if err := r.registerName("panicsvc", panicker{}); err != nil {
		t.Fatalf("registerName failed: %v", err)
	}
	cb := r.callback("panicsvc_boom")
	if cb == nil {
		t.Fatal("expected callback")
	}
	_, err := cb.call(context.Background(), "panicsvc_boom", nil)
	if err == nil {
		t.Error("expected error from recovered panic")
	}
}

type panicker struct{}

func (panicker) Boom() int { panic("boom") }
