package outbox

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// EventHandler handles one outbox event.
type EventHandler func(ctx context.Context, event *OutboxEvent) error

// HandlerRegistry stores event handlers by event type.
type HandlerRegistry struct {
	mu       sync.RWMutex
	handlers map[string]EventHandler
}

func NewHandlerRegistry() *HandlerRegistry {
	return &HandlerRegistry{handlers: map[string]EventHandler{}}
}

func (registry *HandlerRegistry) Register(eventType string, handler EventHandler) error {
	if registry == nil {
		return ErrHandlerRegistryRequired
	}

	normalizedType := strings.TrimSpace(eventType)
	if normalizedType == "" {
		return ErrEventTypeRequired
	}

	if handler == nil {
		return ErrEventHandlerRequired
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()

	if registry.handlers == nil {
		registry.handlers = make(map[string]EventHandler)
	}

	if _, exists := registry.handlers[normalizedType]; exists {
		return fmt.Errorf("%w: %s", ErrHandlerAlreadyRegistered, normalizedType)
	}

	registry.handlers[normalizedType] = handler

	return nil
}

func (registry *HandlerRegistry) Handle(ctx context.Context, event *OutboxEvent) error {
	if registry == nil {
		return ErrHandlerRegistryRequired
	}

	if event == nil {
		return ErrOutboxEventRequired
	}

	eventType := strings.TrimSpace(event.EventType)
	if eventType == "" {
		return ErrEventTypeRequired
	}

	registry.mu.RLock()
	handler, ok := registry.handlers[eventType]
	registry.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrHandlerNotRegistered, eventType)
	}

	return handler(ctx, event)
}
