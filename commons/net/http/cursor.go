package http

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LerianStudio/lib-commons/v4/commons"
	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
)

const (
	// CursorDirectionNext is the cursor direction for forward navigation.
	CursorDirectionNext = "next"
	// CursorDirectionPrev is the cursor direction for backward navigation.
	CursorDirectionPrev = "prev"
)

// ErrInvalidCursorDirection indicates an invalid next/prev cursor direction.
var ErrInvalidCursorDirection = errors.New("invalid cursor direction")

// Cursor is the cursor contract for keyset navigation.
type Cursor struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
}

// CursorPagination carries encoded next and previous cursors.
type CursorPagination struct {
	Next string `json:"next"`
	Prev string `json:"prev"`
}

// EncodeCursor encodes a Cursor as a base64 JSON token.
func EncodeCursor(cursor Cursor) (string, error) {
	if cursor.ID == "" {
		return "", ErrInvalidCursor
	}

	if cursor.Direction != CursorDirectionNext && cursor.Direction != CursorDirectionPrev {
		return "", ErrInvalidCursorDirection
	}

	cursorBytes, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}

	return base64.StdEncoding.EncodeToString(cursorBytes), nil
}

// DecodeCursor decodes a base64 JSON cursor token and validates it.
func DecodeCursor(cursor string) (Cursor, error) {
	decodedCursor, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: decode failed: %w", ErrInvalidCursor, err)
	}

	var cur Cursor
	if err := json.Unmarshal(decodedCursor, &cur); err != nil {
		return Cursor{}, fmt.Errorf("%w: unmarshal failed: %w", ErrInvalidCursor, err)
	}

	if cur.ID == "" {
		return Cursor{}, fmt.Errorf("%w: missing id", ErrInvalidCursor)
	}

	if cur.Direction != CursorDirectionNext && cur.Direction != CursorDirectionPrev {
		return Cursor{}, ErrInvalidCursorDirection
	}

	return cur, nil
}

// CursorDirectionRules returns the comparison operator and effective order.
func CursorDirectionRules(requestedSortDirection, cursorDirection string) (operator, effectiveOrder string, err error) {
	order := ValidateSortDirection(requestedSortDirection)

	switch cursorDirection {
	case CursorDirectionNext:
		if order == cn.SortDirASC {
			return ">", cn.SortDirASC, nil
		}

		return "<", cn.SortDirDESC, nil
	case CursorDirectionPrev:
		if order == cn.SortDirASC {
			return "<", cn.SortDirDESC, nil
		}

		return ">", cn.SortDirASC, nil
	default:
		return "", "", ErrInvalidCursorDirection
	}
}

// PaginateRecords slices records to the requested page and normalizes prev direction order.
func PaginateRecords[T any](
	isFirstPage bool,
	hasPagination bool,
	cursorDirection string,
	items []T,
	limit int,
) []T {
	if !hasPagination {
		return items
	}

	if limit < 0 {
		limit = 0
	}

	if limit > len(items) {
		limit = len(items)
	}

	paginated := make([]T, limit)
	copy(paginated, items[:limit])

	if !isFirstPage && cursorDirection == CursorDirectionPrev {
		return commons.Reverse(paginated)
	}

	return paginated
}

// CalculateCursor builds next/prev cursor tokens for a paged record set.
func CalculateCursor(
	isFirstPage, hasPagination bool,
	cursorDirection string,
	firstItemID, lastItemID string,
) (CursorPagination, error) {
	var pagination CursorPagination

	if cursorDirection != CursorDirectionNext && cursorDirection != CursorDirectionPrev {
		return CursorPagination{}, ErrInvalidCursorDirection
	}

	hasNext := (cursorDirection == CursorDirectionNext && hasPagination) ||
		(cursorDirection == CursorDirectionPrev && (hasPagination || isFirstPage))

	if hasNext {
		next, err := EncodeCursor(Cursor{ID: lastItemID, Direction: CursorDirectionNext})
		if err != nil {
			return CursorPagination{}, err
		}

		pagination.Next = next
	}

	if !isFirstPage {
		prev, err := EncodeCursor(Cursor{ID: firstItemID, Direction: CursorDirectionPrev})
		if err != nil {
			return CursorPagination{}, err
		}

		pagination.Prev = prev
	}

	return pagination, nil
}
