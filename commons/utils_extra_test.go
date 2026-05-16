//go:build unit

package commons

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecCmd covers the ExecCmd method.
func TestExecCmd_SimpleCommand(t *testing.T) {
	t.Parallel()

	s := &Syscmd{}
	output, err := s.ExecCmd(context.Background(), "echo", "hello")
	require.NoError(t, err)
	assert.Contains(t, string(output), "hello")
}

func TestExecCmd_NilContext(t *testing.T) {
	t.Parallel()

	s := &Syscmd{}
	// nil context should be handled gracefully
	//nolint:staticcheck
	output, err := s.ExecCmd(nil, "echo", "test")
	require.NoError(t, err)
	assert.Contains(t, string(output), "test")
}
