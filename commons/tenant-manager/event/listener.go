// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"

	libCommons "github.com/LerianStudio/lib-commons/v5/commons"
	libLog "github.com/LerianStudio/lib-observability/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/logcompat"
)

// EventHandler is a callback invoked for each parsed tenant lifecycle event.
// Returning an error logs the failure but does not stop the listener.
type EventHandler func(ctx context.Context, event TenantLifecycleEvent) error

// ListenerOption configures a TenantEventListener.
type ListenerOption func(*TenantEventListener)

// WithListenerLogger sets the logger for the listener.
// If logger is nil, a no-op logger is used.
func WithListenerLogger(logger libLog.Logger) ListenerOption {
	return func(l *TenantEventListener) {
		l.logger = logcompat.New(logger)
	}
}

// WithService sets the service name on the listener for logging context.
func WithService(name string) ListenerOption {
	return func(l *TenantEventListener) {
		l.service = name
	}
}

// TenantEventListener subscribes to Redis Pub/Sub channels matching the
// tenant-events pattern and dispatches parsed events to the registered handler.
// One goroutine per listener instance; this is a transport-layer component
// with no business logic.
type TenantEventListener struct {
	redisClient redis.UniversalClient
	handler     EventHandler
	logger      *logcompat.Logger
	service     string
	cancel      context.CancelFunc
	done        chan struct{}
}

// NewTenantEventListener creates a new TenantEventListener.
// Both redisClient and handler are required; returns an error if either is nil.
func NewTenantEventListener(
	redisClient redis.UniversalClient,
	handler EventHandler,
	opts ...ListenerOption,
) (*TenantEventListener, error) {
	if redisClient == nil {
		return nil, errors.New("event.NewTenantEventListener: redisClient must not be nil")
	}

	if handler == nil {
		return nil, errors.New("event.NewTenantEventListener: handler must not be nil")
	}

	l := &TenantEventListener{
		redisClient: redisClient,
		handler:     handler,
		logger:      logcompat.New(nil),
	}

	for _, opt := range opts {
		opt(l)
	}

	return l, nil
}

// Start subscribes to the tenant-events pattern and spawns a goroutine to
// read messages. Returns immediately after subscription is established.
// The goroutine runs until Stop is called or the parent context is cancelled.
func (l *TenantEventListener) Start(ctx context.Context) error {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	if l.logger != nil {
		logger = l.logger
	}

	ctx, span := tracer.Start(ctx, "event.tenant_event_listener.start")
	defer span.End()

	ctx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.done = make(chan struct{})

	pubsub := l.redisClient.PSubscribe(ctx, SubscriptionPattern)

	if l.service != "" {
		logger.InfofCtx(ctx, "tenant event listener [%s] subscribed to pattern: %s", l.service, SubscriptionPattern)
	} else {
		logger.InfofCtx(ctx, "tenant event listener subscribed to pattern: %s", SubscriptionPattern)
	}

	go l.listen(ctx, pubsub)

	return nil
}

// Stop unsubscribes the listener, cancels the goroutine, and waits for it
// to finish. Safe to call multiple times.
func (l *TenantEventListener) Stop() error {
	if l.cancel != nil {
		l.cancel()
	}

	if l.done != nil {
		<-l.done
	}

	return nil
}

// listen is the background goroutine that reads messages from the Pub/Sub
// channel, parses each one, and dispatches to the handler.
func (l *TenantEventListener) listen(ctx context.Context, pubsub *redis.PubSub) {
	defer close(l.done)
	defer func() {
		if err := pubsub.Close(); err != nil {
			l.logger.WarnfCtx(ctx, "tenant event listener: failed to close pubsub: %v", err)
		}
	}()

	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			l.logger.InfoCtx(ctx, "tenant event listener stopped: context cancelled")
			return
		case msg, ok := <-ch:
			if !ok {
				l.logger.InfoCtx(ctx, "tenant event listener stopped: channel closed")
				return
			}

			l.handleMessage(ctx, msg)
		}
	}
}

// handleMessage parses a single Pub/Sub message and dispatches it to the handler.
// Parse errors and handler errors are logged and skipped (non-fatal).
func (l *TenantEventListener) handleMessage(ctx context.Context, msg *redis.Message) {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	if l.logger != nil {
		logger = l.logger
	}

	ctx, span := tracer.Start(ctx, "event.tenant_event_listener.handle_message")
	defer span.End()

	evt, err := ParseEvent([]byte(msg.Payload))
	if err != nil {
		logger.WarnfCtx(ctx, "tenant event listener: failed to parse event from channel %s: %v",
			msg.Channel, err)
		libOpentelemetry.HandleSpanError(span, "failed to parse tenant event", err)

		return
	}

	logger.InfofCtx(ctx, "tenant event listener: received event_type=%s tenant_id=%s event_id=%s",
		evt.EventType, evt.TenantID, evt.EventID)

	if handlerErr := l.handler(ctx, *evt); handlerErr != nil {
		logger.WarnfCtx(ctx, "tenant event listener: handler error for event %s (type=%s): %v",
			evt.EventID, evt.EventType, handlerErr)
		libOpentelemetry.HandleSpanError(span, "tenant event handler error", handlerErr)
	}
}
