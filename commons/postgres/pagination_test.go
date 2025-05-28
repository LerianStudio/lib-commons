package postgres

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPagination_SetItems(t *testing.T) {
	tests := []struct {
		name     string
		items    any
		expected any
	}{
		{
			name:     "should set string slice items",
			items:    []string{"item1", "item2", "item3"},
			expected: []string{"item1", "item2", "item3"},
		},
		{
			name:     "should set int slice items",
			items:    []int{1, 2, 3, 4, 5},
			expected: []int{1, 2, 3, 4, 5},
		},
		{
			name: "should set struct slice items",
			items: []struct {
				ID   int
				Name string
			}{
				{ID: 1, Name: "John"},
				{ID: 2, Name: "Jane"},
			},
			expected: []struct {
				ID   int
				Name string
			}{
				{ID: 1, Name: "John"},
				{ID: 2, Name: "Jane"},
			},
		},
		{
			name:     "should set nil items",
			items:    nil,
			expected: nil,
		},
		{
			name:     "should set empty slice",
			items:    []string{},
			expected: []string{},
		},
		{
			name:     "should set single item",
			items:    "single item",
			expected: "single item",
		},
		{
			name: "should set map items",
			items: map[string]int{
				"one":   1,
				"two":   2,
				"three": 3,
			},
			expected: map[string]int{
				"one":   1,
				"two":   2,
				"three": 3,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pagination{}
			p.SetItems(tt.items)
			assert.Equal(t, tt.expected, p.Items)
		})
	}
}

func TestPagination_SetCursor(t *testing.T) {
	tests := []struct {
		name       string
		nextCursor string
		prevCursor string
	}{
		{
			name:       "should set both cursors",
			nextCursor: "MDAwMDAwMDAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAwMA==",
			prevCursor: "MDAwMDAwMDAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAwMQ==",
		},
		{
			name:       "should set empty cursors",
			nextCursor: "",
			prevCursor: "",
		},
		{
			name:       "should set only next cursor",
			nextCursor: "next-cursor-value",
			prevCursor: "",
		},
		{
			name:       "should set only prev cursor",
			nextCursor: "",
			prevCursor: "prev-cursor-value",
		},
		{
			name:       "should set long cursor values",
			nextCursor: "very-long-cursor-value-that-might-be-used-in-real-scenarios-with-base64-encoding",
			prevCursor: "another-very-long-cursor-value-that-might-be-used-in-real-scenarios",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pagination{}
			p.SetCursor(tt.nextCursor, tt.prevCursor)
			assert.Equal(t, tt.nextCursor, p.NextCursor)
			assert.Equal(t, tt.prevCursor, p.PrevCursor)
		})
	}
}

func TestPagination_StructInitialization(t *testing.T) {
	// Test struct initialization with various field values
	now := time.Now()
	items := []string{"item1", "item2"}

	p := &Pagination{
		Items:      items,
		Page:       2,
		PrevCursor: "prev123",
		NextCursor: "next456",
		Limit:      25,
		SortOrder:  "desc",
		StartDate:  now.Add(-24 * time.Hour),
		EndDate:    now,
	}

	assert.Equal(t, items, p.Items)
	assert.Equal(t, 2, p.Page)
	assert.Equal(t, "prev123", p.PrevCursor)
	assert.Equal(t, "next456", p.NextCursor)
	assert.Equal(t, 25, p.Limit)
	assert.Equal(t, "desc", p.SortOrder)
	assert.Equal(t, now.Add(-24*time.Hour), p.StartDate)
	assert.Equal(t, now, p.EndDate)
}

func TestPagination_JSONSerialization(t *testing.T) {
	// Test JSON serialization and deserialization
	items := []map[string]string{
		{"id": "1", "name": "Item 1"},
		{"id": "2", "name": "Item 2"},
	}

	p := &Pagination{
		Items:      items,
		Page:       1,
		PrevCursor: "",
		NextCursor: "next-page-cursor",
		Limit:      10,
		SortOrder:  "asc",
		StartDate:  time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC),
		EndDate:    time.Date(2021, 12, 31, 23, 59, 59, 0, time.UTC),
	}

	// Serialize to JSON
	jsonData, err := json.Marshal(p)
	assert.NoError(t, err)

	// Verify JSON structure
	var jsonMap map[string]any
	err = json.Unmarshal(jsonData, &jsonMap)
	assert.NoError(t, err)

	// Check that certain fields are included/excluded as per tags
	assert.NotNil(t, jsonMap["items"])
	assert.NotNil(t, jsonMap["limit"])
	assert.NotNil(t, jsonMap["page"])
	assert.NotNil(t, jsonMap["next_cursor"])
	
	// SortOrder, StartDate, and EndDate should not be in JSON (json:"-")
	assert.Nil(t, jsonMap["sort_order"])
	assert.Nil(t, jsonMap["start_date"])
	assert.Nil(t, jsonMap["end_date"])

	// Deserialize back
	var p2 Pagination
	err = json.Unmarshal(jsonData, &p2)
	assert.NoError(t, err)
	assert.Equal(t, p.Page, p2.Page)
	assert.Equal(t, p.NextCursor, p2.NextCursor)
	assert.Equal(t, p.Limit, p2.Limit)
}

