//go:build unit

// API compatibility pin for the systemplane v5 public surface.
//
// This file is a compile-only signature pin: it declares zero-value variables
// whose types are the exact signatures of every exported Client method, every
// Option/KeyOption constructor, every sentinel error, and every admin.Mount
// parameter. A rename, a parameter reorder, a return-type change, or removing
// an exported symbol breaks the compile — catching regressions BEFORE they
// escape a review.
//
// The test package is systemplane_test (external) so it also pins that the
// types remain exported. There is no runtime: this file carries no TestXxx
// functions. A successful `go test -tags=unit ./commons/systemplane/...`
// compile is the only signal that matters.
//
// ORGANIZATION: the file is split into three sections — existing v5 API
// (regression pins from tasks 1-6), tenant v5 API (lock-in pins added in
// this feature), and admin API (exported by the admin subpackage).
//
// When intentionally changing a signature as part of a planned v6 bump, a
// maintainer MUST update this file in the same change. The diff of this file
// is the authoritative record of every public-API change.
package systemplane_test

import (
	"context"
	"database/sql"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/admin"
)

// ---------------------------------------------------------------------------
// Exported types: existence pins.
//
// Declaring a typed variable forces the type to stay exported. A type rename
// or a package-level removal breaks the compile here.
// ---------------------------------------------------------------------------

var (
	_ systemplane.Client       // struct type
	_ systemplane.ListEntry    // struct type (returned by List)
	_ systemplane.RedactPolicy // enum type
	_ systemplane.Option       // func type
	_ systemplane.KeyOption    // func type
	_ systemplane.TestStore    // interface type
	_ systemplane.TestEntry    // struct type
	_ systemplane.TestEvent    // struct type

	_ admin.MountOption // func type
)

// ---------------------------------------------------------------------------
// Existing v5 Client method signatures — regression pins from tasks 1-6.
//
// A drift on any of these is a v5 breaking change: every consumer compiled
// against the current API depends on the exact shape below.
// ---------------------------------------------------------------------------

var (
	_ func(namespace, key string, defaultValue any, opts ...systemplane.KeyOption) error = (*systemplane.Client)(nil).Register

	_ func(namespace, key string) (any, bool)              = (*systemplane.Client)(nil).Get
	_ func(namespace, key string) string                   = (*systemplane.Client)(nil).GetString
	_ func(namespace, key string) int                      = (*systemplane.Client)(nil).GetInt
	_ func(namespace, key string) bool                     = (*systemplane.Client)(nil).GetBool
	_ func(namespace, key string) float64                  = (*systemplane.Client)(nil).GetFloat64
	_ func(namespace, key string) time.Duration            = (*systemplane.Client)(nil).GetDuration
	_ func(namespace string) []systemplane.ListEntry       = (*systemplane.Client)(nil).List
	_ func(namespace, key string) string                   = (*systemplane.Client)(nil).KeyDescription
	_ func(namespace, key string) systemplane.RedactPolicy = (*systemplane.Client)(nil).KeyRedaction

	_ func(ctx context.Context, namespace, key string, value any, actor string) error = (*systemplane.Client)(nil).Set

	_ func(namespace, key string, fn func(newValue any)) (unsubscribe func()) = (*systemplane.Client)(nil).OnChange

	_ func(ctx context.Context) error = (*systemplane.Client)(nil).Start
	_ func() error                    = (*systemplane.Client)(nil).Close
)

// ---------------------------------------------------------------------------
// New v5 Tenant API — lock-in pins.
//
// These signatures correspond to TRD §2.1 and are the surface plugin-br-bank-
// transfer (and future consumers) will adopt. A change here is a breaking
// change for those consumers and MUST coincide with a major-version bump.
// ---------------------------------------------------------------------------

var (
	_ func(namespace, key string, defaultValue any, opts ...systemplane.KeyOption) error = (*systemplane.Client)(nil).RegisterTenantScoped

	_ func(ctx context.Context, namespace, key string) (any, bool, error)             = (*systemplane.Client)(nil).GetForTenant
	_ func(ctx context.Context, namespace, key string, value any, actor string) error = (*systemplane.Client)(nil).SetForTenant
	_ func(ctx context.Context, namespace, key, actor string) error                   = (*systemplane.Client)(nil).DeleteForTenant
	_ func(namespace, key string) []string                                            = (*systemplane.Client)(nil).ListTenantsForKey

	_ func(namespace, key string, fn func(ctx context.Context, namespace, key, tenantID string, newValue any)) (unsubscribe func()) = (*systemplane.Client)(nil).OnTenantChange

	// Typed accessor mirrors — all return (T, error) with the fail-closed
	// contract documented in tenant_scoped_accessors.go. A shape regression
	// that returns a bare T (silently collapsing missing-tenant errors) is
	// caught here.
	_ func(ctx context.Context, namespace, key string) (string, error)        = (*systemplane.Client)(nil).GetStringForTenant
	_ func(ctx context.Context, namespace, key string) (int, error)           = (*systemplane.Client)(nil).GetIntForTenant
	_ func(ctx context.Context, namespace, key string) (bool, error)          = (*systemplane.Client)(nil).GetBoolForTenant
	_ func(ctx context.Context, namespace, key string) (float64, error)       = (*systemplane.Client)(nil).GetFloat64ForTenant
	_ func(ctx context.Context, namespace, key string) (time.Duration, error) = (*systemplane.Client)(nil).GetDurationForTenant
)

