package http

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
)

// sortColumnPattern validates sort column names as simple SQL identifiers.
// Callers must still enforce endpoint-specific allowlists with ValidateSortColumn.
var sortColumnPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

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
// It validates identifier syntax only; callers must still validate SortColumn
// against their endpoint-specific allowlist before building queries.
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
