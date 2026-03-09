//go:build unit

package commons

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContains(t *testing.T) {
	t.Parallel()

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		assert.True(t, Contains([]string{"a", "b", "c"}, "b"))
	})

	t.Run("not_found", func(t *testing.T) {
		t.Parallel()
		assert.False(t, Contains([]string{"a", "b", "c"}, "z"))
	})

	t.Run("empty_slice", func(t *testing.T) {
		t.Parallel()
		assert.False(t, Contains([]int{}, 1))
	})
}

func TestCheckMetadataKeyAndValueLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		limit    int
		metadata map[string]any
		wantErr  string
	}{
		{
			name:     "key_too_long",
			limit:    3,
			metadata: map[string]any{"toolong": "v"},
			wantErr:  "0050",
		},
		{
			name:     "int_value",
			limit:    10,
			metadata: map[string]any{"k": 42},
		},
		{
			name:     "float64_value",
			limit:    20,
			metadata: map[string]any{"k": 3.14},
		},
		{
			name:     "string_value_within_limit",
			limit:    10,
			metadata: map[string]any{"k": "short"},
		},
		{
			name:     "string_value_too_long",
			limit:    3,
			metadata: map[string]any{"k": "toolong"},
			wantErr:  "0051",
		},
		{
			name:     "bool_value",
			limit:    10,
			metadata: map[string]any{"k": true},
		},
		{
			name:     "nil_value_skipped",
			limit:    1,
			metadata: map[string]any{"k": nil},
		},
		{
			name:     "unknown_type",
			limit:    10,
			metadata: map[string]any{"k": []int{1, 2}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := CheckMetadataKeyAndValueLength(tc.limit, tc.metadata)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, tc.wantErr, err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSafeIntToUint64(t *testing.T) {
	t.Parallel()

	t.Run("negative_returns_1", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, uint64(1), SafeIntToUint64(-5))
	})

	t.Run("positive", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, uint64(42), SafeIntToUint64(42))
	})

	t.Run("zero", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, uint64(0), SafeIntToUint64(0))
	})
}

func TestSafeInt64ToInt(t *testing.T) {
	t.Parallel()

	t.Run("normal", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 100, SafeInt64ToInt(100))
	})

	t.Run("overflow_max", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, math.MaxInt, SafeInt64ToInt(math.MaxInt64))
	})

	t.Run("underflow_min", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, math.MinInt, SafeInt64ToInt(math.MinInt64))
	})
}

func TestSafeUintToInt(t *testing.T) {
	t.Parallel()

	t.Run("normal", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 10, SafeUintToInt(10))
	})

	t.Run("overflow", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, math.MaxInt, SafeUintToInt(uint(math.MaxUint)))
	})
}

func TestSafeIntToUint32(t *testing.T) {
	t.Parallel()

	t.Run("negative_returns_default", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, uint32(99), SafeIntToUint32(-1, 99, nil, "test"))
	})

	t.Run("overflow_returns_default", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, uint32(99), SafeIntToUint32(math.MaxInt, 99, nil, "test"))
	})

	t.Run("normal", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, uint32(42), SafeIntToUint32(42, 0, nil, "test"))
	})

	t.Run("negative_with_logger", func(t *testing.T) {
		t.Parallel()
		logger := &log.NopLogger{}
		assert.Equal(t, uint32(99), SafeIntToUint32(-1, 99, logger, "field"))
	})

	t.Run("overflow_with_logger", func(t *testing.T) {
		t.Parallel()
		logger := &log.NopLogger{}
		assert.Equal(t, uint32(99), SafeIntToUint32(math.MaxInt, 99, logger, "field"))
	})
}

func TestIsUUID(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		assert.True(t, IsUUID("550e8400-e29b-41d4-a716-446655440000"))
	})

	t.Run("invalid", func(t *testing.T) {
		t.Parallel()
		assert.False(t, IsUUID("not-a-uuid"))
	})
}

func TestGenerateUUIDv7(t *testing.T) {
	t.Parallel()

	id, err := GenerateUUIDv7()
	require.NoError(t, err)
	assert.True(t, IsUUID(id.String()))
}

func TestStructToJSONString(t *testing.T) {
	t.Parallel()

	t.Run("valid_struct", func(t *testing.T) {
		t.Parallel()

		s := struct {
			Name string `json:"name"`
		}{Name: "test"}

		result, err := StructToJSONString(s)
		require.NoError(t, err)
		assert.Equal(t, `{"name":"test"}`, result)
	})

	t.Run("invalid_value", func(t *testing.T) {
		t.Parallel()

		_, err := StructToJSONString(make(chan int))
		assert.Error(t, err)
	})
}

func TestMergeMaps(t *testing.T) {
	t.Parallel()

	t.Run("nil_target", func(t *testing.T) {
		t.Parallel()

		result := MergeMaps(map[string]any{"a": 1}, nil)
		assert.Equal(t, 1, result["a"])
	})

	t.Run("nil_value_deletes_key", func(t *testing.T) {
		t.Parallel()

		target := map[string]any{"a": 1, "b": 2}
		result := MergeMaps(map[string]any{"a": nil}, target)
		_, exists := result["a"]
		assert.False(t, exists)
		assert.Equal(t, 2, result["b"])
	})

	t.Run("normal_merge", func(t *testing.T) {
		t.Parallel()

		target := map[string]any{"a": 1}
		result := MergeMaps(map[string]any{"b": 2}, target)
		assert.Equal(t, 1, result["a"])
		assert.Equal(t, 2, result["b"])
	})
}

func TestReverse(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, Reverse([]int{}))
	})

	t.Run("single", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, []int{1}, Reverse([]int{1}))
	})

	t.Run("multiple", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, []int{3, 2, 1}, Reverse([]int{1, 2, 3}))
	})
}

func TestUUIDsToStrings(t *testing.T) {
	t.Parallel()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, UUIDsToStrings([]uuid.UUID{}))
	})

	t.Run("multiple", func(t *testing.T) {
		t.Parallel()

		u1 := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
		u2 := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")

		result := UUIDsToStrings([]uuid.UUID{u1, u2})
		assert.Equal(t, []string{u1.String(), u2.String()}, result)
	})
}

func TestIsInternalLerianService(t *testing.T) {
	t.Parallel()

	t.Run("matching", func(t *testing.T) {
		t.Parallel()
		assert.True(t, IsInternalLerianService("my-service/1.0.0 LerianStudio"))
	})

	t.Run("non_matching", func(t *testing.T) {
		t.Parallel()
		assert.False(t, IsInternalLerianService("curl/7.68.0"))
	})
}

func TestGetCPUUsage_NilFactory(t *testing.T) {
	t.Parallel()

	// Should not panic when factory is nil; metrics recording is skipped.
	assert.NotPanics(t, func() {
		GetCPUUsage(context.Background(), nil)
	})
}

func TestGetMemUsage_NilFactory(t *testing.T) {
	t.Parallel()

	// Should not panic when factory is nil; metrics recording is skipped.
	assert.NotPanics(t, func() {
		GetMemUsage(context.Background(), nil)
	})
}

func TestGetMapNumKinds(t *testing.T) {
	t.Parallel()

	kinds := GetMapNumKinds()

	expected := []reflect.Kind{
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Float32, reflect.Float64,
	}

	assert.Len(t, kinds, len(expected))

	for _, k := range expected {
		assert.True(t, kinds[k], "expected kind %v to be present", k)
	}
}
