//go:build unit

// Copyright 2025 Lerian Studio.

package webhook

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifySignatureV1(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"id":"123"}`)
	const (
		timestamp = int64(1700000000)
		secret    = "test-secret"
	)

	v1Signature := computeHMACv1(payload, timestamp, secret)
	v0Signature := "sha256=" + computeHMAC(payload, secret)

	tests := []struct {
		name      string
		payload   []byte
		timestamp int64
		secret    string
		signature string
		wantErr   string
	}{
		{
			name:      "accepts valid v1 signature",
			payload:   payload,
			timestamp: timestamp,
			secret:    secret,
			signature: v1Signature,
		},
		{
			name:      "rejects valid v0 signature",
			payload:   payload,
			timestamp: timestamp,
			secret:    secret,
			signature: v0Signature,
			wantErr:   "v1 signature required",
		},
		{
			name:      "rejects tampered payload",
			payload:   []byte(`{"id":"456"}`),
			timestamp: timestamp,
			secret:    secret,
			signature: v1Signature,
			wantErr:   "v1 signature mismatch",
		},
		{
			name:      "rejects tampered timestamp",
			payload:   payload,
			timestamp: timestamp + 1,
			secret:    secret,
			signature: v1Signature,
			wantErr:   "v1 signature mismatch",
		},
		{
			name:      "rejects tampered secret",
			payload:   payload,
			timestamp: timestamp,
			secret:    "other-secret",
			signature: v1Signature,
			wantErr:   "v1 signature mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := VerifySignatureV1(tt.payload, tt.timestamp, tt.secret, tt.signature)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestVerifySignatureV1WithFreshnessAt(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"id":"123"}`)
	const secret = "test-secret"
	now := time.Unix(1700000300, 0)
	tolerance := 5 * time.Minute

	tests := []struct {
		name      string
		timestamp int64
		wantErr   string
	}{
		{name: "accepts lower boundary", timestamp: now.Add(-tolerance).Unix()},
		{name: "accepts upper boundary", timestamp: now.Add(tolerance).Unix()},
		{name: "rejects stale timestamp", timestamp: now.Add(-tolerance - time.Second).Unix(), wantErr: "outside tolerance window"},
		{name: "rejects future timestamp", timestamp: now.Add(tolerance + time.Second).Unix(), wantErr: "outside tolerance window"},
		{name: "rejects future timestamp beyond duration range", timestamp: now.Unix() + math.MaxInt64/int64(time.Second) + 1, wantErr: "outside tolerance window"},
		{name: "rejects timestamp beyond duration range", timestamp: math.MaxInt64, wantErr: "outside tolerance window"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			signature := computeHMACv1(payload, tt.timestamp, secret)
			err := verifySignatureV1WithFreshnessAt(payload, tt.timestamp, secret, signature, tolerance, now)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestVerifySignatureWithFreshnessAtRejectsFutureTimestampBeyondDurationRange(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"id":"123"}`)
	const secret = "test-secret"
	now := time.Unix(1700000300, 0)
	timestamp := now.Unix() + math.MaxInt64/int64(time.Second) + 1
	signature := computeHMACv1(payload, timestamp, secret)

	err := verifySignatureWithFreshnessAt(payload, timestamp, secret, signature, 5*time.Minute, now)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside tolerance window")
}

func TestVerifySignatureV1WithFreshness(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"id":"123"}`)
	const secret = "test-secret"
	timestamp := time.Now().Unix()
	v1Signature := computeHMACv1(payload, timestamp, secret)
	v0Signature := "sha256=" + computeHMAC(payload, secret)

	assert.NoError(t, VerifySignatureV1WithFreshness(
		payload,
		timestamp,
		secret,
		v1Signature,
		time.Minute,
	))

	err := VerifySignatureV1WithFreshness(payload, timestamp, secret, v0Signature, time.Minute)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "v1 signature required")
}
