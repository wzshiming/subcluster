package maps

// Keys returns the keys of the map m.
// The keys will be in an indeterminate order.
func Keys[M ~map[K]V, K comparable, V any](m M) []K {
	r := make([]K, 0, len(m))
	for k := range m {
		r = append(r, k)
	}
	return r
}

// Values returns the values of the map m.
// The values will be in an indeterminate order.
func Values[M ~map[K]V, K comparable, V any](m M) []V {
	r := make([]V, 0, len(m))
	for _, v := range m {
		r = append(r, v)
	}
	return r
}

// Merge merges the maps in m into a single map.
// If a key is present in multiple maps, the value from the last map will be used.
func Merge[M ~map[K]V, K comparable, V any](m ...M) M {
	if len(m) == 0 {
		return nil
	}
	if len(m) == 1 {
		return m[0]
	}

	r := make(M)
	for _, m := range m {
		for k, v := range m {
			r[k] = v
		}
	}
	return r
}
