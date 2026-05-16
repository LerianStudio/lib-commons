//go:build unit

package outbox

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRetryClassifierFunc_NilFn covers the nil function guard.
func TestRetryClassifierFunc_NilFn(t *testing.T) {
	t.Parallel()

	var fn RetryClassifierFunc
	assert.False(t, fn.IsNonRetryable(errors.New("some error")))
}

// TestRetryClassifierFunc_NonNilFn covers the non-nil function path.
func TestRetryClassifierFunc_NonNilFn(t *testing.T) {
	t.Parallel()

	fn := RetryClassifierFunc(func(err error) bool {
		return err != nil
	})
	assert.True(t, fn.IsNonRetryable(errors.New("error")))
	assert.False(t, fn.IsNonRetryable(nil))
}
