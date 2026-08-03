//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManager_Module_ReturnsConfiguredModule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		module string
	}{
		{name: "generic manager", module: ""},
		{name: "named module manager", module: "consignado"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			manager := NewManager(nil, "test-service", WithModule(test.module))

			require.Equal(t, test.module, manager.Module())
		})
	}
}

func TestManager_Module_NilReceiverReturnsEmpty(t *testing.T) {
	t.Parallel()

	var manager *Manager

	require.Empty(t, manager.Module())
}
