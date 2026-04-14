package http

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// TimestampCursor represents a cursor for keyset pagination with timestamp + ID ordering.
// This ensures correct pagination when records are ordered by (timestamp DESC, id DESC).
type TimestampCursor struct {
	Timestamp time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
}

// EncodeTimestampCursor encodes a timestamp and UUID into a base64 cursor string.
// Returns an error if id is uuid.Nil, matching the decoder's validation contract.
func EncodeTimestampCursor(timestamp time.Time, id uuid.UUID) (string, error) {
	if id == uuid.Nil {
		return "", fmt.Errorf("%w: id must not be nil UUID", ErrInvalidCursor)
	}

	cursor := TimestampCursor{
		Timestamp: timestamp.UTC(),
		ID:        id,
	}

	data, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode timestamp cursor: %w", err)
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

// DecodeTimestampCursor decodes a base64 cursor string into a TimestampCursor.
func DecodeTimestampCursor(cursor string) (*TimestampCursor, error) {
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fmt.Errorf("%w: decode failed: %w", ErrInvalidCursor, err)
	}

	var tc TimestampCursor
	if err := json.Unmarshal(decoded, &tc); err != nil {
		return nil, fmt.Errorf("%w: unmarshal failed: %w", ErrInvalidCursor, err)
	}

	if tc.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: missing id", ErrInvalidCursor)
	}

	return &tc, nil
}

// ParseTimestampCursorPagination parses cursor/limit query params for timestamp-based cursor pagination.
// Returns the decoded TimestampCursor (nil for first page), limit, and any error.
func ParseTimestampCursorPagination(fiberCtx *fiber.Ctx) (*TimestampCursor, int, error) {
	if fiberCtx == nil {
		return nil, 0, ErrContextNotFound
	}

	limit := cn.DefaultLimit

	if limitValue := fiberCtx.Query("limit"); limitValue != "" {
		parsed, err := strconv.Atoi(limitValue)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid limit value: %w", err)
		}

		limit = ValidateLimit(parsed, cn.DefaultLimit, cn.MaxLimit)
	}

	cursorParam := fiberCtx.Query("cursor")
	if cursorParam == "" {
		return nil, limit, nil
	}

	tc, err := DecodeTimestampCursor(cursorParam)
	if err != nil {
		return nil, 0, err
	}

	return tc, limit, nil
}
