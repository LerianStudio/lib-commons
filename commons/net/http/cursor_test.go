//go:build unit

package http

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// EncodeCursor
// ---------------------------------------------------------------------------

func TestEncodeCursor_HappyPath_Next(t *testing.T) {
	t.Parallel()

	id := uuid.NewString()
	encoded, err := EncodeCursor(Cursor{ID: id, Direction: CursorDirectionNext})
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)

	// Verify it is valid base64.
	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)

	var cur Cursor
	require.NoError(t, json.Unmarshal(raw, &cur))
	assert.Equal(t, id, cur.ID)
	assert.Equal(t, CursorDirectionNext, cur.Direction)
}

func TestEncodeCursor_HappyPath_Prev(t *testing.T) {
	t.Parallel()

	id := uuid.NewString()
	encoded, err := EncodeCursor(Cursor{ID: id, Direction: CursorDirectionPrev})
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)

	decoded, err := DecodeCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, id, decoded.ID)
	assert.Equal(t, CursorDirectionPrev, decoded.Direction)
}

func TestEncodeCursor_EmptyID(t *testing.T) {
	t.Parallel()

	_, err := EncodeCursor(Cursor{ID: "", Direction: CursorDirectionNext})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
}

func TestEncodeCursor_InvalidDirection(t *testing.T) {
	t.Parallel()

	_, err := EncodeCursor(Cursor{ID: "some-id", Direction: "sideways"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursorDirection)
}

func TestEncodeCursor_EmptyDirection(t *testing.T) {
	t.Parallel()

	_, err := EncodeCursor(Cursor{ID: "some-id", Direction: ""})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursorDirection)
}

// ---------------------------------------------------------------------------
// DecodeCursor
// ---------------------------------------------------------------------------

func TestDecodeCursor_HappyPath_RoundTrip(t *testing.T) {
	t.Parallel()

	id := uuid.NewString()
	encoded, err := EncodeCursor(Cursor{ID: id, Direction: CursorDirectionNext})
	require.NoError(t, err)

	decoded, err := DecodeCursor(encoded)
	require.NoError(t, err)

	assert.Equal(t, id, decoded.ID)
	assert.Equal(t, CursorDirectionNext, decoded.Direction)
}

func TestDecodeCursor_InvalidBase64(t *testing.T) {
	t.Parallel()

	_, err := DecodeCursor("not-valid-base64!!!")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	assert.Contains(t, err.Error(), "decode failed")
}

func TestDecodeCursor_ValidBase64InvalidJSON(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte("not json at all"))
	_, err := DecodeCursor(encoded)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	assert.Contains(t, err.Error(), "unmarshal failed")
}

func TestDecodeCursor_MissingID(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte(`{"direction":"next"}`))
	_, err := DecodeCursor(encoded)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	assert.Contains(t, err.Error(), "missing id")
}

func TestDecodeCursor_EmptyID(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte(`{"id":"","direction":"next"}`))
	_, err := DecodeCursor(encoded)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
	assert.Contains(t, err.Error(), "missing id")
}

func TestDecodeCursor_InvalidDirection(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte(`{"id":"test-id","direction":"weird"}`))
	_, err := DecodeCursor(encoded)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursorDirection)
}

func TestDecodeCursor_EmptyDirection(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte(`{"id":"test-id","direction":""}`))
	_, err := DecodeCursor(encoded)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursorDirection)
}

func TestDecodeCursor_MissingDirection(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte(`{"id":"test-id"}`))
	_, err := DecodeCursor(encoded)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursorDirection)
}

func TestDecodeCursor_EmptyString(t *testing.T) {
	t.Parallel()

	_, err := DecodeCursor("")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
}

func TestDecodeCursor_ExtraFields(t *testing.T) {
	t.Parallel()

	// Extra JSON fields should be ignored.
	encoded := base64.StdEncoding.EncodeToString([]byte(`{"id":"test-id","direction":"next","extra":"ignored"}`))
	decoded, err := DecodeCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, "test-id", decoded.ID)
	assert.Equal(t, CursorDirectionNext, decoded.Direction)
}

// ---------------------------------------------------------------------------
// CursorDirectionRules (4 combos + invalid)
// ---------------------------------------------------------------------------

