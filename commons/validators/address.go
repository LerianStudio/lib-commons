package validators

import (
	"errors"
)

// Address is a simplified address structure for validation purposes.
type Address struct {
	Line1   string
	Line2   *string
	ZipCode string
	City    string
	State   string
	Country string
}

// Config defines validation configuration parameters
type Config struct {
	MaxAddressLineLength int
	MaxZipCodeLength     int
	MaxCityLength        int
	MaxStateLength       int
}

// DefaultConfig returns default validation configuration
func DefaultConfig() Config {
	return Config{
		MaxAddressLineLength: 100,
		MaxZipCodeLength:     20,
		MaxCityLength:        100,
		MaxStateLength:       100,
	}
}

// ValidateAddress validates an address structure for completeness and correctness.
func ValidateAddress(address *Address) error {
	return ValidateAddressWithConfig(address, DefaultConfig())
}

// ValidateAddressWithConfig validates an address structure using custom validation configuration.
func ValidateAddressWithConfig(address *Address, config Config) error {
	if address == nil {
		return errors.New(ErrAddressNil) // Address cannot be nil
	}

	// Validate required fields
	if address.Line1 == "" {
		return errors.New(ErrLine1Required) // Address line 1 is required
	}

	if len(address.Line1) > config.MaxAddressLineLength {
		return errors.New(ErrLine1TooLong) // Address line 1 exceeds maximum length
	}

	// Validate optional line 2
	if address.Line2 != nil && len(*address.Line2) > config.MaxAddressLineLength {
		return errors.New(ErrLine2TooLong) // Address line 2 exceeds maximum length
	}

	// Validate zip code
	if address.ZipCode == "" {
		return errors.New(ErrZipRequired) // Zip code is required
	}

	if len(address.ZipCode) > config.MaxZipCodeLength {
		return errors.New(ErrZipTooLong) // Zip code exceeds maximum length
	}

	// Validate city
	if address.City == "" {
		return errors.New(ErrCityRequired) // City is required
	}

	if len(address.City) > config.MaxCityLength {
		return errors.New(ErrCityTooLong) // City exceeds maximum length
	}

	// Validate state
	if address.State == "" {
		return errors.New(ErrStateRequired) // State is required
	}

	if len(address.State) > config.MaxStateLength {
		return errors.New(ErrStateTooLong) // State exceeds maximum length
	}

	// Validate country
	if address.Country == "" {
		return errors.New(ErrAddressCountryRequired) // Country is required
	}

	return Default.ValidateCountryAddress(address.Country)
}