package http

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ErrLimitMustBePositive is returned when limit is below 1.
var ErrLimitMustBePositive = errors.New("limit must be greater than zero")

// ErrInvalidCursor is returned when the cursor cannot be decoded.
var ErrInvalidCursor = errors.New("invalid cursor format")

// sortColumnPattern validates sort column names to prevent SQL injection.
var sortColumnPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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

		limit = parsed
	}

	if limit <= 0 {
		limit = cn.DefaultLimit
	}

	if limit > cn.MaxLimit {
		limit = cn.MaxLimit
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

		limit = parsed
	}

	if limit <= 0 {
		limit = cn.DefaultLimit
	}

	if limit > cn.MaxLimit {
		limit = cn.MaxLimit
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

// SortCursor encodes a position in a sorted result set for composite keyset pagination.
// It stores the sort column name, sort value, and record ID, enabling stable cursor
// pagination when ordering by columns other than id.
type SortCursor struct {
	SortColumn string `json:"sc"`
	SortValue  string `json:"sv"`
	ID         string `json:"i"`
	PointsNext bool   `json:"pn"`
}

// EncodeSortCursor encodes sort cursor data into a base64 string.
// Returns an error if id is empty or sortColumn is empty, matching the
// decoder's validation contract.
func EncodeSortCursor(sortColumn, sortValue, id string, pointsNext bool) (string, error) {
	if id == "" {
		return "", fmt.Errorf("%w: id must not be empty", ErrInvalidCursor)
	}

	if sortColumn == "" {
		return "", fmt.Errorf("%w: sort column must not be empty", ErrInvalidCursor)
	}

	cursor := SortCursor{
		SortColumn: sortColumn,
		SortValue:  sortValue,
		ID:         id,
		PointsNext: pointsNext,
	}

	data, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode sort cursor: %w", err)
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

// DecodeSortCursor decodes a base64 cursor string into a SortCursor.
func DecodeSortCursor(cursor string) (*SortCursor, error) {
	decoded, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fmt.Errorf("%w: decode failed: %w", ErrInvalidCursor, err)
	}

	var sc SortCursor
	if err := json.Unmarshal(decoded, &sc); err != nil {
		return nil, fmt.Errorf("%w: unmarshal failed: %w", ErrInvalidCursor, err)
	}

	if sc.ID == "" {
		return nil, fmt.Errorf("%w: missing id", ErrInvalidCursor)
	}

	if sc.SortColumn == "" || !sortColumnPattern.MatchString(sc.SortColumn) {
		return nil, fmt.Errorf("%w: invalid sort column", ErrInvalidCursor)
	}

	return &sc, nil
}

// SortCursorDirection computes the actual SQL ORDER BY direction and comparison
// operator for composite keyset pagination based on the requested direction and
// whether the cursor points forward or backward.
func SortCursorDirection(requestedDir string, pointsNext bool) (actualDir, operator string) {
	isAsc := strings.EqualFold(requestedDir, cn.SortDirASC)

	if pointsNext {
		if isAsc {
			return cn.SortDirASC, ">"
		}

		return cn.SortDirDESC, "<"
	}

	// Backward navigation: flip the direction
	if isAsc {
		return cn.SortDirDESC, "<"
	}

	return cn.SortDirASC, ">"
}

// CalculateSortCursorPagination computes Next/Prev cursor strings for composite keyset pagination.
func CalculateSortCursorPagination(
	isFirstPage, hasPagination, pointsNext bool,
	sortColumn string,
	firstSortValue, firstID string,
	lastSortValue, lastID string,
) (next, prev string, err error) {
	hasNext := (pointsNext && hasPagination) || (!pointsNext && (hasPagination || isFirstPage))

	if hasNext {
		next, err = EncodeSortCursor(sortColumn, lastSortValue, lastID, true)
		if err != nil {
			return "", "", err
		}
	}

	if !isFirstPage {
		prev, err = EncodeSortCursor(sortColumn, firstSortValue, firstID, false)
		if err != nil {
			return "", "", err
		}
	}

	return next, prev, nil
}

// ValidateSortColumn checks whether column is in the allowed list (case-insensitive)
// and returns the matched allowed value. If no match is found, it returns defaultColumn.
func ValidateSortColumn(column string, allowed []string, defaultColumn string) string {
	for _, a := range allowed {
		if strings.EqualFold(column, a) {
			return a
		}
	}

	return defaultColumn
}
