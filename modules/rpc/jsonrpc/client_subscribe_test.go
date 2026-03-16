package jsonrpc

import (
	"context"
	"errors"
	"testing"
)

func TestClientSubscribeRejectsInvalidChannel(t *testing.T) {
	client := &Client{}

	_, err := client.Subscribe(context.Background(), "eth", 123, "newHeads")
	if !errors.Is(err, ErrInvalidSubscribeChannel) {
		t.Fatalf("Subscribe() error = %v, want %v", err, ErrInvalidSubscribeChannel)
	}
}

func TestClientSubscribeRejectsNilChannel(t *testing.T) {
	client := &Client{}
	var ch chan int

	_, err := client.Subscribe(context.Background(), "eth", ch, "newHeads")
	if !errors.Is(err, ErrNilSubscribeChannel) {
		t.Fatalf("Subscribe() error = %v, want %v", err, ErrNilSubscribeChannel)
	}
}
