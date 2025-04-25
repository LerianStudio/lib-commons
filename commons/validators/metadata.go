package validators

import (
	"errors"
)

// MetadataConfig defines configuration for metadata validation
type MetadataConfig struct {
	// MaxMetadataSize defines the maximum size of metadata in bytes
	MaxMetadataSize int

	// MaxStringLength defines the maximum length for string fields in metadata
	MaxStringLength int

	// MaxKeyLength defines the maximum length for metadata keys
	MaxKeyLength int

	// StrictMode enables additional validation checks
	StrictMode bool
}

// DefaultMetadataConfig returns a default configuration for metadata validation
func DefaultMetadataConfig() MetadataConfig {
	return MetadataConfig{
		MaxMetadataSize: 8192,  // 8KB
		MaxStringLength: 1024,  // 1KB
		MaxKeyLength:    64,
		StrictMode:      false,
	}
}

// ValidateMetadata validates metadata using default configuration
func ValidateMetadata(metadata map[string]any) error {
	return ValidateMetadataWithConfig(metadata, DefaultMetadataConfig())
}

// ValidateMetadataWithConfig validates metadata with custom configuration
func ValidateMetadataWithConfig(metadata map[string]any, config MetadataConfig) error {
	if metadata == nil {
		return nil // Empty metadata is valid
	}

	// Validate metadata keys and values
	for key, value := range metadata {
		if err := validateMetadataKey(key, config.MaxKeyLength); err != nil {
			return err
		}

		if err := validateMetadataValue(key, value, config); err != nil {
			return err
		}
	}

	// Check total metadata size
	if err := validateMetadataSize(metadata, config.MaxMetadataSize); err != nil {
		return err
	}

	return nil
}

// validateMetadataKey validates a single metadata key
func validateMetadataKey(key string, maxLength int) error {
	if key == "" {
		return errors.New("0071") // Metadata key cannot be empty
	}

	if len(key) > maxLength {
		return errors.New("0072") // Metadata key exceeds maximum length
	}

	return nil
}

// validateMetadataValue validates a single metadata value
func validateMetadataValue(_ string, value any, config MetadataConfig) error {
	// Validate value type
	if !isValidMetadataValueType(value) {
		return errors.New("0078") // Unsupported metadata value type (updated from 0073)
	}

	// Check string value length
	if strValue, ok := value.(string); ok {
		if len(strValue) > config.MaxStringLength {
			return errors.New("0079") // Metadata string value exceeds maximum length (updated from 0074)
		}

		return nil
	}

	// If strict mode is enabled, validate numeric ranges
	if config.StrictMode {
		switch value := value.(type) {
		case int:
			if value < -9999999999 || value > 9999999999 {
				return errors.New("0082") // Metadata integer value outside allowed range (updated from 0075)
			}
		case float64:
			if value < -9999999999.0 || value > 9999999999.0 {
				return errors.New("0083") // Metadata float value outside allowed range (updated from 0076)
			}
		}
	}

	return nil
}

// validateMetadataSize validates the total size of metadata
func validateMetadataSize(metadata map[string]any, maxSize int) error {
	totalSize := 0
	for key, value := range metadata {
		totalSize += len(key)
		switch val := value.(type) {
		case string:
			totalSize += len(val)
		case bool, int, float64:
			totalSize += 8 // Approximate size for these types
		}
	}

	if totalSize > maxSize {
		return errors.New("0084") // Total metadata size exceeds maximum allowed size (updated from 0077)
	}

	return nil
}

// isValidMetadataValueType checks if a value is of a type supported in metadata
func isValidMetadataValueType(value any) bool {
	switch value.(type) {
	case string, bool, int, float64, nil:
		return true
	default:
		return false
	}
}