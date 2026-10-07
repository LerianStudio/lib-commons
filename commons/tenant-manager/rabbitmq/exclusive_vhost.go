// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
// resolves to a broker vhost that is not that tenant's alone. Ownership comes
// from the Tenant Manager's tenant-to-vhost configuration, never from which
// tenant connects first:
//
//   - The manager builds a census of the active tenants of its service
//     (GetActiveTenantsByService, then each tenant's config for its module):
//     which vhost each one is configured for. The connecting tenant's freshly
//     fetched config replaces its census entry.
//   - A tenant connects to a vhost only when the census configures it for that
//     tenant alone and no other tenant holds it. A tenant holds a vhost from
//     its first stored connection until that connection is released
//     (CloseConnection on tenant removal or suspension, LRU eviction, Close); a
//     connection that drops (broker restart, network failure) keeps the claim.
//   - The holder keeps its vhost when the configuration later names a second
//     tenant for it: the holder could only have connected while the
//     configuration gave it the vhost alone, so the tenant newly configured
//     onto it is the misconfigured one. That tenant is refused before any dial
//     with an error wrapping core.ErrVHostConflict that names the holder; a
//     connected tenant whose config moves onto the held vhost keeps its old
//     connection. While the configuration names the vhost twice, every settings
//     revalidation or reconnect of the holder logs an ERROR naming all those
//     tenants, so the operator fixes the newcomer's config. The holder's
//     connection is never closed for another tenant's configuration error.
//   - A held vhost whose holder's current config no longer names it (the
//     holder was moved to another vhost, removed, suspended or denied) goes to
//     the tenant the configuration names now: the holder's connection is
//     closed and its claim released, with a WARN, as part of that tenant's
//     GetConnection, without waiting for the holder to call GetConnection.
//     The holder's config is fetched fresh for that decision; when it cannot be
//     read, the claim stands and the requester is refused.
//   - With no holder, a vhost the census configures for two or more tenants is
//     refused for every one of them, whatever the order, with an error naming
//     the broker, the vhost and all those tenants (sorted): the configuration
//     alone cannot say which of them is wrong. This is the case after a process
//     start or after the holder's claim was released.
//
// Errors and logs never carry credentials. The holder rule is per manager, so
// per process: a pod that starts while the configuration names one vhost for
// two tenants refuses both there, even if another pod's holder keeps it.
// Messages that other services publish into a held vhost under the refused
// tenant's config still reach the holder's consumers until the configuration
// is fixed.
//
// The census is cached for the connections check interval
// (WithConnectionsCheckInterval, 30s by default; a disabled interval still
// caches it for 30s) and rebuilt lazily by the first caller that finds it
// stale, one rebuild at a time; every caller waits for it on its own context.
// A rebuild costs one active-tenants call plus one config call per active
// tenant, at most eight at once, and must finish within 30 seconds: about 240
// seconds of config-call time, so 5,000 active tenants at 50ms a config call
// already exceed it and no census is built. A tenant whose config the census cannot read
// is left out of it with a WARN; the holder layer and the connecting tenant's
// own fresh config still keep it off another tenant's vhost in this manager.
// A rebuild fails when the active tenants cannot be listed, the Tenant
// Manager's circuit breaker is open, the rebuild runs out of time, or no
// listed tenant's config could be read; it is then not retried for five
// seconds. If no census was ever built, a failed one makes the call fail with
// core.ErrVHostCensusUnavailable and nothing is dialed; after a first census,
// a failed rebuild keeps the last one with a WARN. Tenants listed with a
// status other than active, or whose config is not found, denied, suspended,
// or carries no RabbitMQ settings for the module, cannot connect and are left
// out.
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

