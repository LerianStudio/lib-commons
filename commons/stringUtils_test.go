package commons

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRemoveAccents(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:    "basic accents",
			input:   "áéíóú",
			want:    "aeiou",
			wantErr: false,
		},
		{
			name:    "uppercase accents",
			input:   "ÁÉÍÓÚ",
			want:    "AEIOU",
			wantErr: false,
		},
		{
			name:    "mixed case with accents",
			input:   "São Paulo",
			want:    "Sao Paulo",
			wantErr: false,
		},
		{
			name:    "portuguese accents",
			input:   "àãâêôõçÇ",
			want:    "aaaeoocC",
			wantErr: false,
		},
		{
			name:    "no accents",
			input:   "Hello World",
			want:    "Hello World",
			wantErr: false,
		},
		{
			name:    "empty string",
			input:   "",
			want:    "",
			wantErr: false,
		},
		{
			name:    "numbers and special chars",
			input:   "café123!@#",
			want:    "cafe123!@#",
			wantErr: false,
		},
		{
			name:    "unicode diacritics",
			input:   "naïve résumé",
			want:    "naive resume",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RemoveAccents(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestRemoveSpaces(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single spaces",
			input: "hello world",
			want:  "helloworld",
		},
		{
			name:  "multiple spaces",
			input: "hello   world",
			want:  "helloworld",
		},
		{
			name:  "leading and trailing spaces",
			input: "  hello world  ",
			want:  "helloworld",
		},
		{
			name:  "tabs and newlines",
			input: "hello\tworld\n",
			want:  "helloworld",
		},
		{
			name:  "no spaces",
			input: "helloworld",
			want:  "helloworld",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only spaces",
			input: "   \t\n   ",
			want:  "",
		},
		{
			name:  "unicode spaces",
			input: "hello world", // contains non-breaking space
			want:  "helloworld",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemoveSpaces(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsNilOrEmpty(t *testing.T) {
	tests := []struct {
		name  string
		input *string
		want  bool
	}{
		{
			name:  "nil pointer",
			input: nil,
			want:  true,
		},
		{
			name:  "empty string",
			input: strPtr(""),
			want:  true,
		},
		{
			name:  "spaces only",
			input: strPtr("   "),
			want:  true,
		},
		{
			name:  "null string",
			input: strPtr("null"),
			want:  true,
		},
		{
			name:  "nil string",
			input: strPtr("nil"),
			want:  true,
		},
		{
			name:  "null with spaces",
			input: strPtr("  null  "),
			want:  true,
		},
		{
			name:  "nil with spaces",
			input: strPtr("  nil  "),
			want:  true,
		},
		{
			name:  "non-empty string",
			input: strPtr("hello"),
			want:  false,
		},
		{
			name:  "string with spaces",
			input: strPtr("  hello  "),
			want:  false,
		},
		{
			name:  "zero string",
			input: strPtr("0"),
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNilOrEmpty(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCamelToSnakeCase(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple camelCase",
			input: "camelCase",
			want:  "camel_case",
		},
		{
			name:  "PascalCase",
			input: "PascalCase",
			want:  "pascal_case",
		},
		{
			name:  "multiple uppercase",
			input: "HTTPServerError",
			want:  "h_t_t_p_server_error",
		},
		{
			name:  "lowercase only",
			input: "lowercase",
			want:  "lowercase",
		},
		{
			name:  "single char",
			input: "A",
			want:  "a",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "with numbers",
			input: "getUserByID123",
			want:  "get_user_by_i_d123",
		},
		{
			name:  "consecutive capitals",
			input: "XMLHttpRequest",
			want:  "x_m_l_http_request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CamelToSnakeCase(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRegexIgnoreAccents(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple char",
			input: "a",
			want:  "[aáàãâ]",
		},
		{
			name:  "uppercase char",
			input: "A",
			want:  "[AÁÀÃÂ]",
		},
		{
			name:  "word with accents",
			input: "café",
			want:  "[cç][aáàãâ]f[eéèê]",
		},
		{
			name:  "mixed case",
			input: "CaFe",
			want:  "[CÇ][aáàãâ]F[eéèê]",
		},
		{
			name:  "no accent chars",
			input: "xyz",
			want:  "xyz",
		},
		{
			name:  "numbers and special",
			input: "a1b2!",
			want:  "[aáàãâ]1b2!",
		},
		{
			name:  "all vowels",
			input: "aeiouAEIOU",
			want:  "[aáàãâ][eéèê][iíìî][oóòõô][uùúû][AÁÀÃÂ][EÉÈÊ][IÍÌÎ][OÓÒÕÔ][UÙÚÛ]",
		},
		{
			name:  "accented input",
			input: "áéíóúç",
			want:  "[aáàãâ][eéèê][iíìî][oóòõô][uùúû][cç]",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RegexIgnoreAccents(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRemoveChars(t *testing.T) {
	tests := []struct {
		name  string
		str   string
		chars map[string]bool
		want  string
	}{
		{
			name: "remove vowels",
			str:  "hello world",
			chars: map[string]bool{
				"a": true,
				"e": true,
				"i": true,
				"o": true,
				"u": true,
			},
			want: "hll wrld",
		},
		{
			name: "remove numbers",
			str:  "abc123xyz",
			chars: map[string]bool{
				"1": true,
				"2": true,
				"3": true,
			},
			want: "abcxyz",
		},
		{
			name:  "empty chars map",
			str:   "hello",
			chars: map[string]bool{},
			want:  "hello",
		},
		{
			name: "remove all chars",
			str:  "abc",
			chars: map[string]bool{
				"a": true,
				"b": true,
				"c": true,
			},
			want: "",
		},
		{
			name:  "empty string",
			str:   "",
			chars: map[string]bool{"a": true},
			want:  "",
		},
		{
			name: "remove special chars",
			str:  "hello!@#world",
			chars: map[string]bool{
				"!": true,
				"@": true,
				"#": true,
			},
			want: "helloworld",
		},
		{
			name:  "nil map",
			str:   "hello",
			chars: nil,
			want:  "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RemoveChars(tt.str, tt.chars)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReplaceUUIDWithPlaceholder(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single UUID",
			input: "/users/550e8400-e29b-41d4-a716-446655440000/profile",
			want:  "/users/:id/profile",
		},
		{
			name:  "multiple UUIDs",
			input: "/orgs/550e8400-e29b-41d4-a716-446655440000/users/550e8400-e29b-41d4-a716-446655440001",
			want:  "/orgs/:id/users/:id",
		},
		{
			name:  "no UUID",
			input: "/users/123/profile",
			want:  "/users/123/profile",
		},
		{
			name:  "uppercase UUID",
			input: "/users/550E8400-E29B-41D4-A716-446655440000",
			want:  "/users/:id",
		},
		{
			name:  "UUID without hyphens in path",
			input: "/users/550e8400e29b41d4a716446655440000",
			want:  "/users/550e8400e29b41d4a716446655440000",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "UUID at start",
			input: "550e8400-e29b-41d4-a716-446655440000/profile",
			want:  ":id/profile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReplaceUUIDWithPlaceholder(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateServerAddress(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "valid address with port",
			input: "localhost:8080",
			want:  "localhost:8080",
		},
		{
			name:  "valid IP with port",
			input: "192.168.1.1:3000",
			want:  "192.168.1.1:3000",
		},
		{
			name:  "domain with port",
			input: "example.com:443",
			want:  "example.com:443",
		},
		{
			name:  "no port",
			input: "localhost",
			want:  "",
		},
		{
			name:  "port only",
			input: ":8080",
			want:  "",
		},
		{
			name:  "multiple colons",
			input: "host:port:extra",
			want:  "",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "non-numeric port",
			input: "localhost:abc",
			want:  "",
		},
		{
			name:  "wildcard address",
			input: "0.0.0.0:8080",
			want:  "0.0.0.0:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateServerAddress(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHashSHA256(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "simple string",
			input: "hello",
		},
		{
			name:  "empty string",
			input: "",
		},
		{
			name:  "long string",
			input: "The quick brown fox jumps over the lazy dog",
		},
		{
			name:  "special characters",
			input: "!@#$%^&*()_+-=[]{}|;':\",./<>?",
		},
		{
			name:  "unicode",
			input: "こんにちは世界",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HashSHA256(tt.input)
			// SHA256 produces 64 character hex string
			assert.Len(t, got, 64)
			// Verify it's valid hex
			assert.Regexp(t, "^[a-f0-9]{64}$", got)
			// Same input should produce same hash
			got2 := HashSHA256(tt.input)
			assert.Equal(t, got, got2)
		})
	}

	// Test known hash values
	t.Run("known hash values", func(t *testing.T) {
		// Known SHA256 hashes
		assert.Equal(t, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", HashSHA256("hello"))
		assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", HashSHA256(""))
	})
}

func TestStringToInt(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "valid positive number",
			input: "42",
			want:  42,
		},
		{
			name:  "valid negative number",
			input: "-42",
			want:  -42,
		},
		{
			name:  "zero",
			input: "0",
			want:  0,
		},
		{
			name:  "invalid string",
			input: "abc",
			want:  100,
		},
		{
			name:  "empty string",
			input: "",
			want:  100,
		},
		{
			name:  "float string",
			input: "42.5",
			want:  100,
		},
		{
			name:  "number with spaces",
			input: " 42 ",
			want:  100,
		},
		{
			name:  "large number",
			input: "999999999",
			want:  999999999,
		},
		{
			name:  "leading zeros",
			input: "00042",
			want:  42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StringToInt(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Helper function to create string pointers
func strPtr(s string) *string {
	return &s
}
