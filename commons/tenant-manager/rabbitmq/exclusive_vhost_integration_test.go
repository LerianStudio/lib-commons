//go:build integration

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
)

const (
	vhostIntegrationImage = "rabbitmq:3.13-management-alpine"
	vhostTenantA          = "tenant-a"
	vhostTenantB          = "tenant-b"
)

// brokerEndpoint is where the test broker listens and how to log in to it.
type brokerEndpoint struct {
	host, user, pass string
	port             int
}

// startVHostBroker runs a RabbitMQ container and returns its endpoint.
func startVHostBroker(t *testing.T) brokerEndpoint {
	t.Helper()

	ctx := context.Background()

	container, err := tcrabbit.Run(ctx, vhostIntegrationImage,
		testcontainers.WithAdditionalWaitStrategy(wait.ForListeningPort(tcrabbit.DefaultAMQPPort)))
	require.NoError(t, err, "start RabbitMQ container")

	t.Cleanup(func() {
		termCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		assert.NoError(t, container.Terminate(termCtx))
	})

	raw, err := container.AmqpURL(ctx)
	require.NoError(t, err)

	u, err := url.Parse(raw)
	require.NoError(t, err)

	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	pass, _ := u.User.Password()

	return brokerEndpoint{host: u.Hostname(), port: port, user: u.User.Username(), pass: pass}
}