// censusRetryBackoff is how long a failed census rebuild is not retried, so a
// failing Tenant Manager is not called by every connecting tenant.
const censusRetryBackoff = 5 * time.Second

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
	retryAt time.Time // no rebuild before this, after a failed one
	lastErr error     // why the last rebuild failed; nil after a success
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
// connection, whether or not its connection is still open, and nil otherwise.
// Caller MUST hold p.mu (read or write).
func (p *Manager) vhostConflict(tenantID string, claim vhostClaim) error {
	if !p.exclusiveVHost {
		return nil
	}

	if holder := p.vhostHolder(tenantID, claim); holder != "" {
		return heldConflict(tenantID, holder, claim)
	}

	return nil
}

// vhostHolder returns the tenant other than tenantID that holds claim, or "".
// Caller MUST hold p.mu (read or write).
func (p *Manager) vhostHolder(tenantID string, claim vhostClaim) string {
	for holder, held := range p.vhosts {
		if holder != tenantID && held == claim {
			return holder
		}
	}

	return ""
}

// heldConflict is the refusal of requester on claim, which holder holds.
func heldConflict(requester, holder string, claim vhostClaim) error {
	return fmt.Errorf("%w: broker %s vhost %q requested by tenant %s is held by tenant %s",
		core.ErrVHostConflict, claim.broker, claim.vhost, requester, holder)
}

// admitVHost returns nil when tenantID may connect to claim: always without
// WithExclusiveVHosts; with it, when tenantID holds claim, or when no other
// tenant holds it (after releasing a holder its configuration no longer
// names) and the census configures it for tenantID alone.
// Caller must NOT hold p.mu.
func (p *Manager) admitVHost(ctx context.Context, tenantID string, claim vhostClaim) error {
	if !p.exclusiveVHost {
		return nil
	}

	p.mu.RLock()
	held, holds := p.vhosts[tenantID]
	owns := holds && held == claim
	holder := p.vhostHolder(tenantID, claim)
	p.mu.RUnlock()

	if owns {
		p.reportConfiguredSharers(ctx, tenantID, claim)

		return nil
	}

	if holder != "" {
		if err := p.releaseUnconfiguredHolder(ctx, tenantID, holder, claim); err != nil {
			return err
		}
	}

	return p.configuredVHostConflict(ctx, tenantID, claim, holder)
}

// releaseUnconfiguredHolder decides whether holder, which holds claim, still
// owns it, from holder's current config. It returns the refusal of requester
// when holder's config still names claim or cannot be read, and otherwise
// closes holder's connection, releases its claim and returns nil.
// Caller must NOT hold p.mu.
func (p *Manager) releaseUnconfiguredHolder(ctx context.Context, requester, holder string, claim vhostClaim) error {
	config, err := p.client.GetTenantConfig(ctx, holder, p.service, client.WithSkipCache())

	switch {
	case err == nil:
		if rabbitConfig := resolveRabbitMQConfig(config, p.module); rabbitConfig != nil && claimFor(rabbitConfig) == claim {
			return heldConflict(requester, holder, claim)
		}
	case cannotConnect(err):
	default:
		return fmt.Errorf("%w (its current configuration could not be read: %w)", heldConflict(requester, holder, claim), err)
	}

	p.mu.Lock()

	if p.vhosts[holder] != claim {
		p.mu.Unlock()

		return nil
	}

	conn := p.connections[holder]
	delete(p.connections, holder)
	delete(p.cachedURIs, holder)
	delete(p.vhosts, holder)
	delete(p.lastAccessed, holder)
	delete(p.lastConnectionsCheck, holder)

	p.mu.Unlock()

	if p.logger != nil {
		p.logger.Warnf("tenant %s's configuration no longer names RabbitMQ broker %s vhost %q it holds; closing its connection so tenant %s can connect",
			holder, claim.broker, claim.vhost, requester)
	}

	p.closeRabbitMQConn(conn, "failed to close the RabbitMQ connection of tenant %s, whose vhost was reassigned", holder)

	return nil
}

