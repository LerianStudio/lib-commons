//go:build unit

package safe

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFirst_Success(t *testing.T) {
	t.Parallel()

	slice := []int{1, 2, 3}

	result, err := First(slice)

	assert.NoError(t, err)
	assert.Equal(t, 1, result)
}

func TestFirst_EmptySlice(t *testing.T) {
	t.Parallel()

	slice := []int{}

	result, err := First(slice)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptySlice)
	assert.Equal(t, 0, result)
}

func TestFirst_SingleElement(t *testing.T) {
	t.Parallel()

	slice := []string{"only"}

	result, err := First(slice)

	assert.NoError(t, err)
	assert.Equal(t, "only", result)
}

func TestLast_Success(t *testing.T) {
	t.Parallel()

	slice := []int{1, 2, 3}

	result, err := Last(slice)

	assert.NoError(t, err)
	assert.Equal(t, 3, result)
}

func TestLast_EmptySlice(t *testing.T) {
	t.Parallel()

	slice := []int{}

	result, err := Last(slice)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptySlice)
	assert.Equal(t, 0, result)
}

func TestLast_SingleElement(t *testing.T) {
	t.Parallel()

	slice := []string{"only"}

	result, err := Last(slice)

	assert.NoError(t, err)
	assert.Equal(t, "only", result)
}

func TestAt_Success(t *testing.T) {
	t.Parallel()

	slice := []int{10, 20, 30}

	result, err := At(slice, 1)

	assert.NoError(t, err)
	assert.Equal(t, 20, result)
}

func TestAt_FirstIndex(t *testing.T) {
	t.Parallel()

	slice := []int{10, 20, 30}

	result, err := At(slice, 0)

	assert.NoError(t, err)
	assert.Equal(t, 10, result)
}

func TestAt_LastIndex(t *testing.T) {
	t.Parallel()

	slice := []int{10, 20, 30}

	result, err := At(slice, 2)

	assert.NoError(t, err)
	assert.Equal(t, 30, result)
}

func TestAt_NegativeIndex(t *testing.T) {
	t.Parallel()

	slice := []int{10, 20, 30}

	result, err := At(slice, -1)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrIndexOutOfBounds)
	assert.Equal(t, 0, result)
}

func TestAt_IndexTooLarge(t *testing.T) {
	t.Parallel()

	slice := []int{10, 20, 30}

	result, err := At(slice, 5)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrIndexOutOfBounds)
	assert.Equal(t, 0, result)
}

func TestAt_EmptySlice(t *testing.T) {
	t.Parallel()

	slice := []int{}

	result, err := At(slice, 0)

	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrIndexOutOfBounds)
	assert.Equal(t, 0, result)
}

func TestFirstOrDefault_Success(t *testing.T) {
	t.Parallel()

	slice := []int{1, 2, 3}

	result := FirstOrDefault(slice, 99)

	assert.Equal(t, 1, result)
}

func TestFirstOrDefault_EmptySlice(t *testing.T) {
	t.Parallel()

	slice := []int{}

	result := FirstOrDefault(slice, 99)

	assert.Equal(t, 99, result)
}

func TestLastOrDefault_Success(t *testing.T) {
	t.Parallel()

	slice := []int{1, 2, 3}

	result := LastOrDefault(slice, 99)

	assert.Equal(t, 3, result)
}

func TestLastOrDefault_EmptySlice(t *testing.T) {
	t.Parallel()

	slice := []int{}

	result := LastOrDefault(slice, 99)

	assert.Equal(t, 99, result)
}

func TestAtOrDefault_Success(t *testing.T) {
	t.Parallel()

	slice := []int{10, 20, 30}

	result := AtOrDefault(slice, 1, 99)

	assert.Equal(t, 20, result)
}

func TestAtOrDefault_OutOfBounds(t *testing.T) {
	t.Parallel()

	slice := []int{10, 20, 30}

	result := AtOrDefault(slice, 5, 99)

	assert.Equal(t, 99, result)
}

func TestAtOrDefault_NegativeIndex(t *testing.T) {
	t.Parallel()

	slice := []int{10, 20, 30}

	result := AtOrDefault(slice, -1, 99)

	assert.Equal(t, 99, result)
}
