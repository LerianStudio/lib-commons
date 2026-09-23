//go:build unit

package server

import (
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// TestExecuteShutdown_BeforeLaunch_RefusesLaterFiberLaunch pins the ordering
// guarantee between shutdown and server startup: once executeShutdown has run,
// a fiber launch that lost the race must be refused instead of starting a
// Listen goroutine that would keep serving after graceful shutdown completed.
func TestExecuteShutdown_BeforeLaunch_RefusesLaterFiberLaunch(t *testing.T) {
	t.Parallel()

	sm := NewServerManager(nil, nil, nil).WithHTTPServer(fiber.New(), ":0")

	// Shutdown wins the race: it runs before any launch published its
	// lifecycle signal.
	sm.executeShutdown()

	if launched := sm.launchFiberHTTPServer(); launched {
		t.Fatal("launchFiberHTTPServer must refuse to launch after shutdown was initiated")
	}

	sm.lifecycleMu.Lock()
	defer sm.lifecycleMu.Unlock()

	if sm.fiberListenDone != nil {
		t.Fatal("no listen lifecycle signal must be published for a refused launch")
	}
}

// TestExecuteShutdown_AfterLaunch_WaitsForListenExit pins the normal ordering:
// a launch that published its lifecycle signal before shutdown must be waited
// on, so the Listen goroutine cannot outlive executeShutdown.
func TestExecuteShutdown_AfterLaunch_WaitsForListenExit(t *testing.T) {
	t.Parallel()

	sm := NewServerManager(nil, nil, nil).WithHTTPServer(fiber.New(), ":0")

	if launched := sm.launchFiberHTTPServer(); !launched {
		t.Fatal("launchFiberHTTPServer must launch before shutdown is initiated")
	}

	sm.executeShutdown()

	select {
	case <-sm.fiberListenDone:
		// Listen goroutine exited before executeShutdown returned.
	default:
		t.Fatal("executeShutdown must not return while the fiber Listen goroutine is still running")
	}
}

// TestStartupErrors_EveryConfiguredServerReports pins that the startup error
// channel is wide enough for every configured server: with the main HTTP,
// gRPC and admin ports all taken, each of the three failed binds lands on the
// channel and none takes the drop branch. The servers are launched without the
// shutdown handler reading the channel, so the channel alone must hold all
// three reports; the returned error is pinned by
// TestAdminHTTPServerStartupErrorSurfaces.
func TestStartupErrors_EveryConfiguredServerReports(t *testing.T) {
	occupied := func() string {
		t.Helper()

		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = l.Close() })

		return l.Addr().String()
	}

	sm := NewServerManager(nil, nil, nil).
		WithHTTPServer(fiber.New(), occupied()).
		WithGRPCServer(grpc.NewServer(), occupied()).
		WithAdminHTTPServer(fiber.New(), occupied())

	require.NoError(t, sm.initServers())
	t.Cleanup(sm.executeShutdown)

	require.Eventually(t, func() bool { return len(sm.startupErrors) == 3 },
		5*time.Second, 10*time.Millisecond, "every failed bind must reach the startup error channel")

	reports := make([]string, 0, 3)
	for range 3 {
		reports = append(reports, (<-sm.startupErrors).Error())
	}

	for _, prefix := range []string{"HTTP server:", "gRPC listen:", "admin HTTP server:"} {
		assert.True(t, slices.ContainsFunc(reports, func(r string) bool { return strings.HasPrefix(r, prefix) }),
			"missing the %q report (reports=%v)", prefix, reports)
	}
}

// TestSameListenAddress pins both directions of the admin address conflict
// check: sockets that would collide conflict, and sockets that would not do
// not, so a service separating API and admin by interface still boots.
func TestSameListenAddress(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"same string", "127.0.0.1:9000", "127.0.0.1:9000", true},
		{"wildcard beside explicit host", ":9000", "127.0.0.1:9000", true},
		{"unspecified IPv4 beside explicit host", "0.0.0.0:9000", "127.0.0.1:9000", true},
		{"unspecified IPv6 beside explicit host", "[::]:9000", "127.0.0.1:9000", true},
		{"two explicit hosts on one port", "127.0.0.1:9000", "192.168.1.5:9000", false},
		{"different ports", ":9000", ":9001", false},
		{"malformed address falls back to string compare", "8081", ":8081", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, sameListenAddress(tc.a, tc.b))
			assert.Equal(t, tc.want, sameListenAddress(tc.b, tc.a), "the check must be symmetric")
		})
	}
}