// ---------------------------------------------------------------------------
// Constructors — NewPostgres, NewMongoDB, NewForTesting.
//
// The TestStore mirror is explicitly unstable (see client_testing.go:15-16)
// but its NewForTesting constructor signature is still pinned so any
// intentional change is surfaced at review time.
// ---------------------------------------------------------------------------

var (
	_ func(db *sql.DB, listenDSN string, opts ...systemplane.Option) (*systemplane.Client, error)          = systemplane.NewPostgres
	_ func(client *mongo.Client, database string, opts ...systemplane.Option) (*systemplane.Client, error) = systemplane.NewMongoDB
	_ func(s systemplane.TestStore, opts ...systemplane.Option) (*systemplane.Client, error)               = systemplane.NewForTesting
)

// ---------------------------------------------------------------------------
// Option constructors — all return a systemplane.Option.
// ---------------------------------------------------------------------------

var (
	_ func(l log.Logger) systemplane.Option               = systemplane.WithLogger
	_ func(t *opentelemetry.Telemetry) systemplane.Option = systemplane.WithTelemetry
	_ func(name string) systemplane.Option                = systemplane.WithListenChannel
	_ func(d time.Duration) systemplane.Option            = systemplane.WithPollInterval
	_ func(d time.Duration) systemplane.Option            = systemplane.WithDebounce
	_ func(name string) systemplane.Option                = systemplane.WithCollection
	_ func(name string) systemplane.Option                = systemplane.WithTable
	_ func(maxEntries int) systemplane.Option             = systemplane.WithLazyTenantLoad
)

// ---------------------------------------------------------------------------
// KeyOption constructors — all return a systemplane.KeyOption.
// ---------------------------------------------------------------------------

var (
	_ func(s string) systemplane.KeyOption                        = systemplane.WithDescription
	_ func(fn func(any) error) systemplane.KeyOption              = systemplane.WithValidator
	_ func(policy systemplane.RedactPolicy) systemplane.KeyOption = systemplane.WithRedaction
)

// ---------------------------------------------------------------------------
// RedactPolicy constants — existence pins.
//
// Renaming any of these or changing the underlying kind (int vs named type)
// breaks the compile.
// ---------------------------------------------------------------------------

var (
	_ systemplane.RedactPolicy = systemplane.RedactNone
	_ systemplane.RedactPolicy = systemplane.RedactMask
	_ systemplane.RedactPolicy = systemplane.RedactFull
)

var _ func(value any, policy systemplane.RedactPolicy) any = systemplane.ApplyRedaction

// ---------------------------------------------------------------------------
// Sentinel errors — every exported sentinel is pinned.
//
// A renamed or removed sentinel breaks the compile. New sentinels can be added
// freely (additive); this file is updated in the same change to reflect the
// new surface.
// ---------------------------------------------------------------------------

var (
	// Existing v5 sentinels (preserved verbatim per TRD §6).
	_ error = systemplane.ErrClosed
	_ error = systemplane.ErrNotStarted
	_ error = systemplane.ErrRegisterAfterStart
	_ error = systemplane.ErrUnknownKey
	_ error = systemplane.ErrValidation
	_ error = systemplane.ErrDuplicateKey

	// Tenant-scoping sentinels (new in this feature).
	_ error = systemplane.ErrMissingTenantContext
	_ error = systemplane.ErrInvalidTenantID
	_ error = systemplane.ErrTenantScopeNotRegistered
)

// ---------------------------------------------------------------------------
// Admin package — Mount + MountOption constructors.
//
// The admin subpackage exposes a small surface (Mount + three MountOptions +
// a new WithTenantAuthorizer). Its public shape is pinned the same way as
// the Client's.
// ---------------------------------------------------------------------------

var (
	_ func(router fiber.Router, c *systemplane.Client, opts ...admin.MountOption) = admin.Mount

	_ func(p string) admin.MountOption                          = admin.WithPathPrefix
	_ func(fn func(*fiber.Ctx, string) error) admin.MountOption = admin.WithAuthorizer
	_ func(fn func(*fiber.Ctx) string) admin.MountOption        = admin.WithActorExtractor

	// New in this feature.
	_ func(fn func(c *fiber.Ctx, action, tenantID string) error) admin.MountOption = admin.WithTenantAuthorizer
)
