// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package rabbitmq

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
)

// defaultVHost is the vhost RabbitMQ (and amqp091) selects when the URI path
// names none, so an empty configured vhost and "/" are the same vhost.
const defaultVHost = "/"

// WithExclusiveVHosts makes the manager refuse a tenant whose RabbitMQ config
// resolves to a broker vhost that another tenant of this manager already holds
// over a live connection. The refused call returns an error wrapping
// core.ErrVHostConflict that names the broker, the vhost and both tenants, and
// no connection is dialed. A config change detected by settings revalidation
// that would collide keeps the tenant's current connection and logs an ERROR.
//
// Use it whenever tenant identity is derived from the vhost a message arrived
// on (a per-tenant consumer, a subscription bound to the tenant's queues): two
// tenants on one vhost read each other's messages. The multi-tenant consumer
// needs no change: the refusal surfaces through its existing reconnect backoff
// and degraded marking, and clears once the tenant's config is fixed.
//
// A vhost is identified by the lowercased broker host, the port and the
// case-sensitive vhost name, with an empty name meaning "/". The check does
// not detect one broker reached under two hostnames or addresses, and it
// covers the tenants of a single manager, not other managers or processes.
//
// Off by default, because development setups commonly point every tenant at
// the default vhost.
func WithExclusiveVHosts() Option {
	return func(p *Manager) {
		p.exclusiveVHost = true
	}
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
// vhosts are on and a tenant other than tenantID holds claim over a live
// connection, and nil otherwise. Caller MUST hold p.mu (read or write).
func (p *Manager) vhostConflict(tenantID string, claim vhostClaim) error {
	if !p.exclusiveVHost {
		return nil
	}

	for holder, held := range p.vhosts {
		if holder == tenantID || held != claim {
			continue
		}

		if conn := p.connections[holder]; conn != nil && !conn.IsClosed() {
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
