//go:build unit

package nilcheck

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type sampleStruct struct{}

type sampleInterface interface {
	Do()
}

type sampleImpl struct{}

func (*sampleImpl) Do() {}

func TestInterface(t *testing.T) {
	t.Parallel()

	var nilPointer *sampleStruct
	var nilSlice []string
	var nilMap map[string]string
	var nilChan chan int
	var nilFunc func()
	var nilIface sampleInterface

	var typedNilIface sampleInterface
	var typedImpl *sampleImpl
	typedNilIface = typedImpl

	require.True(t, Interface(nil))
	require.True(t, Interface(nilPointer))
	require.True(t, Interface(nilSlice))
	require.True(t, Interface(nilMap))
	require.True(t, Interface(nilChan))
	require.True(t, Interface(nilFunc))
	require.True(t, Interface(nilIface))
	require.True(t, Interface(typedNilIface))

	require.False(t, Interface(0))
	require.False(t, Interface(""))
	require.False(t, Interface(sampleStruct{}))
	require.False(t, Interface(&sampleStruct{}))
	require.False(t, Interface([]string{}))
}
