//go:build unit

package http

import (
	"testing"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateLimitStrict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		limit   int
		want    int
		wantErr error
	}{
		{name: "valid limit", limit: 10, want: 10},
		{name: "limit capped", limit: cn.MaxLimit + 10, want: cn.MaxLimit},
		{name: "zero rejected", limit: 0, wantErr: ErrLimitMustBePositive},
		{name: "negative rejected", limit: -5, wantErr: ErrLimitMustBePositive},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ValidateLimitStrict(tc.limit, cn.MaxLimit)
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Zero(t, got)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