func TestPagination_EdgeCases(t *testing.T) {
	t.Run("should handle zero values", func(t *testing.T) {
		p := &Pagination{}
		assert.Nil(t, p.Items)
		assert.Equal(t, 0, p.Page)
		assert.Equal(t, "", p.PrevCursor)
		assert.Equal(t, "", p.NextCursor)
		assert.Equal(t, 0, p.Limit)
		assert.Equal(t, "", p.SortOrder)
		assert.True(t, p.StartDate.IsZero())
		assert.True(t, p.EndDate.IsZero())
	})

	t.Run("should handle negative page", func(t *testing.T) {
		p := &Pagination{Page: -1}
		assert.Equal(t, -1, p.Page)
	})

	t.Run("should handle negative limit", func(t *testing.T) {
		p := &Pagination{Limit: -10}
		assert.Equal(t, -10, p.Limit)
	})

	t.Run("should handle multiple SetItems calls", func(t *testing.T) {
		p := &Pagination{}
		
		// First set
		p.SetItems([]int{1, 2, 3})
		assert.Equal(t, []int{1, 2, 3}, p.Items)
		
		// Second set (should override)
		p.SetItems([]string{"a", "b", "c"})
		assert.Equal(t, []string{"a", "b", "c"}, p.Items)
		
		// Third set with nil
		p.SetItems(nil)
		assert.Nil(t, p.Items)
	})

	t.Run("should handle multiple SetCursor calls", func(t *testing.T) {
		p := &Pagination{}
		
		// First set
		p.SetCursor("next1", "prev1")
		assert.Equal(t, "next1", p.NextCursor)
		assert.Equal(t, "prev1", p.PrevCursor)
		
		// Second set (should override)
		p.SetCursor("next2", "prev2")
		assert.Equal(t, "next2", p.NextCursor)
		assert.Equal(t, "prev2", p.PrevCursor)
		
		// Third set with empty strings
		p.SetCursor("", "")
		assert.Equal(t, "", p.NextCursor)
		assert.Equal(t, "", p.PrevCursor)
	})
}

func TestPagination_DateHandling(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	
	tests := []struct {
		name      string
		startDate time.Time
		endDate   time.Time
	}{
		{
			name:      "should handle UTC dates",
			startDate: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC),
			endDate:   time.Date(2023, 12, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			name:      "should handle dates in different timezone",
			startDate: time.Date(2023, 1, 1, 0, 0, 0, 0, loc),
			endDate:   time.Date(2023, 12, 31, 23, 59, 59, 0, loc),
		},
		{
			name:      "should handle zero dates",
			startDate: time.Time{},
			endDate:   time.Time{},
		},
		{
			name:      "should handle far future dates",
			startDate: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
			endDate:   time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC),
		},
		{
			name:      "should handle far past dates",
			startDate: time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC),
			endDate:   time.Date(1900, 12, 31, 23, 59, 59, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pagination{
				StartDate: tt.startDate,
				EndDate:   tt.endDate,
			}
			assert.Equal(t, tt.startDate, p.StartDate)
			assert.Equal(t, tt.endDate, p.EndDate)
		})
	}
}

func TestPagination_ComplexItems(t *testing.T) {
	// Test with complex nested structures
	type User struct {
		ID        int
		Name      string
		CreatedAt time.Time
		Metadata  map[string]any
	}

	users := []User{
		{
			ID:        1,
			Name:      "John Doe",
			CreatedAt: time.Now(),
			Metadata: map[string]any{
				"role":     "admin",
				"verified": true,
				"score":    95.5,
			},
		},
		{
			ID:        2,
			Name:      "Jane Smith",
			CreatedAt: time.Now().Add(-24 * time.Hour),
			Metadata: map[string]any{
				"role":     "user",
				"verified": false,
				"score":    87.3,
			},
		},
	}

	p := &Pagination{}
	p.SetItems(users)

	retrievedUsers, ok := p.Items.([]User)
	assert.True(t, ok)
	assert.Len(t, retrievedUsers, 2)
	assert.Equal(t, users[0].ID, retrievedUsers[0].ID)
	assert.Equal(t, users[1].Name, retrievedUsers[1].Name)
}

func TestPagination_ConcurrentAccess(t *testing.T) {
	// Test concurrent access to pagination struct
	// Note: The current implementation is not thread-safe, 
	// but this test documents the behavior
	
	p := &Pagination{
		Items:      []int{1, 2, 3},
		NextCursor: "initial",
		PrevCursor: "initial",
	}

	done := make(chan bool, 2)

	// Goroutine 1: Set items
	go func() {
		for i := 0; i < 100; i++ {
			p.SetItems([]int{i, i + 1, i + 2})
		}
		done <- true
	}()

	// Goroutine 2: Set cursors
	go func() {
		for i := 0; i < 100; i++ {
			p.SetCursor("next"+string(rune(i)), "prev"+string(rune(i)))
		}
		done <- true
	}()

	// Wait for both goroutines
	<-done
	<-done

	// Just verify the struct is still valid
	assert.NotNil(t, p.Items)
	assert.NotEmpty(t, p.NextCursor)
	assert.NotEmpty(t, p.PrevCursor)
}

// Benchmark tests
func BenchmarkPagination_SetItems(b *testing.B) {
	items := make([]int, 1000)
	for i := range items {
		items[i] = i
	}

	p := &Pagination{}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.SetItems(items)
	}
}

func BenchmarkPagination_SetCursor(b *testing.B) {
	p := &Pagination{}
	nextCursor := "MDAwMDAwMDAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAwMA=="
	prevCursor := "MDAwMDAwMDAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAwMQ=="
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.SetCursor(nextCursor, prevCursor)
	}
}

func BenchmarkPagination_JSONMarshal(b *testing.B) {
	items := make([]map[string]string, 100)
	for i := range items {
		items[i] = map[string]string{
			"id":   string(rune(i)),
			"name": "Item " + string(rune(i)),
		}
	}

	p := &Pagination{
		Items:      items,
		Page:       5,
		NextCursor: "next-cursor-value",
		PrevCursor: "prev-cursor-value",
		Limit:      100,
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(p)
	}
}