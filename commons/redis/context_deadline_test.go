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

	"github.com/LerianStudio/lib-commons/v7/commons"
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
func startStalledRedis(t *testing.T) string {
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

	return ln.Addr().String()
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

// TestClientHonoursCallerDeadlineAgainstStalledServer measures the wall time of
// a command whose server never answers.
//
// This is deliberately a clock measurement and not an assertion that
// ContextTimeoutEnabled is set: the field is only interesting because of what
// go-redis does with it, and a field assertion would keep passing if that
// behaviour ever changed. socketBound is set an order of magnitude above
// callerBudget so the two possible outcomes cannot be confused — the call
// either returns on the caller's budget or on the socket timeout.
func TestClientHonoursCallerDeadlineAgainstStalledServer(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "true")

	const (
		callerBudget = 200 * time.Millisecond
		socketBound  = 3 * time.Second
	)

	client, err := New(context.Background(), Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: startStalledRedis(t)}},
		Options: ConnectionOptions{
			ReadTimeout:  socketBound,
			WriteTimeout: socketBound,
			DialTimeout:  2 * time.Second,
			MaxRetries:   -1, // one attempt, so the elapsed time names one bound
		},
	})
	require.NoError(t, err, "the stalled server answers PING, so connecting must succeed")

	t.Cleanup(func() { _ = client.Close() })

	rdb, err := client.GetClient(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), callerBudget)
	defer cancel()

	start := time.Now()
	err = rdb.Get(ctx, "stalled-probe").Err()
	elapsed := time.Since(start)

	require.Error(t, err, "a server that never answers cannot produce a successful read")

	assert.Less(t, elapsed, socketBound/2,
		"the call outlived the caller's %s budget and ran to the %s socket timeout instead: go-redis discards the context deadline unless ContextTimeoutEnabled is set on the client options",
		callerBudget, socketBound)

	assert.Greater(t, elapsed, callerBudget/2,
		"the call returned before the budget could plausibly have expired, so the stall was not exercised")
}
