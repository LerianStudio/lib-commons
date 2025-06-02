package transaction

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitAlias(t *testing.T) {
	tests := []struct {
		name  string
		alias string
		want  string
	}{
		{
			name:  "alias with hash",
			alias: "0#account1",
			want:  "account1",
		},
		{
			name:  "alias without hash",
			alias: "account1",
			want:  "account1",
		},
		{
			name:  "alias with multiple hashes",
			alias: "0#account#with#hash",
			want:  "account#with#hash",
		},
		{
			name:  "empty alias",
			alias: "",
			want:  "",
		},
		{
			name:  "only hash",
			alias: "#",
			want:  "",
		},
		{
			name:  "hash at the end",
			alias: "account#",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitAlias(tt.alias)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConcatAlias(t *testing.T) {
	tests := []struct {
		name  string
		index int
		alias string
		want  string
	}{
		{
			name:  "basic concatenation",
			index: 0,
			alias: "account1",
			want:  "0#account1",
		},
		{
			name:  "negative index",
			index: -1,
			alias: "account1",
			want:  "-1#account1",
		},
		{
			name:  "large index",
			index: 999999,
			alias: "account1",
			want:  "999999#account1",
		},
		{
			name:  "empty alias",
			index: 5,
			alias: "",
			want:  "5#",
		},
		{
			name:  "alias with special characters",
			index: 10,
			alias: "@user:domain.com",
			want:  "10#@user:domain.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConcatAlias(tt.index, tt.alias)
			assert.Equal(t, tt.want, got)
		})
	}
}
