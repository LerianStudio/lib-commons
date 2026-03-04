// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package middleware

import (
	"reflect"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
)

// dummyHandler is a no-op fiber.Handler used to verify handler identity in tests.
func dummyHandler(c *fiber.Ctx) error { return c.Next() }

// anotherHandler is a second no-op handler to distinguish from dummyHandler.
func anotherHandler(c *fiber.Ctx) error { return c.Next() }

func TestWithTenantRoute(t *testing.T) {
	t.Run("nil tenant middleware returns only auth handler", func(t *testing.T) {
		handlers := WithTenantRoute(dummyHandler, nil)

		assert.Len(t, handlers, 1)
	})

	t.Run("non-nil tenant middleware returns auth then tenant", func(t *testing.T) {
		handlers := WithTenantRoute(dummyHandler, anotherHandler)

		assert.Len(t, handlers, 2)
	})

	t.Run("auth handler is first, tenant is second", func(t *testing.T) {
		auth := func(c *fiber.Ctx) error { return nil }
		tenant := func(c *fiber.Ctx) error { return nil }
		handlers := WithTenantRoute(auth, tenant)

		assert.Len(t, handlers, 2)
		assert.Equal(t, reflect.ValueOf(auth).Pointer(), reflect.ValueOf(handlers[0]).Pointer())
		assert.Equal(t, reflect.ValueOf(tenant).Pointer(), reflect.ValueOf(handlers[1]).Pointer())
	})

	t.Run("nil auth handler panics", func(t *testing.T) {
		assert.PanicsWithValue(t, "middleware: authHandler must not be nil", func() {
			WithTenantRoute(nil, dummyHandler)
		})
	})
}

func TestWithTenantRouteChain(t *testing.T) {
	t.Run("nil tenant middleware returns only pre-handlers", func(t *testing.T) {
		handlers := WithTenantRouteChain(nil, dummyHandler, anotherHandler)

		assert.Len(t, handlers, 2)
	})

	t.Run("non-nil tenant middleware appends tenant after pre-handlers", func(t *testing.T) {
		handlers := WithTenantRouteChain(dummyHandler, anotherHandler)

		assert.Len(t, handlers, 2)
	})

	t.Run("multiple pre-handlers with tenant middleware", func(t *testing.T) {
		handlers := WithTenantRouteChain(dummyHandler, anotherHandler, dummyHandler, anotherHandler)

		assert.Len(t, handlers, 4)
	})

	t.Run("zero pre-handlers with non-nil tenant returns only tenant", func(t *testing.T) {
		handlers := WithTenantRouteChain(dummyHandler)

		assert.Len(t, handlers, 1)
	})

	t.Run("zero pre-handlers with nil tenant returns empty slice", func(t *testing.T) {
		handlers := WithTenantRouteChain(nil)

		assert.Empty(t, handlers)
	})

	t.Run("tenant middleware is always last", func(t *testing.T) {
		auth := func(c *fiber.Ctx) error { return nil }
		rateLimit := func(c *fiber.Ctx) error { return nil }
		tenant := func(c *fiber.Ctx) error { return nil }
		handlers := WithTenantRouteChain(tenant, auth, rateLimit)

		assert.Len(t, handlers, 3)
		assert.Equal(t, reflect.ValueOf(auth).Pointer(), reflect.ValueOf(handlers[0]).Pointer())
		assert.Equal(t, reflect.ValueOf(rateLimit).Pointer(), reflect.ValueOf(handlers[1]).Pointer())
		assert.Equal(t, reflect.ValueOf(tenant).Pointer(), reflect.ValueOf(handlers[2]).Pointer())
	})

	t.Run("nil entry in preHandlers panics", func(t *testing.T) {
		assert.PanicsWithValue(t, "middleware: preHandlers[1] must not be nil", func() {
			WithTenantRouteChain(dummyHandler, anotherHandler, nil)
		})
	})
}
