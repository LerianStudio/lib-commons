package constant

// Pagination defaults.
const (
	// DefaultLimit is the default number of items per page.
	DefaultLimit = 20
	// DefaultOffset is the default pagination offset.
	DefaultOffset = 0
	// MaxLimit is the maximum allowed items per page.
	MaxLimit = 200
)

// Sort direction constants (uppercase, used by HTTP APIs).
const (
	// SortDirASC is the ascending sort direction for API responses.
	SortDirASC = "ASC"
	// SortDirDESC is the descending sort direction for API responses.
	SortDirDESC = "DESC"
)
