// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
)

// defaultVHost is the vhost RabbitMQ (and amqp091) selects when the URI path
// names none, so an empty configured vhost and "/" are the same vhost.
const defaultVHost = "/"

// WithExclusiveVHosts makes the manager refuse a tenant whose RabbitMQ config
// resolves to a broker vhost that the tenants' configuration does not give to
// that tenant alone. Ownership comes from the Tenant Manager, not from who
// connects first:
//
//   - The manager builds a census of the active tenants of its service
//     (GetActiveTenantsByService, then each tenant's config for its module):
//     which vhost each one is configured for. A vhost configured for exactly
//     one active tenant belongs to that tenant.
//   - A vhost configured for two or more active tenants is refused for every
//     one of them, whatever the connection order, with an error wrapping
//     core.ErrVHostConflict that names the broker, the vhost and all those
//     tenants (sorted), never credentials. Nothing is dialed. The refusal
//     clears once the configuration gives the vhost to a single tenant.
//   - The connecting tenant's freshly fetched config replaces its census
//     entry, so a move onto a shared vhost is refused at once.
//   - A tenant that is already connected and that a newer census puts on a
//     shared vhost loses its cached connection and its claim at its next
//     settings revalidation, with an ERROR log. Both tenants end up refused,
//     whichever revalidates first.
//
// The census is cached for the connections check interval
// (WithConnectionsCheckInterval, 30s by default; a disabled interval still
// caches it for 30s) and rebuilt lazily by the first caller that finds it
// stale, one rebuild at a time; every caller waits for it on its own context.
// A rebuild costs one active-tenants call plus one config call per active
// tenant, at most eight at once. If no census was ever built and one cannot be
// (the Tenant Manager is unreachable, or a listed tenant's config answers
// with anything but success, not found or access denied), the call fails with
// core.ErrVHostCensusUnavailable and nothing is dialed; after a first census,
// a failed rebuild keeps the last one with a WARN. Tenants listed with a
// status other than active, or whose config is not found, denied, or carries
// no RabbitMQ settings for the module, cannot connect and are left out.
//
// A second layer covers what the census cannot see yet: a tenant also holds
// its vhost from its first stored connection until that connection is
// released (CloseConnection on tenant removal or suspension, LRU eviction,
// Close), and another tenant asking for that vhost is refused with
// core.ErrVHostConflict naming the holder. A connection that drops (broker
// restart, network failure) keeps the claim. A config change detected by
// settings revalidation that lands on a vhost another tenant still holds
// keeps the tenant's current connection and logs an ERROR.
//
// Use it whenever tenant identity is derived from the vhost a message arrived
// on (a per-tenant consumer, a subscription bound to the tenant's queues): two
// tenants on one vhost read each other's messages. The multi-tenant consumer
// needs no change: the refusal surfaces through its existing reconnect backoff
// and degraded marking.
//
// A vhost is identified by the lowercased broker host, the port and the
// case-sensitive vhost name, with an empty name meaning "/". The check does
// not detect one broker reached under two hostnames or addresses, and it
// covers the tenants of one service and module, not other services.
//
// Off by default, because development setups commonly point every tenant at
// the default vhost. Without it the manager makes no census calls.
func WithExclusiveVHosts() Option {
	return func(p *Manager) {
		p.exclusiveVHost = true
	}
}

// censusFetchConcurrency bounds the tenant config calls one census rebuild
// runs at once.
const censusFetchConcurrency = 8

// censusBuildTimeout bounds one census rebuild. It runs detached from the
// caller that started it, so another caller's deadline cannot cut it short.
const censusBuildTimeout = 30 * time.Second

// censusKey is the single in-flight key of a manager's census rebuilds.
const censusKey = "vhost-census"

// vhostCensus caches which vhost each active tenant is configured for. The
// zero value is ready to use. claims is replaced, never mutated, so a map
// handed to a caller stays valid without the lock.
type vhostCensus struct {
	group singleflight.Group

	mu      sync.Mutex
	claims  map[string]vhostClaim // tenantID -> configured vhost; nil until the first build
	builtAt time.Time
}

// vhostClaim identifies one vhost on one broker. It holds no credentials.
type vhostClaim struct {
	broker string // lowercased host and port, as net.JoinHostPort forms them
	vhost  string // case-sensitive; empty normalised to defaultVHost
}

// claimFor returns the vhost cfg connects to. cfg must not be nil.
func claimFor(cfg *core.RabbitMQConfig) vhostClaim {
	vhost := cfg.VHost
	if vhost == "" {
		vhost = defaultVHost
	}

	return vhostClaim{
		broker: net.JoinHostPort(strings.ToLower(cfg.Host), strconv.Itoa(cfg.Port)),
		vhost:  vhost,
	}
}

// vhostConflict returns an error wrapping core.ErrVHostConflict when exclusive
// vhosts are on and a tenant other than tenantID holds claim through a stored
// connection, whether or not
// its connection is still open, and nil otherwise. Caller MUST hold p.mu
// (read or write).
func (p *Manager) vhostConflict(tenantID string, claim vhostClaim) error {
	if !p.exclusiveVHost {
		return nil
	}

	for holder, held := range p.vhosts {
		if holder != tenantID && held == claim {
			return fmt.Errorf("%w: broker %s vhost %q requested by tenant %s is held by tenant %s",
				core.ErrVHostConflict, claim.broker, claim.vhost, tenantID, holder)
		}
	}

	return nil
}

