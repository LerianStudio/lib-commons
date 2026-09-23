//go:build unit

package crypto

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPurpose = "idempotency-body"

var (
	secretA = []byte("operator secret A, any length or format")
	secretB = []byte("operator secret B")
	secretC = []byte("c")
	testAAD = []byte("IDEMBODY1")
)

func newTestSealer(t *testing.T, secret []byte) *Sealer {
	t.Helper()

	s, err := NewSealer(testPurpose, secret)
	require.NoError(t, err)

	return s
}

func TestNewSealer_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		purpose string
		secret  []byte
		wantErr error
	}{
		{name: "empty purpose", purpose: "", secret: secretA, wantErr: ErrEmptyPurpose},
		{name: "nil secret", purpose: testPurpose, secret: nil, wantErr: ErrEmptySecret},
		{name: "empty secret", purpose: testPurpose, secret: []byte{}, wantErr: ErrEmptySecret},
		{name: "valid", purpose: testPurpose, secret: secretA},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s, err := NewSealer(tt.purpose, tt.secret)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, s)

				return
			}

			require.NoError(t, err)
			assert.NotNil(t, s)
		})
	}
}

func TestSealer_RoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		plaintext []byte
		aad       []byte
	}{
		{name: "payload with aad", plaintext: []byte(`{"status":201}`), aad: testAAD},
		{name: "payload without aad", plaintext: []byte("body"), aad: nil},
		{name: "empty payload", plaintext: []byte{}, aad: testAAD},
	}

	s := newTestSealer(t, secretA)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sealed, err := s.Seal(tt.plaintext, tt.aad)
			require.NoError(t, err)
			assert.Len(t, sealed, 12+len(tt.plaintext)+16)

			opened, err := s.Open(sealed, tt.aad)
			require.NoError(t, err)
			assert.Equal(t, string(tt.plaintext), string(opened))
		})
	}
}

func TestSealer_SealIsRandomized(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t, secretA)

	a, err := s.Seal([]byte("same"), testAAD)
	require.NoError(t, err)
	b, err := s.Seal([]byte("same"), testAAD)
	require.NoError(t, err)

	assert.NotEqual(t, a, b)
}

func TestSealer_OpenRejects(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t, secretA)

	sealed, err := s.Seal([]byte("payload"), testAAD)
	require.NoError(t, err)

	flipped := append([]byte(nil), sealed...)
	flipped[len(flipped)-1] ^= 0x01

	flippedNonce := append([]byte(nil), sealed...)
	flippedNonce[0] ^= 0x01

	tests := []struct {
		name    string
		sealed  []byte
		aad     []byte
		wantErr error
	}{
		{name: "different aad", sealed: sealed, aad: []byte("OTHERMAGC"), wantErr: ErrOpenFailed},
		{name: "missing aad", sealed: sealed, aad: nil, wantErr: ErrOpenFailed},
		{name: "flipped ciphertext byte", sealed: flipped, aad: testAAD, wantErr: ErrOpenFailed},
		{name: "flipped nonce byte", sealed: flippedNonce, aad: testAAD, wantErr: ErrOpenFailed},
		{name: "nonce only, no tag", sealed: sealed[:12], aad: testAAD, wantErr: ErrOpenFailed},
		{name: "shorter than nonce", sealed: sealed[:11], aad: testAAD, wantErr: ErrCiphertextTooShort},
		{name: "nil payload", sealed: nil, aad: testAAD, wantErr: ErrCiphertextTooShort},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opened, err := s.Open(tt.sealed, tt.aad)
			require.ErrorIs(t, err, tt.wantErr)
			assert.Nil(t, opened)
		})
	}
}

func TestSealer_WrongKeyIndistinguishableFromTamper(t *testing.T) {
	t.Parallel()

	sealed, err := newTestSealer(t, secretA).Seal([]byte("payload"), testAAD)
	require.NoError(t, err)

	_, wrongKeyErr := newTestSealer(t, secretB).Open(sealed, testAAD)
	_, tamperErr := newTestSealer(t, secretA).Open(sealed, []byte("tampered!"))

	require.ErrorIs(t, wrongKeyErr, ErrOpenFailed)
	require.ErrorIs(t, tamperErr, ErrOpenFailed)
	assert.Equal(t, wrongKeyErr.Error(), tamperErr.Error())
}

func TestSealer_IndependentInstancesInteroperate(t *testing.T) {
	t.Parallel()

	writer := newTestSealer(t, secretA)
	reader := newTestSealer(t, append([]byte(nil), secretA...))

	sealed, err := writer.Seal([]byte("cross-process"), testAAD)
	require.NoError(t, err)

	opened, err := reader.Open(sealed, testAAD)
	require.NoError(t, err)
	assert.Equal(t, "cross-process", string(opened))
}