// sameVHostClient serves every tenant the same broker endpoint and the default
// vhost, which is the misconfiguration WithExclusiveVHosts refuses.
func sameVHostClient(t *testing.T, ep brokerEndpoint) *client.Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := ""

		for _, id := range []string{vhostTenantA, vhostTenantB} {
			if strings.Contains(r.URL.Path, "/tenants/"+id+"/") {
				tenantID = id
			}
		}

		if tenantID == "" {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{
			"id": %q,
			"tenantSlug": %q,
			"messaging": {
				"rabbitmq": {"host": %q, "port": %d, "vhost": "/", "username": %q, "password": %q}
			}
		}`, tenantID, tenantID, ep.host, ep.port, ep.user, ep.pass)
	}))
	t.Cleanup(server.Close)

	c, err := client.NewClient(server.URL, testutil.NewMockLogger(),
		client.WithAllowInsecureHTTP(),
		client.WithServiceAPIKey("test-key"),
	)
	require.NoError(t, err)

	return c
}

func TestIntegration_ExclusiveVHosts_RefusesSecondTenant(t *testing.T) {
	ep := startVHostBroker(t)
	ctx := context.Background()

	t.Run("exclusive refuses until the holder lets go", func(t *testing.T) {
		m := NewManager(sameVHostClient(t, ep), "ledger",
			WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())
		t.Cleanup(func() { assert.NoError(t, m.Close(context.Background())) })

		connA, err := m.GetConnection(ctx, vhostTenantA)
		require.NoError(t, err)
		require.False(t, connA.IsClosed())

		_, err = m.GetConnection(ctx, vhostTenantB)
		require.ErrorIs(t, err, core.ErrVHostConflict)
		assert.Contains(t, err.Error(), vhostTenantA)
		assert.Contains(t, err.Error(), vhostTenantB)
		assert.NotContains(t, err.Error(), ep.pass+"@")

		require.NoError(t, m.CloseConnection(ctx, vhostTenantA))

		connB, err := m.GetConnection(ctx, vhostTenantB)
		require.NoError(t, err, "the vhost is free once its holder's connection is closed")
		assert.False(t, connB.IsClosed())

		_, err = m.GetConnection(ctx, vhostTenantA)
		require.ErrorIs(t, err, core.ErrVHostConflict, "the claim now belongs to tenant-b")
	})

	t.Run("default lets both tenants share the vhost", func(t *testing.T) {
		m := NewManager(sameVHostClient(t, ep), "ledger", WithLogger(testutil.NewMockLogger()))
		t.Cleanup(func() { assert.NoError(t, m.Close(context.Background())) })

		_, err := m.GetConnection(ctx, vhostTenantA)
		require.NoError(t, err)

		_, err = m.GetConnection(ctx, vhostTenantB)
		require.NoError(t, err)
	})
}

// holdingProxy forwards TCP connections to a broker. The first connection it
// accepts is held, without contacting the broker, until release is closed;
// every later one is forwarded at once.
type holdingProxy struct {
	addr    *net.TCPAddr
	held    chan struct{} // closed once the first connection is accepted
	release chan struct{} // closed by the test to forward the held connection
	heldEOF chan struct{} // closed when the held connection's client side hangs up

	mu    sync.Mutex
	conns []net.Conn
}

func startHoldingProxy(t *testing.T, upstream string) *holdingProxy {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	hp := &holdingProxy{
		addr:    ln.Addr().(*net.TCPAddr),
		held:    make(chan struct{}),
		release: make(chan struct{}),
		heldEOF: make(chan struct{}),
	}

	var wg sync.WaitGroup

	t.Cleanup(func() {
		_ = ln.Close()

		hp.mu.Lock()
		for _, c := range hp.conns {
			_ = c.Close()
		}
		hp.mu.Unlock()

		wg.Wait()
	})

	wg.Add(1)

	go func() {
		defer wg.Done()

		for first := true; ; first = false {
			downstream, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}

			hp.track(downstream)

			wg.Add(1)

			go func(first bool) {
				defer wg.Done()

				if first {
					close(hp.held)
					<-hp.release
				}

				hp.forward(downstream, upstream, first)
			}(first)
		}
	}()

	return hp
}

func (hp *holdingProxy) track(c net.Conn) {
	hp.mu.Lock()
	defer hp.mu.Unlock()

	hp.conns = append(hp.conns, c)
}

func (hp *holdingProxy) forward(downstream net.Conn, upstream string, first bool) {
	up, err := net.Dial("tcp", upstream)
	if err != nil {
		_ = downstream.Close()

		return
	}

	hp.track(up)

	done := make(chan struct{})

	go func() {
		_, _ = io.Copy(downstream, up)
		_ = downstream.Close()

		close(done)
	}()

	_, _ = io.Copy(up, downstream)
	_ = up.Close()

	if first {
		close(hp.heldEOF)
	}

	<-done
}

// TestIntegration_ExclusiveVHosts_ConcurrentDialLoserRefused drives the race the
// pre-dial check cannot see: tenant-b passes it, tenant-a dials and stores its
// connection while tenant-b's dial is held, then tenant-b's dial completes. The
// write-locked recheck must refuse tenant-b and close its fresh connection.
func TestIntegration_ExclusiveVHosts_ConcurrentDialLoserRefused(t *testing.T) {
	ep := startVHostBroker(t)
	proxy := startHoldingProxy(t, net.JoinHostPort(ep.host, strconv.Itoa(ep.port)))
	ctx := context.Background()

	viaProxy := ep
	viaProxy.host = "127.0.0.1"
	viaProxy.port = proxy.addr.Port

	m := NewManager(sameVHostClient(t, viaProxy), "ledger",
		WithLogger(testutil.NewMockLogger()), WithExclusiveVHosts())
	t.Cleanup(func() { assert.NoError(t, m.Close(context.Background())) })

	errB := make(chan error, 1)

	go func() {
		_, err := m.GetConnection(ctx, vhostTenantB)
		errB <- err
	}()

	select {
	case <-proxy.held:
	case <-time.After(20 * time.Second):
		t.Fatal("tenant-b never reached the dial")
	}

	connA, err := m.GetConnection(ctx, vhostTenantA)
	require.NoError(t, err, "tenant-a passes the pre-dial check: tenant-b holds no claim yet")
	require.False(t, connA.IsClosed())

	close(proxy.release)

	select {
	case err = <-errB:
	case <-time.After(30 * time.Second):
		t.Fatal("tenant-b's GetConnection never returned")
	}

	require.Error(t, err)
	require.True(t, errors.Is(err, core.ErrVHostConflict), "got %v", err)

	select {
	case <-proxy.heldEOF:
	case <-time.After(15 * time.Second):
		t.Fatal("tenant-b's refused connection was never closed")
	}

	stats := m.Stats()
	assert.Equal(t, []string{vhostTenantA}, stats.TenantIDs)
	assert.False(t, connA.IsClosed(), "the winner's connection is untouched")
}
