package safe

import (
	"errors"
	"fmt"
)

// ErrEmptySlice is returned when attempting to access elements of an empty slice.
var ErrEmptySlice = errors.New("empty slice")

// ErrIndexOutOfBounds is returned when an index is outside the valid range.
var ErrIndexOutOfBounds = errors.New("index out of bounds")

// First returns the first element of a slice.
// Returns ErrEmptySlice if the slice is empty.
//
// Example:
//
//	first, err := safe.First(items)
//	if err != nil {
//	    return fmt.Errorf("get first item: %w", err)
//	}
func First[T any](slice []T) (T, error) {
	var zero T

	if len(slice) == 0 {
		return zero, ErrEmptySlice
	}

	return slice[0], nil
}

// Last returns the last element of a slice.
// Returns ErrEmptySlice if the slice is empty.
//
// Example:
//
//	last, err := safe.Last(items)
//	if err != nil {
//	    return fmt.Errorf("get last item: %w", err)
//	}
func Last[T any](slice []T) (T, error) {
	var zero T

	if len(slice) == 0 {
		return zero, ErrEmptySlice
	}

	return slice[len(slice)-1], nil
}

// At returns the element at the specified index.
// Returns ErrIndexOutOfBounds if the index is out of range.
//
// Example:
//
//	item, err := safe.At(items, 5)
//	if err != nil {
//	    return fmt.Errorf("get item at index 5: %w", err)
//	}
func At[T any](slice []T, index int) (T, error) {
	var zero T

	if index < 0 || index >= len(slice) {
		return zero, fmt.Errorf("%w: index %d, length %d", ErrIndexOutOfBounds, index, len(slice))
	}

	return slice[index], nil
}

// FirstOrDefault returns the first element of a slice, or defaultValue if empty.
//
// Example:
//
//	first := safe.FirstOrDefault(items, defaultItem)
func FirstOrDefault[T any](slice []T, defaultValue T) T {
	if len(slice) == 0 {
		return defaultValue
	}

	return slice[0]
}

// LastOrDefault returns the last element of a slice, or defaultValue if empty.
//
// Example:
//
//	last := safe.LastOrDefault(items, defaultItem)
func LastOrDefault[T any](slice []T, defaultValue T) T {
	if len(slice) == 0 {
		return defaultValue
	}

	return slice[len(slice)-1]
}

// AtOrDefault returns the element at index, or defaultValue if out of bounds.
//
// Example:
//
//	item := safe.AtOrDefault(items, 5, defaultItem)
func AtOrDefault[T any](slice []T, index int, defaultValue T) T {
	if index < 0 || index >= len(slice) {
		return defaultValue
	}

	return slice[index]
}