func TestSealer_DifferentPurposeDoesNotOpen(t *testing.T) {
	t.Parallel()

	other, err := NewSealer("another-purpose", secretA)
	require.NoError(t, err)

	sealed, err := newTestSealer(t, secretA).Seal([]byte("payload"), testAAD)
	require.NoError(t, err)

	opened, err := other.Open(sealed, testAAD)
	require.ErrorIs(t, err, ErrOpenFailed)
	assert.Nil(t, opened)
}

func TestSealer_Rotate(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t, secretA)

	underA, err := s.Seal([]byte("sealed under A"), testAAD)
	require.NoError(t, err)

	require.NoError(t, s.Rotate(secretB))

	opened, err := s.Open(underA, testAAD)
	require.NoError(t, err, "previous-generation payload must still open")
	assert.Equal(t, "sealed under A", string(opened))

	underB, err := s.Seal([]byte("sealed under B"), testAAD)
	require.NoError(t, err)

	opened, err = s.Open(underB, testAAD)
	require.NoError(t, err)
	assert.Equal(t, "sealed under B", string(opened))

	freshB := newTestSealer(t, secretB)

	opened, err = freshB.Open(underB, testAAD)
	require.NoError(t, err, "new seals are under B")
	assert.Equal(t, "sealed under B", string(opened))

	_, err = freshB.Open(underA, testAAD)
	require.ErrorIs(t, err, ErrOpenFailed, "a fresh B sealer holds no previous key")
}

func TestSealer_RotateTwiceDropsOldestGeneration(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t, secretA)

	underA, err := s.Seal([]byte("A"), testAAD)
	require.NoError(t, err)

	require.NoError(t, s.Rotate(secretB))

	underB, err := s.Seal([]byte("B"), testAAD)
	require.NoError(t, err)

	require.NoError(t, s.Rotate(secretC))

	_, err = s.Open(underA, testAAD)
	require.ErrorIs(t, err, ErrOpenFailed)

	opened, err := s.Open(underB, testAAD)
	require.NoError(t, err)
	assert.Equal(t, "B", string(opened))
}

func TestSealer_RotateToTheSameSecretIsANoOp(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t, secretA)

	underA, err := s.Seal([]byte("A"), testAAD)
	require.NoError(t, err)

	require.NoError(t, s.Rotate(secretB))
	require.NoError(t, s.Rotate(secretB), "a refresh that re-sends the current secret")

	opened, err := s.Open(underA, testAAD)
	require.NoError(t, err, "the previous generation must survive a same-secret rotation")
	assert.Equal(t, "A", string(opened))
}

func TestSealer_RotateToTheCurrentSecretKeepsNoPhantomPrevious(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t, secretB)
	require.NoError(t, s.Rotate(secretB))

	assert.Nil(t, s.keys.Load().previous, "a same-secret rotation must not mint a previous generation")
}

func TestSealer_RotateEmptyKeepsKeys(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t, secretA)
	require.NoError(t, s.Rotate(secretB))

	underA, err := newTestSealer(t, secretA).Seal([]byte("A"), testAAD)
	require.NoError(t, err)

	require.ErrorIs(t, s.Rotate(nil), ErrEmptySecret)
	require.ErrorIs(t, s.Rotate([]byte{}), ErrEmptySecret)

	_, err = s.Open(underA, testAAD)
	require.NoError(t, err, "previous generation survives a refused rotation")

	sealed, err := s.Seal([]byte("still B"), testAAD)
	require.NoError(t, err)

	_, err = newTestSealer(t, secretB).Open(sealed, testAAD)
	require.NoError(t, err, "current generation survives a refused rotation")
}

func TestSealer_ConcurrentRotateSealOpen(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t, secretA)
	secrets := [][]byte{secretA, secretB}

	const workers, iterations = 8, 200

	var wg sync.WaitGroup

	errs := make(chan error, workers*iterations+iterations)

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := range iterations {
			if err := s.Rotate(secrets[i%2]); err != nil {
				errs <- err
			}
		}
	}()

	for range workers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range iterations {
				sealed, err := s.Seal([]byte("payload"), testAAD)
				if err != nil {
					errs <- err

					continue
				}

				// Rotation alternates A and B, so the sealing key is always
				// either current or previous when Open runs.
				if _, err := s.Open(sealed, testAAD); err != nil {
					errs <- err
				}
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
}
