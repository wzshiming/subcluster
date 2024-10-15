package slices

import (
	"slices"
)

// Clone returns a copy of the slice.
func Clone[S ~[]E, E any](s S) S {
	return slices.Clone(s)
}
