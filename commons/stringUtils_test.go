//go:build unit

package commons

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoveAccents(t *testing.T) {
	t.Parallel()

	t.Run("accented", func(t *testing.T) {
		t.Parallel()

		result, err := RemoveAccents("café résumé")
		require.NoError(t, err)
		assert.Equal(t, "cafe resume", result)
	})

	t.Run("plain_text", func(t *testing.T) {
		t.Parallel()

		result, err := RemoveAccents("hello world")
		require.NoError(t, err)
		assert.Equal(t, "hello world", result)
	})
}

func TestRemoveSpaces(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"spaces", "a b c", "abc"},
		{"tabs", "a\tb\tc", "abc"},
		{"mixed", " a \t b \n c ", "abc"},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, RemoveSpaces(tc.input))
		})
	}
}

func TestIsNilOrEmpty(t *testing.T) {
	t.Parallel()

	s := func(v string) *string { return &v }

	tests := []struct {
		name string
		val  *string
		want bool
	}{
		{"nil", nil, true},
		{"empty", s(""), true},
		{"whitespace", s("   "), true},
		{"null_string", s("null"), true},
		{"nil_string", s("nil"), true},
		{"valid", s("hello"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, IsNilOrEmpty(tc.val))
		})
	}
}

func TestCamelToSnakeCase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "CamelCase", "camel_case"},
		{"lower", "already", "already"},
		{"multiple_upper", "HTTPServer", "h_t_t_p_server"},
		{"empty", "", ""},
		{"single_upper", "A", "a"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, CamelToSnakeCase(tc.input))
		})
	}
}

func TestRegexIgnoreAccents(t *testing.T) {
	t.Parallel()

	t.Run("accented_input", func(t *testing.T) {
		t.Parallel()

		result := RegexIgnoreAccents("café")
		assert.Contains(t, result, "[cç]")
		assert.Contains(t, result, "[aáàãâ]")
		assert.Contains(t, result, "[eéèê]")
	})

	t.Run("plain_input", func(t *testing.T) {
		t.Parallel()

		result := RegexIgnoreAccents("abc")
		assert.Contains(t, result, "[aáàãâ]")
		assert.Contains(t, result, "[cç]")
	})
}

func TestRemoveChars(t *testing.T) {
	t.Parallel()

	chars := map[string]bool{"-": true, ".": true}
	assert.Equal(t, "abc", RemoveChars("a-b.c", chars))
}

func TestReplaceUUIDWithPlaceholder(t *testing.T) {
	t.Parallel()

	path := "/api/v1/550e8400-e29b-41d4-a716-446655440000/items"
	assert.Equal(t, "/api/v1/:id/items", ReplaceUUIDWithPlaceholder(path))
}

func TestValidateServerAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"valid_hostname", "localhost:8080", "localhost:8080"},
		{"valid_ip", "192.168.1.1:443", "192.168.1.1:443"},
		{"valid_ipv6_bracketed", "[::1]:8080", "[::1]:8080"},
		{"valid_ipv6_full", "[2001:db8::1]:9090", "[2001:db8::1]:9090"},
		{"valid_port_1", "host:1", "host:1"},
		{"valid_port_65535", "host:65535", "host:65535"},
		{"invalid_no_port", "localhost", ""},
		{"invalid_empty", "", ""},
		{"invalid_port_0", "host:0", ""},
		{"invalid_port_65536", "host:65536", ""},
		{"invalid_port_negative", "host:-1", ""},
		{"invalid_port_non_numeric", "host:abc", ""},
		{"invalid_no_host", ":8080", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ValidateServerAddress(tc.input))
		})
	}
}

func TestHashSHA256(t *testing.T) {
	t.Parallel()

	h1 := HashSHA256("hello")
	h2 := HashSHA256("hello")

	assert.Equal(t, h1, h2)
	assert.Len(t, h1, 64) // SHA-256 hex is 64 chars
}

func TestHashSHA256Bytes_Input_ReturnsLowercaseHexDigest(t *testing.T) {
	t.Parallel()

	largeInput := bytes.Repeat([]byte{0x00, 0x7f, 0x80, 0xff}, 256*1024)
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{name: "text parity", input: []byte("hello")},
		{name: "binary data with NUL bytes", input: []byte{0x00, 0x01, 0x02, 0xff, 0x00}},
		{name: "nil input", input: nil, want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{name: "empty input", input: []byte{}, want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{name: "large buffer", input: largeInput},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			before := bytes.Clone(test.input)
			got := HashSHA256Bytes(test.input)

			assert.Equal(t, HashSHA256(string(test.input)), got)
			assert.Len(t, got, 64)
			assert.Equal(t, strings.ToLower(got), got)
			assert.Equal(t, before, test.input)
			if test.want != "" {
				assert.Equal(t, test.want, got)
			}
		})
	}
}

func BenchmarkHashSHA256Bytes_LargeBuffer_NoBodySizeCopy(b *testing.B) {
	input := bytes.Repeat([]byte{0xa5}, 1024*1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()

	for b.Loop() {
		HashSHA256Bytes(input)
	}
}

func TestStringToInt(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 42, StringToInt("42"))
	})

	t.Run("invalid_returns_100", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 100, StringToInt("not_a_number"))
	})
}
