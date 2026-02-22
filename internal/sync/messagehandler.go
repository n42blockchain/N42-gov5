package sync

import (
	"context"
	"fmt"
	"runtime/debug"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"go.opencensus.io/trace"

	"github.com/n42blockchain/N42/log"
)

const noMsgData = "message contains no data"

// safelyHandleMessage will recover and log any panic that occurs from the
// function argument.
func (s *Service) safelyHandleMessage(ctx context.Context, fn func(ctx context.Context, message *pubsub.Message) error, msg *pubsub.Message) {
	defer s.handlePanic(ctx, msg)

	if err := fn(ctx, msg); err != nil {
		if span := trace.FromContext(ctx); span != nil {
			span.SetStatus(trace.Status{
				Code:    trace.StatusCodeInternal,
				Message: err.Error(),
			})
		}
	}
}

// handlePanic recovers from a panic during message handling and logs the event.
func (s *Service) handlePanic(ctx context.Context, msg *pubsub.Message) {
	r := recover()
	if r == nil {
		return
	}

	printedMsg := noMsgData
	if msg != nil {
		printedMsg = msg.String()
	}
	log.Error("Panicked when handling p2p message! Recovering...", "r", r, "msg", printedMsg)
	debug.PrintStack()

	if ctx == nil {
		return
	}
	if span := trace.FromContext(ctx); span != nil {
		span.SetStatus(trace.Status{
			Code:    trace.StatusCodeInternal,
			Message: fmt.Sprintf("Panic: %v", r),
		})
	}
}
