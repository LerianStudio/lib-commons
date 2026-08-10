//go:build unit

package idempotency

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequestFingerprint_WithoutScope_PreservesLegacyVectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
		body   []byte
		want   string
	}{
		{
			name:   "empty body",
			method: http.MethodPost,
			path:   "/test",
			want:   "e963d546bca9b5959c64ace9a141bd2447f6095fb00b0f5dec919c076214fc15",
		},
		{
			name:   "JSON body",
			method: http.MethodPost,
			path:   "/test",
			body:   []byte(`{"amount":10}`),
			want:   "cfd20c604859fff109feb290356b2350c4451da33c22691722eebaaf861099aa",
		},
		{
			name:   "different method and path",
			method: http.MethodPut,
			path:   "/resource",
			body:   []byte("payload"),
			want:   "791a9a838fb6782de0a8366e5d7f37b08685cf85b8ecfc364803fc62073c4ac3",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.want,
				requestFingerprint(testCase.method, testCase.path, testCase.body))
		})
	}
}

func TestRequestFingerprint_WithScope_UsesVersionedLengthPrefixedDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		scope  string
		method string
		path   string
		body   []byte
		want   string
	}{
		{
			name:   "empty scope remains in scoped domain",
			method: http.MethodPost,
			path:   "/test",
			want:   "222b14b4eb931a0efaf8904a4c6ade9263b7d65e1ac6d7001723c2b8294fc5c3",
		},
		{
			name:   "text scope",
			scope:  "profile-a",
			method: http.MethodPost,
			path:   "/test",
			body:   []byte(`{"amount":10}`),
			want:   "0f7d4bb47cabf9415ebe7541555c462d4bff1b0597d8e7db08e75d833ddb039e",
		},
		{
			name:   "scope bytes include zero byte",
			scope:  "a\x00b",
			method: http.MethodPut,
			path:   "/resource",
			body:   []byte("payload"),
			want:   "b312e1817fb2927c8834025ed4329f2760cd9f0eae7448dc836be9e6298ad16d",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.want,
				requestFingerprintWithScope(
					testCase.scope,
					testCase.method,
					testCase.path,
					testCase.body,
				))
		})
	}
}