// cannotConnect reports whether a tenant config fetch failed because the
// tenant cannot connect at all: not found, access denied or suspended.
func cannotConnect(err error) bool {
	return errors.Is(err, core.ErrTenantNotFound) ||
		errors.Is(err, core.ErrTenantServiceAccessDenied) ||
		core.IsTenantSuspendedError(err)
}

// reportConfiguredSharers logs an ERROR when the census configures claim, which
// holder holds, for other tenants too. It never refuses: the holder keeps its
// vhost. A census that cannot be read is skipped. Caller must NOT hold p.mu.
func (p *Manager) reportConfiguredSharers(ctx context.Context, holder string, claim vhostClaim) {
	census, err := p.vhostCensus(ctx)
	if err != nil || p.logger == nil {
		return
	}

	if sharers := configuredSharers(census, holder, claim, ""); len(sharers) > 0 {
		p.logger.Errorf("tenant %s keeps RabbitMQ broker %s vhost %q, which it holds; the configuration also names tenants %s, which are refused until it gives the vhost to one tenant",
			holder, claim.broker, claim.vhost, strings.Join(sharers, ", "))
	}
}

// configuredVHostConflict returns an error wrapping core.ErrVHostConflict when
// the census configures claim for any active tenant other than tenantID and
// released, whose own census entry is replaced by claim. It returns the census
// error when no census is available. Caller must NOT hold p.mu.
func (p *Manager) configuredVHostConflict(ctx context.Context, tenantID string, claim vhostClaim, released string) error {
	census, err := p.vhostCensus(ctx)
	if err != nil {
		return err
	}

	sharers := configuredSharers(census, tenantID, claim, released)
	if len(sharers) == 0 {
		return nil
	}

	tenants := append([]string{tenantID}, sharers...)
	slices.Sort(tenants)

	return fmt.Errorf("%w: broker %s vhost %q is configured for tenants %s",
		core.ErrVHostConflict, claim.broker, claim.vhost, strings.Join(tenants, ", "))
}

// configuredSharers returns, sorted, the tenants other than tenantID and
// except that census configures for claim.
func configuredSharers(census map[string]vhostClaim, tenantID string, claim vhostClaim, except string) []string {
	var sharers []string

	for other, configured := range census {
		if other != tenantID && other != except && configured == claim {
			sharers = append(sharers, other)
		}
	}

	slices.Sort(sharers)

	return sharers
}

// vhostCensus returns the current census, rebuilding it when it is missing or
// older than the census interval and no failed rebuild is backing off.
// Concurrent callers share one rebuild and each stops waiting when its own ctx
// ends. A failed rebuild falls back to the last census (with a WARN when the
// rebuild has just failed), or returns core.ErrVHostCensusUnavailable when
// there is none.
func (p *Manager) vhostCensus(ctx context.Context) (map[string]vhostClaim, error) {
	c := &p.census

	c.mu.Lock()
	last, builtAt, retryAt, lastErr := c.claims, c.builtAt, c.retryAt, c.lastErr
	c.mu.Unlock()

	if last != nil && time.Since(builtAt) < p.censusInterval() {
		return last, nil
	}

	if time.Now().Before(retryAt) {
		return censusFallback(last, lastErr)
	}

	rebuilt := c.group.DoChan(censusKey, func() (any, error) {
		return p.rebuildVHostCensus(ctx)
	})

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for the rabbitmq vhost census: %w", ctx.Err())
	case res := <-rebuilt:
		if claims, ok := res.Val.(map[string]vhostClaim); ok && res.Err == nil {
			return claims, nil
		}

		if last != nil && p.logger != nil {
			p.logger.Warnf("rabbitmq vhost census rebuild failed, using the census from %s: %v",
				builtAt.UTC().Format(time.RFC3339), res.Err)
		}

		return censusFallback(last, res.Err)
	}
}