func TestCursorDirectionRules_AllCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		requestedSort    string
		cursorDir        string
		expectedOperator string
		expectedOrder    string
		expectErr        bool
	}{
		{
			name:             "ASC + next",
			requestedSort:    cn.SortDirASC,
			cursorDir:        CursorDirectionNext,
			expectedOperator: ">",
			expectedOrder:    cn.SortDirASC,
		},
		{
			name:             "ASC + prev",
			requestedSort:    cn.SortDirASC,
			cursorDir:        CursorDirectionPrev,
			expectedOperator: "<",
			expectedOrder:    cn.SortDirDESC,
		},
		{
			name:             "DESC + next",
			requestedSort:    cn.SortDirDESC,
			cursorDir:        CursorDirectionNext,
			expectedOperator: "<",
			expectedOrder:    cn.SortDirDESC,
		},
		{
			name:             "DESC + prev",
			requestedSort:    cn.SortDirDESC,
			cursorDir:        CursorDirectionPrev,
			expectedOperator: ">",
			expectedOrder:    cn.SortDirASC,
		},
		{
			name:          "invalid cursor direction",
			requestedSort: cn.SortDirASC,
			cursorDir:     "invalid",
			expectErr:     true,
		},
		{
			name:          "empty cursor direction",
			requestedSort: cn.SortDirASC,
			cursorDir:     "",
			expectErr:     true,
		},
		{
			name:             "lowercase sort direction defaults to ASC + next",
			requestedSort:    "asc",
			cursorDir:        CursorDirectionNext,
			expectedOperator: ">",
			expectedOrder:    cn.SortDirASC,
		},
		{
			name:             "lowercase desc + next",
			requestedSort:    "desc",
			cursorDir:        CursorDirectionNext,
			expectedOperator: "<",
			expectedOrder:    cn.SortDirDESC,
		},
		{
			name:             "garbage sort direction defaults to ASC + next",
			requestedSort:    "GARBAGE",
			cursorDir:        CursorDirectionNext,
			expectedOperator: ">",
			expectedOrder:    cn.SortDirASC,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			operator, order, err := CursorDirectionRules(tc.requestedSort, tc.cursorDir)

			if tc.expectErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidCursorDirection)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.expectedOperator, operator)
			assert.Equal(t, tc.expectedOrder, order)
		})
	}
}

// ---------------------------------------------------------------------------
// PaginateRecords
// ---------------------------------------------------------------------------

func TestPaginateRecords_NoPagination(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3, 4, 5}
	result := PaginateRecords(true, false, CursorDirectionNext, items, 3)
	assert.Equal(t, []int{1, 2, 3, 4, 5}, result)
}

func TestPaginateRecords_NextDirection(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3, 4, 5}
	result := PaginateRecords(false, true, CursorDirectionNext, items, 3)
	assert.Equal(t, []int{1, 2, 3}, result)
}

func TestPaginateRecords_PrevDirection_NotFirstPage(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3, 4, 5}
	result := PaginateRecords(false, true, CursorDirectionPrev, items, 3)
	assert.Equal(t, []int{3, 2, 1}, result)

	// Original slice should not be mutated.
	assert.Equal(t, []int{1, 2, 3, 4, 5}, items)
}

func TestPaginateRecords_PrevDirection_FirstPage(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3, 4, 5}
	// When isFirstPage=true, prev direction should NOT reverse.
	result := PaginateRecords(true, true, CursorDirectionPrev, items, 3)
	assert.Equal(t, []int{1, 2, 3}, result)
}

func TestPaginateRecords_EmptySlice(t *testing.T) {
	t.Parallel()

	result := PaginateRecords(true, true, CursorDirectionNext, []int{}, 10)
	assert.Empty(t, result)
}

func TestPaginateRecords_SingleItem(t *testing.T) {
	t.Parallel()

	result := PaginateRecords(false, true, CursorDirectionNext, []int{42}, 10)
	assert.Equal(t, []int{42}, result)
}

func TestPaginateRecords_ExactlyLimit(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3}
	result := PaginateRecords(false, true, CursorDirectionNext, items, 3)
	assert.Equal(t, []int{1, 2, 3}, result)
}

func TestPaginateRecords_MoreThanLimit(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3, 4, 5}
	result := PaginateRecords(false, true, CursorDirectionNext, items, 2)
	assert.Equal(t, []int{1, 2}, result)
}

func TestPaginateRecords_LimitZero(t *testing.T) {
	t.Parallel()

	// Limit 0 with hasPagination=true should return empty.
	items := []int{1, 2, 3}
	result := PaginateRecords(false, true, CursorDirectionNext, items, 0)
	assert.Empty(t, result)
}

