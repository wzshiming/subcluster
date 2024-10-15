package format

// Ptr returns the pointer of v.
func Ptr[T any](v T) *T {
	return &v
}

// ElemOrDefault returns the element of v or default.
func ElemOrDefault[T any](v *T) (t T) {
	if v == nil {
		return t
	}
	return *v
}
