package commons

import (
	"github.com/LerianStudio/lib-commons/commons/validators"
)

// IsErrorCode checks if an error matches a specific error code
func IsErrorCode(err error, code string) bool {
	if err == nil {
		return false
	}

	return err.Error() == code
}

// GetErrorMessage returns a human-readable message for an error
func GetErrorMessage(err error) string {
	if err == nil {
		return ""
	}

	return validators.GetErrorMessage(err.Error())
}

// FormatError takes an error (which may be a numeric code) and returns a structured error
func FormatError(err error) validators.ErrorWithCode {
	if err == nil {
		return validators.ErrorWithCode{}
	}

	return validators.ParseError(err)
}