// rebuildVHostCensus is the body of the single in-flight census rebuild. It
// first rechecks the census under the lock: a caller that read a stale census
// can reach it only after another rebuild finished, and then reuses that census,
// or, when that rebuild failed, honours its backoff, instead of calling the
// Tenant Manager again.
func (p *Manager) rebuildVHostCensus(ctx context.Context) (map[string]vhostClaim, error) {
	c := &p.census

	c.mu.Lock()

	switch {
	case c.claims != nil && time.Since(c.builtAt) < p.censusInterval():
		claims := c.claims
		c.mu.Unlock()

		return claims, nil
	case time.Now().Before(c.retryAt):
		claims, lastErr := c.claims, c.lastErr
		c.mu.Unlock()

		if claims != nil {
			return claims, nil
		}

		return nil, lastErr
	}

	c.mu.Unlock()

	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), censusBuildTimeout)
	defer cancel()

	claims, err := p.buildVHostCensus(buildCtx)

	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		c.retryAt, c.lastErr = time.Now().Add(censusRetryBackoff), err

		return nil, err
	}

	c.claims, c.builtAt, c.retryAt, c.lastErr = claims, time.Now(), time.Time{}, nil

	return claims, nil
}

// censusFallback returns last when there is one, and otherwise
// core.ErrVHostCensusUnavailable wrapping why the census could not be built.
func censusFallback(last map[string]vhostClaim, failure error) (map[string]vhostClaim, error) {
	if last != nil {
		return last, nil
	}

	return nil, fmt.Errorf("%w: %w", core.ErrVHostCensusUnavailable, failure)
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
// the service is configured for. Tenants that cannot connect (not active, not
// found, access denied, suspended, no RabbitMQ for the module) are left out,
// and so, with a WARN, is a tenant whose config cannot be read. The build
// fails when the active tenants cannot be listed, the Tenant Manager's circuit
// breaker is open, ctx ends, or no listed tenant's config could be read.
func (p *Manager) buildVHostCensus(ctx context.Context) (map[string]vhostClaim, error) {
	summaries, err := p.client.GetActiveTenantsByService(ctx, p.service)
	if err != nil {
		return nil, fmt.Errorf("list active tenants: %w", err)
	}

	var (
		mu         sync.Mutex
		claims     = make(map[string]vhostClaim, len(summaries))
		read       int
		unreadable = make(map[string]error)
	)

	var group errgroup.Group

	group.SetLimit(censusFetchConcurrency)

	for _, tenantID := range activeTenantIDs(summaries) {
		group.Go(func() error {
			config, fetchErr := p.client.GetTenantConfig(ctx, tenantID, p.service, client.WithSkipCache())

			mu.Lock()
			defer mu.Unlock()

			switch {
			case fetchErr == nil:
				read++
			case cannotConnect(fetchErr):
				read++

				return nil
			default:
				unreadable[tenantID] = fetchErr

				return nil
			}

			if rabbitConfig := resolveRabbitMQConfig(config, p.module); rabbitConfig != nil {
				claims[tenantID] = claimFor(rabbitConfig)
			}

			return nil
		})
	}

	_ = group.Wait() // every task returns nil; failures are collected above

	if len(unreadable) == 0 {
		return claims, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read tenant configs: %w", err)
	}

	tenants := slices.Sorted(maps.Keys(unreadable))

	for _, tenantID := range tenants {
		if errors.Is(unreadable[tenantID], core.ErrCircuitBreakerOpen) {
			return nil, fmt.Errorf("tenant %s config: %w", tenantID, unreadable[tenantID])
		}
	}

	if read == 0 {
		return nil, fmt.Errorf("no listed tenant config could be read; tenant %s config: %w", tenants[0], unreadable[tenants[0]])
	}

	if p.logger != nil {
		p.logger.Warnf("rabbitmq vhost census leaves out tenants %s, whose config could not be read: %v",
			strings.Join(tenants, ", "), unreadable[tenants[0]])
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
