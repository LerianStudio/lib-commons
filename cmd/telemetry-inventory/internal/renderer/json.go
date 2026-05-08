// Package renderer turns schema dictionaries into deterministic output formats.
package renderer

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

// JSON renders a dictionary as indented JSON with a trailing newline.
func JSON(d *schema.Dictionary) ([]byte, error) {
	if d == nil {
		return nil, errors.New("dictionary is nil")
	}

	out, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal dictionary json: %w", err)
	}

	out = append(out, '\n')

	return out, nil
}