func TestPaginateRecords_NegativeLimit(t *testing.T) {
	t.Parallel()

	// Negative limit is clamped to 0.
	items := []int{1, 2, 3}
	result := PaginateRecords(false, true, CursorDirectionNext, items, -5)
	assert.Empty(t, result)
}

func TestPaginateRecords_LimitOne(t *testing.T) {
	t.Parallel()

	items := []int{10, 20, 30}
	result := PaginateRecords(false, true, CursorDirectionNext, items, 1)
	assert.Equal(t, []int{10}, result)
}

func TestPaginateRecords_LimitLargerThanSlice(t *testing.T) {
	t.Parallel()

	items := []int{1, 2}
	result := PaginateRecords(false, true, CursorDirectionNext, items, 100)
	assert.Equal(t, []int{1, 2}, result)
}

func TestPaginateRecords_PrevSingleItemNotFirstPage(t *testing.T) {
	t.Parallel()

	items := []int{42}
	result := PaginateRecords(false, true, CursorDirectionPrev, items, 5)
	assert.Equal(t, []int{42}, result, "single item reversed is still that item")
}

func TestPaginateRecords_StringType(t *testing.T) {
	t.Parallel()

	items := []string{"a", "b", "c", "d"}
	result := PaginateRecords(false, true, CursorDirectionPrev, items, 3)
	assert.Equal(t, []string{"c", "b", "a"}, result)
}

func TestPaginateRecords_ConcurrentUsage(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)

		go func(limit int) {
			defer wg.Done()

			// Each goroutine gets its own copy.
			localItems := make([]int, len(items))
			copy(localItems, items)

			result := PaginateRecords(false, true, CursorDirectionPrev, localItems, limit)
			assert.LessOrEqual(t, len(result), limit)
		}(i%5 + 1)
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// CalculateCursor
// ---------------------------------------------------------------------------

func TestCalculateCursor_FirstPageWithMore(t *testing.T) {
	t.Parallel()

	firstID := uuid.NewString()
	lastID := uuid.NewString()

	pagination, err := CalculateCursor(true, true, CursorDirectionNext, firstID, lastID)
	require.NoError(t, err)
	assert.NotEmpty(t, pagination.Next)
	assert.Empty(t, pagination.Prev, "first page should not have prev cursor")

	next, err := DecodeCursor(pagination.Next)
	require.NoError(t, err)
	assert.Equal(t, lastID, next.ID)
	assert.Equal(t, CursorDirectionNext, next.Direction)
}

func TestCalculateCursor_MiddlePage(t *testing.T) {
	t.Parallel()

	firstID := uuid.NewString()
	lastID := uuid.NewString()

	pagination, err := CalculateCursor(false, true, CursorDirectionNext, firstID, lastID)
	require.NoError(t, err)
	assert.NotEmpty(t, pagination.Next)
	assert.NotEmpty(t, pagination.Prev)

	next, err := DecodeCursor(pagination.Next)
	require.NoError(t, err)
	assert.Equal(t, lastID, next.ID)
	assert.Equal(t, CursorDirectionNext, next.Direction)

	prev, err := DecodeCursor(pagination.Prev)
	require.NoError(t, err)
	assert.Equal(t, firstID, prev.ID)
	assert.Equal(t, CursorDirectionPrev, prev.Direction)
}

func TestCalculateCursor_LastPage(t *testing.T) {
	t.Parallel()

	firstID := uuid.NewString()
	lastID := uuid.NewString()

	pagination, err := CalculateCursor(false, false, CursorDirectionNext, firstID, lastID)
	require.NoError(t, err)
	assert.Empty(t, pagination.Next, "last page should not have next cursor")
	assert.NotEmpty(t, pagination.Prev)
}

func TestCalculateCursor_SinglePage(t *testing.T) {
	t.Parallel()

	firstID := uuid.NewString()
	lastID := uuid.NewString()

	pagination, err := CalculateCursor(true, false, CursorDirectionNext, firstID, lastID)
	require.NoError(t, err)
	assert.Empty(t, pagination.Next)
	assert.Empty(t, pagination.Prev)
}

func TestCalculateCursor_PrevDirection_NotFirstPage_WithPagination(t *testing.T) {
	t.Parallel()

	firstID := uuid.NewString()
	lastID := uuid.NewString()

	pagination, err := CalculateCursor(false, true, CursorDirectionPrev, firstID, lastID)
	require.NoError(t, err)
	// For prev direction: (cursorDirection == CursorDirectionPrev && (hasPagination || isFirstPage))
	assert.NotEmpty(t, pagination.Next)
	assert.NotEmpty(t, pagination.Prev)
}

