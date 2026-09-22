//go:build unit

package crypto

import (
	"fmt"
	"testing"

	constant "github.com/LerianStudio/lib-observability/v4/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSealer_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Sealer

	sealed, err := s.Seal([]byte("x"), nil)
	require.ErrorIs(t, err, ErrNilSealer)
	assert.Nil(t, sealed)

	opened, err := s.Open([]byte("0123456789abcdef"), nil)
	require.ErrorIs(t, err, ErrNilSealer)
	assert.Nil(t, opened)

	require.ErrorIs(t, s.Rotate(secretA), ErrNilSealer)
	assert.Equal(t, "<nil>", s.String())
	assert.Equal(t, "<nil>", s.GoString())
}

func TestSealer_ZeroValue(t *testing.T) {
	t.Parallel()

	s := &Sealer{}

	_, err := s.Seal([]byte("x"), nil)
	require.ErrorIs(t, err, ErrCipherNotInitialized)

	_, err = s.Open([]byte("0123456789abcdef"), nil)
	require.ErrorIs(t, err, ErrCipherNotInitialized)

	require.ErrorIs(t, s.Rotate(secretA), ErrCipherNotInitialized)
}

func TestSealer_Redaction(t *testing.T) {
	t.Parallel()

	secret := []byte("super-secret-sealer-key")

	s, err := NewSealer(testPurpose, secret)
	require.NoError(t, err)

	for _, out := range []string{s.String(), s.GoString(), fmt.Sprintf("%v", s), fmt.Sprintf("%#v", s), fmt.Sprintf("%+v", s)} {
		assert.Contains(t, out, constant.ObfuscatedValue)
		assert.NotContains(t, out, string(secret))
	}
}
