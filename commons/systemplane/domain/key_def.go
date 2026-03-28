// Copyright 2025 Lerian Studio.

package domain

import (
	"errors"
	"fmt"
	"regexp"
)

// ValueType classifies the data type of a configuration value.
type ValueType string

// Supported ValueType values.
const (
	ValueTypeString ValueType = "string"
	ValueTypeInt    ValueType = "int"
	ValueTypeBool   ValueType = "bool"
	ValueTypeFloat  ValueType = "float"
	ValueTypeObject ValueType = "object"
	ValueTypeArray  ValueType = "array"
)

// ErrInvalidValueType indicates an invalid value type.
var ErrInvalidValueType = errors.New("invalid value type")

// ErrInvalidRedactPolicy indicates an invalid redact policy.
var ErrInvalidRedactPolicy = errors.New("invalid redact policy")

// ErrInvalidEnvVar indicates an invalid environment variable name.
var ErrInvalidEnvVar = errors.New("invalid env var")

var envVarPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// IsValid reports whether the value type is supported.
func (vt ValueType) IsValid() bool {
	switch vt {
	case ValueTypeString, ValueTypeInt, ValueTypeBool, ValueTypeFloat:
		return true
	case ValueTypeObject, ValueTypeArray:
		return true
	}

	return false
}

// ParseValueType parses a string into a ValueType.
func ParseValueType(s string) (ValueType, error) {
	vt := ValueType(s)
	if !vt.IsValid() {
		return "", fmt.Errorf("parse %q: %w", s, ErrInvalidValueType)
	}

	return vt, nil
}

// RedactPolicy controls how a key's value is displayed in non-privileged
// contexts (e.g., audit logs, API responses without elevated permissions).
type RedactPolicy string

// Supported RedactPolicy values.
const (
	RedactNone RedactPolicy = "none"
	RedactFull RedactPolicy = "full"
	RedactMask RedactPolicy = "mask"
)

// IsValid reports whether the redact policy is supported.
// The zero value is treated as valid for backward compatibility and is
// interpreted the same as RedactNone by the service layer.
func (rp RedactPolicy) IsValid() bool {
	switch rp {
	case "", RedactNone, RedactFull, RedactMask:
		return true
	default:
		return false
	}
}

// ValidatorFunc is a custom validation function for a key's value. It returns
// a non-nil error when the value is invalid.
type ValidatorFunc func(value any) error

// ComponentNone is a sentinel value for keys that do not require any
// infrastructure component rebuild when changed. Use this for pure
// business-logic keys (rate limits, worker intervals, archival settings)
// whose changes are picked up through the config snapshot without
// reconnecting infrastructure.
const ComponentNone = "_none"

// KeyDef carries all registry metadata for a configuration key. It describes
// the key's type, visibility, constraints, and runtime behavior.
type KeyDef struct {
	Key              string
	EnvVar           string
	Kind             Kind
	AllowedScopes    []Scope
	DefaultValue     any
	ValueType        ValueType
	Validator        ValidatorFunc
	Secret           bool
	RedactPolicy     RedactPolicy
	ApplyBehavior    ApplyBehavior
	MutableAtRuntime bool
	Description      string
	Group            string

	// Component declares which infrastructure component this key affects
	// (e.g., "postgres", "redis", "rabbitmq", "s3", "http", "logger").
	// Use ComponentNone ("_none") for pure business-logic keys that require
	// no infrastructure rebuild when changed. An empty string means the key
	// is not yet classified — the diff function treats unclassified keys as
	// cross-cutting and forces a full rebuild for safety.
	//
	// This field is used by BundleFactory implementations to determine which
	// runtime resources need rebuilding when a key changes, enabling
	// component-granular bundle rebuilds instead of all-or-nothing swaps.
	Component string
}

// Validate checks that the KeyDef itself is well-formed. It does not validate
// any particular value; use the Validator field for that.
func (keyDef KeyDef) Validate() error {
	if keyDef.Key == "" {
		return fmt.Errorf("key def: key must not be empty: %w", ErrKeyUnknown)
	}

	if !keyDef.Kind.IsValid() {
		return fmt.Errorf("key def %q kind %q: %w", keyDef.Key, keyDef.Kind, ErrInvalidKind)
	}

	if len(keyDef.AllowedScopes) == 0 {
		return fmt.Errorf("key def %q: at least one allowed scope required: %w", keyDef.Key, ErrScopeInvalid)
	}

	for _, s := range keyDef.AllowedScopes {
		if !s.IsValid() {
			return fmt.Errorf("key def %q scope %q: %w", keyDef.Key, s, ErrInvalidScope)
		}
	}

	if !keyDef.ValueType.IsValid() {
		return fmt.Errorf("key def %q value type %q: %w", keyDef.Key, keyDef.ValueType, ErrInvalidValueType)
	}

	if !keyDef.RedactPolicy.IsValid() {
		return fmt.Errorf("key def %q redact policy %q: %w", keyDef.Key, keyDef.RedactPolicy, ErrInvalidRedactPolicy)
	}

	if keyDef.EnvVar != "" && !envVarPattern.MatchString(keyDef.EnvVar) {
		return fmt.Errorf("key def %q env var %q: %w", keyDef.Key, keyDef.EnvVar, ErrInvalidEnvVar)
	}

	// Secret keys are always treated as RedactFull at runtime (see
	// normalizeRedactPolicy in catalog/validate.go), so any RedactPolicy
	// declared on a secret key is acceptable — no error needed here.

	if !keyDef.ApplyBehavior.IsValid() {
		return fmt.Errorf("key def %q apply behavior %q: %w", keyDef.Key, keyDef.ApplyBehavior, ErrInvalidApplyBehavior)
	}

	return nil
}
