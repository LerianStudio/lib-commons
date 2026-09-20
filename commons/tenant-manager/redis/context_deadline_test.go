//go:build unit

package redis

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stalledReplies are the only commands a stalled server answers: enough for
// go-redis to finish its handshake and the connect-time PING. Refusing HELLO
// keeps the connection on RESP2, which needs no map replies.
var stalledReplies = map[string]string{
	"HELLO":  "-ERR unknown command 'HELLO'\r\n",
	"CLIENT": "-ERR unknown subcommand\r\n",
	"AUTH":   "+OK\r\n",
	"SELECT": "+OK\r\n",
	"PING":   "+PONG\r\n",
}

// startStalledRedis serves a Redis that connects, greets, answers PING, and
// then goes permanently silent on every data command. This is the failure the
// caller's deadline is supposed to bound: the socket is healthy, so nothing at
// the TCP layer errors out and only a timeout can end the call.
func startStalledRedis(t *testing.T) (host, port string) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			go serveStalled(conn)
		}
	}()

	host, port, err = net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)

	return host, port
}

func serveStalled(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	br := bufio.NewReader(conn)

	for {
		name, err := readStalledCommand(br)
		if err != nil {
			return
		}

		reply, answerable := stalledReplies[name]
		if !answerable {
			// Keep draining so the client never blocks writing, but answer
			// nothing from here on.
			_, _ = io.Copy(io.Discard, br)

			return
		}

		if _, err := conn.Write([]byte(reply)); err != nil {
			return
		}
	}
}

// readStalledCommand consumes one RESP array and returns its uppercased verb.
func readStalledCommand(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}

	line = strings.TrimRight(line, "\r\n")

	if !strings.HasPrefix(line, "*") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			return "", io.ErrUnexpectedEOF
		}

		return strings.ToUpper(fields[0]), nil
	}

	argc, err := strconv.Atoi(line[1:])
	if err != nil {
		return "", err
	}

	var verb string

	for i := 0; i < argc; i++ {
		header, err := br.ReadString('\n')
		if err != nil {
			return "", err
		}

		size, err := strconv.Atoi(strings.TrimRight(header, "\r\n")[1:])
		if err != nil {
			return "", err
		}

		arg := make([]byte, size+2) // payload plus CRLF
		if _, err := io.ReadFull(br, arg); err != nil {
			return "", err
		}

		if i == 0 {
			verb = strings.ToUpper(string(arg[:size]))
		}
	}

	return verb, nil
}

// TestTenantPubSubClientHonoursCallerDeadlineAgainstStalledServer measures the
// wall time of a command whose server never answers.
//
// This configuration exposes no timeout knob at all, so the only bound other
// than the caller's deadline is go-redis' own 3s ReadTimeout default — which is
// what the call falls back to when the deadline is discarded. The assertion is
// on the clock rather than on the option field, because the field only matters
// for the behaviour go-redis attaches to it.
func TestTenantPubSubClientHonoursCallerDeadlineAgainstStalledServer(t *testing.T) {
	const (
		callerBudget = 200 * time.Millisecond
		// go-redis' default ReadTimeout, the fallback bound for this client.
		socketBound = 3 * time.Second
	)

	host, port := startStalledRedis(t)

	rdb, err := NewTenantPubSubRedisClient(context.Background(), TenantPubSubRedisConfig{
		Host: host,
		Port: port,
	})
	require.NoError(t, err, "the stalled server answers PING, so connecting must succeed")

	t.Cleanup(func() { _ = rdb.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), callerBudget)
	defer cancel()

	start := time.Now()
	err = rdb.Get(ctx, "stalled-probe").Err()
	elapsed := time.Since(start)

	require.Error(t, err, "a server that never answers cannot produce a successful read")

	assert.Less(t, elapsed, socketBound/2,
		"the call outlived the caller's %s budget and ran to the %s socket-read default instead: go-redis discards the context deadline unless ContextTimeoutEnabled is set on the client options",
		callerBudget, socketBound)

	assert.Greater(t, elapsed, callerBudget/2,
		"the call returned before the budget could plausibly have expired, so the stall was not exercised")
}
