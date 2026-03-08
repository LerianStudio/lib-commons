// Package safe provides panic-free helpers for math, slices, and regex operations.
//
// Core APIs include decimal division helpers (Divide, Percentage), bounds-checked
// slice accessors (First, Last, At), and regex compilation/matching with caching.
//
// Functions that can fail return explicit errors instead of panicking, so callers
// can handle failures predictably in production paths.
package safe