func TestCalculateCursor_PrevDirection_FirstPage_NoPagination(t *testing.T) {
	t.Parallel()

	firstID := uuid.NewString()
	lastID := uuid.NewString()

	// isFirstPage=true, hasPagination=false, direction=prev
	// hasNext = (prev && (false || true)) = true
	pagination, err := CalculateCursor(true, false, CursorDirectionPrev, firstID, lastID)
	require.NoError(t, err)
	assert.NotEmpty(t, pagination.Next)
	assert.Empty(t, pagination.Prev, "first page should not have prev")
}

func TestCalculateCursor_InvalidDirection(t *testing.T) {
	t.Parallel()

	_, err := CalculateCursor(true, true, "invalid", "id1", "id2")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursorDirection)
}

func TestCalculateCursor_EmptyDirection(t *testing.T) {
	t.Parallel()

	_, err := CalculateCursor(true, true, "", "id1", "id2")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursorDirection)
}

func TestCalculateCursor_EmptyLastItemID(t *testing.T) {
	t.Parallel()

	// EncodeCursor will fail because ID is empty.
	_, err := CalculateCursor(true, true, CursorDirectionNext, "first", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
}

func TestCalculateCursor_EmptyFirstItemID_NotFirstPage(t *testing.T) {
	t.Parallel()

	// When not first page, prev cursor is built with firstItemID; empty will fail.
	_, err := CalculateCursor(false, false, CursorDirectionNext, "", "last")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidCursor)
}

// ---------------------------------------------------------------------------
// Cursor encode/decode round-trip with various ID formats
// ---------------------------------------------------------------------------

func TestCursor_RoundTrip_UUIDId(t *testing.T) {
	t.Parallel()

	id := uuid.NewString()
	encoded, err := EncodeCursor(Cursor{ID: id, Direction: CursorDirectionNext})
	require.NoError(t, err)

	decoded, err := DecodeCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, id, decoded.ID)
	assert.Equal(t, CursorDirectionNext, decoded.Direction)
}

func TestCursor_RoundTrip_ArbitraryStringID(t *testing.T) {
	t.Parallel()

	id := "custom-resource-id-12345"
	encoded, err := EncodeCursor(Cursor{ID: id, Direction: CursorDirectionPrev})
	require.NoError(t, err)

	decoded, err := DecodeCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, id, decoded.ID)
	assert.Equal(t, CursorDirectionPrev, decoded.Direction)
}

func TestCursor_RoundTrip_SpecialCharacters(t *testing.T) {
	t.Parallel()

	id := "id/with+special=characters&more"
	encoded, err := EncodeCursor(Cursor{ID: id, Direction: CursorDirectionNext})
	require.NoError(t, err)

	decoded, err := DecodeCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, id, decoded.ID)
}

func TestCursor_RoundTrip_VeryLongID(t *testing.T) {
	t.Parallel()

	id := strings.Repeat("x", 1000)
	encoded, err := EncodeCursor(Cursor{ID: id, Direction: CursorDirectionNext})
	require.NoError(t, err)

	decoded, err := DecodeCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, id, decoded.ID)
}

func TestCursor_RoundTrip_UnicodeID(t *testing.T) {
	t.Parallel()

	id := "id-with-unicode-\u00e9\u00e8\u00ea"
	encoded, err := EncodeCursor(Cursor{ID: id, Direction: CursorDirectionNext})
	require.NoError(t, err)

	decoded, err := DecodeCursor(encoded)
	require.NoError(t, err)
	assert.Equal(t, id, decoded.ID)
}

// ---------------------------------------------------------------------------
// Cursor constants
// ---------------------------------------------------------------------------

func TestCursorDirectionConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "next", CursorDirectionNext)
	assert.Equal(t, "prev", CursorDirectionPrev)
}

// ---------------------------------------------------------------------------
// CursorPagination struct
// ---------------------------------------------------------------------------

func TestCursorPagination_JSON(t *testing.T) {
	t.Parallel()

	cp := CursorPagination{Next: "abc", Prev: "def"}
	data, err := json.Marshal(cp)
	require.NoError(t, err)

	var decoded CursorPagination
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "abc", decoded.Next)
	assert.Equal(t, "def", decoded.Prev)
}

func TestCursorPagination_EmptyJSON(t *testing.T) {
	t.Parallel()

	cp := CursorPagination{}
	data, err := json.Marshal(cp)
	require.NoError(t, err)

	var decoded CursorPagination
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Empty(t, decoded.Next)
	assert.Empty(t, decoded.Prev)
}