// heldVHostConflict is vhostConflict under the read lock.
// Caller must NOT hold p.mu.
func (p *Manager) heldVHostConflict(tenantID string, claim vhostClaim) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.vhostConflict(tenantID, claim)
}

// admitVHost returns nil when tenantID may connect to claim: always without
// WithExclusiveVHosts; with it, when the configuration gives the vhost to
// tenantID alone and no other tenant holds it. Caller must NOT hold p.mu.
func (p *Manager) admitVHost(ctx context.Context, tenantID string, claim vhostClaim) error {
	if !p.exclusiveVHost {
		return nil
	}

	if err := p.configuredVHostConflict(ctx, tenantID, claim); err != nil {
		return err
	}

	return p.heldVHostConflict(tenantID, claim)
}

// configuredVHostConflict returns an error wrapping core.ErrVHostConflict when
// the census configures claim for any active tenant other than tenantID, whose
// own census entry is replaced by claim. It returns the census error when no
// census is available. Caller must NOT hold p.mu.
func (p *Manager) configuredVHostConflict(ctx context.Context, tenantID string, claim vhostClaim) error {
	census, err := p.vhostCensus(ctx)
	if err != nil {
		return err
	}

	tenants := []string{tenantID}

	for other, configured := range census {
		if other != tenantID && configured == claim {
			tenants = append(tenants, other)
		}
	}

	if len(tenants) == 1 {
		return nil
	}

	slices.Sort(tenants)

	return fmt.Errorf("%w: broker %s vhost %q is configured for tenants %s",
		core.ErrVHostConflict, claim.broker, claim.vhost, strings.Join(tenants, ", "))
}

// vhostCensus returns the current census, rebuilding it when it is missing or
// older than the census interval. Concurrent callers share one rebuild and
// each stops waiting when its own ctx ends. A failed rebuild falls back to the
// last census with a WARN, or returns core.ErrVHostCensusUnavailable when
// there is none.
func (p *Manager) vhostCensus(ctx context.Context) (map[string]vhostClaim, error) {
	c := &p.census

	c.mu.Lock()
	last, builtAt := c.claims, c.builtAt
	c.mu.Unlock()

	if last != nil && time.Since(builtAt) < p.censusInterval() {
		return last, nil
	}

	rebuilt := c.group.DoChan(censusKey, func() (any, error) {
		buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), censusBuildTimeout)
		defer cancel()

		claims, err := p.buildVHostCensus(buildCtx)
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		c.claims, c.builtAt = claims, time.Now()
		c.mu.Unlock()

		return claims, nil
	})

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for the rabbitmq vhost census: %w", ctx.Err())
	case res := <-rebuilt:
		if claims, ok := res.Val.(map[string]vhostClaim); ok && res.Err == nil {
			return claims, nil
		}

		if last != nil {
			if p.logger != nil {
				p.logger.Warnf("rabbitmq vhost census rebuild failed, using the census from %s: %v",
					builtAt.UTC().Format(time.RFC3339), res.Err)
			}

			return last, nil
		}

		return nil, fmt.Errorf("%w: %w", core.ErrVHostCensusUnavailable, res.Err)
	}
}

// censusInterval is how long a census stays fresh: the connections check
// interval, or its default when revalidation is disabled.
func (p *Manager) censusInterval() time.Duration {
	if p.connectionsCheckInterval > 0 {
		return p.connectionsCheckInterval
	}

	return defaultConnectionsCheckInterval
}

// buildVHostCensus asks the Tenant Manager which vhost each active tenant of
// the service is configured for. Tenants that cannot connect (not active, no
// config, access denied, no RabbitMQ for the module) are left out; any other
// failure fails the whole census, because a partial one could miss a sharer.
func (p *Manager) buildVHostCensus(ctx context.Context) (map[string]vhostClaim, error) {
	summaries, err := p.client.GetActiveTenantsByService(ctx, p.service)
	if err != nil {
		return nil, fmt.Errorf("list active tenants: %w", err)
	}

	var (
		mu     sync.Mutex
		claims = make(map[string]vhostClaim, len(summaries))
	)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(censusFetchConcurrency)

	for _, tenantID := range activeTenantIDs(summaries) {
		group.Go(func() error {
			config, fetchErr := p.client.GetTenantConfig(groupCtx, tenantID, p.service, client.WithSkipCache())

			switch {
			case fetchErr == nil:
			case errors.Is(fetchErr, core.ErrTenantNotFound), errors.Is(fetchErr, core.ErrTenantServiceAccessDenied):
				return nil
			default:
				return fmt.Errorf("tenant %s config: %w", tenantID, fetchErr)
			}

			rabbitConfig := resolveRabbitMQConfig(config, p.module)
			if rabbitConfig == nil {
				return nil
			}

			mu.Lock()
			claims[tenantID] = claimFor(rabbitConfig)
			mu.Unlock()

			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, err
	}

	return claims, nil
}

// activeTenantIDs returns the distinct, non-empty IDs of the summaries whose
// status is active or unset, as the multi-tenant consumer reads them.
func activeTenantIDs(summaries []*client.TenantSummary) []string {
	ids := make([]string, 0, len(summaries))
	seen := make(map[string]bool, len(summaries))

	for _, summary := range summaries {
		if summary == nil {
			continue
		}

		tenantID := strings.TrimSpace(summary.ID)
		if tenantID == "" || seen[tenantID] || (summary.Status != "" && !strings.EqualFold(summary.Status, "active")) {
			continue
		}

		seen[tenantID] = true
		ids = append(ids, tenantID)
	}

	return ids
}
