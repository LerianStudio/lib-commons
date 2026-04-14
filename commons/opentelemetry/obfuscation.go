package opentelemetry

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/safe"
	"github.com/LerianStudio/lib-commons/v5/commons/security"
)

// RedactionAction defines how sensitive values are transformed.
type RedactionAction string

const (
	// RedactionMask replaces a sensitive value with the configured mask.
	RedactionMask RedactionAction = "mask"
	// RedactionHash replaces a sensitive value with an HMAC-SHA256 hash.
	RedactionHash RedactionAction = "hash"
	// RedactionDrop removes a sensitive field from the output.
	RedactionDrop RedactionAction = "drop"
)

// RedactionRule matches fields/paths and applies a redaction action.
type RedactionRule struct {
	FieldPattern string
	PathPattern  string
	Action       RedactionAction

	fieldRegex *regexp.Regexp
	pathRegex  *regexp.Regexp
}

// hmacKeySize is the byte length of the HMAC key generated for each Redactor.
const hmacKeySize = 32

// Redactor applies ordered redaction rules to structured payloads.
type Redactor struct {
	rules     []RedactionRule
	maskValue string
	hmacKey   []byte // per-instance key used by HMAC-SHA256 hashing
}

// NewDefaultRedactor builds a mask-based redactor from default sensitive fields.
func NewDefaultRedactor() *Redactor {
	fields := security.DefaultSensitiveFields()

	rules := make([]RedactionRule, 0, len(fields))
	for _, field := range fields {
		rules = append(rules, RedactionRule{FieldPattern: `(?i)^` + regexp.QuoteMeta(field) + `$`, Action: RedactionMask})
	}

	r, err := NewRedactor(rules, cn.ObfuscatedValue)
	if err != nil {
		// Rule compilation failed unexpectedly. Return a conservative always-mask
		// redactor rather than a no-rules redactor that would leak everything.
		return NewAlwaysMaskRedactor()
	}

	return r
}

// NewAlwaysMaskRedactor returns a conservative redactor that treats ALL fields as sensitive.
// This is used as a safe fallback when normal redactor construction fails, ensuring
// no data leaks through in fail-open scenarios.
func NewAlwaysMaskRedactor() *Redactor {
	return &Redactor{
		rules: []RedactionRule{
			{
				// Match every field name
				FieldPattern: ".*",
				fieldRegex:   regexp.MustCompile(".*"),
				Action:       RedactionMask,
			},
		},
		maskValue: cn.ObfuscatedValue,
	}
}

// NewRedactor compiles rules and returns a configured redactor.
func NewRedactor(rules []RedactionRule, maskValue string) (*Redactor, error) {
	if maskValue == "" {
		maskValue = cn.ObfuscatedValue
	}

	compiled := make([]RedactionRule, 0, len(rules))
	for i := range rules {
		rule := rules[i]
		if rule.Action == "" {
			rule.Action = RedactionMask
		}

		if rule.FieldPattern != "" {
			re, err := safe.Compile(rule.FieldPattern)
			if err != nil {
				return nil, fmt.Errorf("invalid redaction field pattern at index %d: %w", i, err)
			}

			rule.fieldRegex = re
		}

		if rule.PathPattern != "" {
			re, err := safe.Compile(rule.PathPattern)
			if err != nil {
				return nil, fmt.Errorf("invalid redaction path pattern at index %d: %w", i, err)
			}

			rule.pathRegex = re
		}

		compiled = append(compiled, rule)
	}

	key := make([]byte, hmacKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("failed to generate HMAC key: %w", err)
	}

	return &Redactor{rules: compiled, maskValue: maskValue, hmacKey: key}, nil
}

func (r *Redactor) actionFor(path, fieldName string) (RedactionAction, bool) {
	if r == nil {
		return "", false
	}

	for i := range r.rules {
		rule := r.rules[i]
		pathMatch := true

		var fieldMatch bool
		if rule.fieldRegex != nil {
			fieldMatch = rule.fieldRegex.MatchString(fieldName)
		} else {
			fieldMatch = security.IsSensitiveField(fieldName)
		}

		if rule.pathRegex != nil {
			pathMatch = rule.pathRegex.MatchString(path)
		}

		if fieldMatch && pathMatch {
			return rule.Action, true
		}
	}

	return "", false
}

// redactValue applies the first matching redaction rule to a field value.
// It returns the (possibly transformed) value, whether the field should be dropped,
// and whether any redaction rule was applied (so the caller can skip expensive comparison).
func (r *Redactor) redactValue(path, fieldName string, value any) (redacted any, drop bool, applied bool) {
	action, ok := r.actionFor(path, fieldName)
	if !ok {
		return value, false, false
	}

	switch action {
	case RedactionDrop:
		return nil, true, true
	case RedactionHash:
		return r.hashString(fmt.Sprint(value)), false, true
	case RedactionMask:
		fallthrough
	default:
		return r.maskValue, false, true
	}
}

// hashString computes an HMAC-SHA256 of v using the Redactor's per-instance key.
// The result is a hex-encoded string prefixed with "sha256:" for identification.
// Using HMAC prevents rainbow-table attacks against low-entropy PII.
func (r *Redactor) hashString(v string) string {
	if len(r.hmacKey) > 0 {
		mac := hmac.New(sha256.New, r.hmacKey)
		mac.Write([]byte(v))

		return "sha256:" + hex.EncodeToString(mac.Sum(nil))
	}

	// Fallback for zero-key edge case (should not happen with proper construction).
	h := sha256.Sum256([]byte(v))

	return fmt.Sprintf("sha256:%x", h)
}

// obfuscateStructFields recursively obfuscates sensitive fields in a struct or map.
func obfuscateStructFields(data any, path string, redactor *Redactor) any {
	switch v := data.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))

		for key, value := range v {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}

			if redactor != nil {
				redacted, drop, applied := redactor.redactValue(childPath, key, value)
				if drop {
					continue
				}

				if applied {
					result[key] = redacted
					continue
				}
			}

			result[key] = obfuscateStructFields(value, childPath, redactor)
		}

		return result

	case []any:
		result := make([]any, len(v))

		for i, item := range v {
			childPath := fmt.Sprintf("%s[%d]", path, i)
			result[i] = obfuscateStructFields(item, childPath, redactor)
		}

		return result

	default:
		return data
	}
}

// ObfuscateStruct applies obfuscation to a struct and returns the obfuscated data.
// This is a utility function that can be used independently of OpenTelemetry spans.
func ObfuscateStruct(valueStruct any, redactor *Redactor) (any, error) {
	if valueStruct == nil || redactor == nil {
		return valueStruct, nil
	}

	// Convert to JSON and back to get a generic representation.
	// Using any (not map[string]any) to handle arrays, primitives, and objects.
	jsonBytes, err := json.Marshal(valueStruct)
	if err != nil {
		return nil, err
	}

	var data any

	decoder := json.NewDecoder(bytes.NewReader(jsonBytes))
	decoder.UseNumber()

	if err := decoder.Decode(&data); err != nil {
		return nil, err
	}

	// Zero the intermediate buffer to minimize sensitive data lifetime in memory
	clear(jsonBytes)

	return obfuscateStructFields(data, "", redactor), nil
}
