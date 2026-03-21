//go:build unit

package http

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestURLValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		website string
		wantErr bool
	}{
		{name: "valid HTTP URL", website: "http://example.com", wantErr: false},
		{name: "valid HTTPS URL", website: "https://example.com/path", wantErr: false},
		{name: "invalid URL", website: "not-a-url", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testURLPayload{Website: tc.website}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFieldURL)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUUIDValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "valid UUID", id: "550e8400-e29b-41d4-a716-446655440000", wantErr: false},
		{name: "invalid UUID", id: "not-a-uuid", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testUUIDPayload{ID: tc.id}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFieldUUID)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLteValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   int
		wantErr bool
	}{
		{name: "value less than constraint is valid", value: 50, wantErr: false},
		{name: "value equal to constraint is valid", value: 100, wantErr: false},
		{name: "value greater than constraint is invalid", value: 101, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testLtePayload{Value: tc.value}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFieldLessThanOrEqual)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLtValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   int
		wantErr bool
	}{
		{name: "value less than constraint is valid", value: 50, wantErr: false},
		{name: "value equal to constraint is invalid", value: 100, wantErr: true},
		{name: "value greater than constraint is invalid", value: 101, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testLtPayload{Value: tc.value}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFieldLessThan)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMinValidator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "value at minimum is valid", value: "hello", wantErr: false},
		{name: "value above minimum is valid", value: "hello world", wantErr: false},
		{name: "value below minimum is invalid", value: "hi", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := testMinPayload{Name: tc.value}
			err := ValidateStruct(&payload)

			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrFieldMinLength)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
