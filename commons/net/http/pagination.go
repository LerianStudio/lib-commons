package http

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ErrLimitMustBePositive is returned when limit is below 1.
var ErrLimitMustBePositive = errors.New("limit must be greater than zero")

// ErrInvalidCursor is returned when the cursor cannot be decoded.
var ErrInvalidCursor = errors.New("invalid cursor format")

// ValidateLimitStrict validates a pagination limit without silently coercing
// non-positive values. It returns ErrLimitMustBePositive when limit < 1 and
// caps values above maxLimit.
func ValidateLimitStrict(limit, maxLimit int) (int, error) {
	if limit <= 0 {
		return 0, ErrLimitMustBePositive
	}

	if limit > maxLimit {
		return maxLimit, nil
	}

	return limit, nil
}

// ParsePagination parses limit/offset query params with defaults.
// Non-numeric values return an error. Negative or zero limits are coerced to
// DefaultLimit; negative offsets are coerced to DefaultOffset; limits above
// MaxLimit are capped.
func ParsePagination(fiberCtx *fiber.Ctx) (int, int, error) {
	if fiberCtx == nil {
		return 0, 0, ErrContextNotFound
	}

	limit := cn.DefaultLimit
	offset := cn.DefaultOffset

	if limitValue := fiberCtx.Query("limit"); limitValue != "" {
		parsed, err := strconv.Atoi(limitValue)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid limit value: %w", err)
		}

		limit = parsed
	}

	if offsetValue := fiberCtx.Query("offset"); offsetValue != "" {
		parsed, err := strconv.Atoi(offsetValue)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid offset value: %w", err)
		}

		offset = parsed
	}

	if limit <= 0 {
		limit = cn.DefaultLimit
	}

	if limit > cn.MaxLimit {
		limit = cn.MaxLimit
	}

	if offset < 0 {
		offset = cn.DefaultOffset
	}

	return limit, offset, nil
}

// ParseOpaqueCursorPagination parses cursor/limit query params for opaque cursor pagination.
// It validates limit but does not attempt to decode the cursor string.
// Returns the raw cursor string (empty for first page), limit, and any error.
func ParseOpaqueCursorPagination(fiberCtx *fiber.Ctx) (string, int, error) {
	if fiberCtx == nil {
		return "", 0, ErrContextNotFound
	}

	limit := cn.DefaultLimit

	if limitValue := fiberCtx.Query("limit"); limitValue != "" {
		parsed, err := strconv.Atoi(limitValue)
		if err != nil {
			return "", 0, fmt.Errorf("invalid limit value: %w", err)
		}

		limit = ValidateLimit(parsed, cn.DefaultLimit, cn.MaxLimit)
	}

	cursorParam := fiberCtx.Query("cursor")
	if cursorParam == "" {
		return "", limit, nil
	}

	return cursorParam, limit, nil
}

// EncodeUUIDCursor encodes a UUID into a base64 cursor string.
func EncodeUUIDCursor(id uuid.UUID) string {
	return base64.StdEncoding.EncodeToString([]byte(id.String()))
}

// DecodeUUIDCursor decodes a base64 cursor string into a UUID.
func DecodeUUIDCursor(cursor string) (uuid.UUID, error) {
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: decode failed: %w", ErrInvalidCursor, err)
	}

	id, err := uuid.Parse(string(decoded))
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: parse failed: %w", ErrInvalidCursor, err)
	}

	return id, nil
}
