package postgres

import (
	"regexp"
	"strings"
)

var identifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func validateIdentifier(identifier string) error {
	if len(identifier) > maxSQLIdentifierLength {
		return ErrInvalidIdentifier
	}

	if !identifierPattern.MatchString(identifier) {
		return ErrInvalidIdentifier
	}

	return nil
}

func validateIdentifierPath(path string) error {
	for part := range strings.SplitSeq(path, ".") {
		trimmed := strings.TrimSpace(part)
		if err := validateIdentifier(trimmed); err != nil {
			return err
		}
	}

	return nil
}

func quoteIdentifierPath(path string) string {
	parts := strings.Split(path, ".")
	quoted := make([]string, 0, len(parts))

	for _, part := range parts {
		quoted = append(quoted, quoteIdentifier(strings.TrimSpace(part)))
	}

	return strings.Join(quoted, ".")
}

func quoteIdentifier(identifier string) string {
	identifier = strings.ReplaceAll(identifier, "\x00", "")

	return "\"" + strings.ReplaceAll(identifier, "\"", "\"\"") + "\""
}
