// Package unwrap finds a concrete driver behind wrapping drivers.
// Wrappers preserve the original contract in their Unwrap return type; this
// helper does not use reflection or search arbitrary object graphs.
//
// Example: native, found := unwrap.As[*memorycache.Driver](store.Driver()); fmt.Println(found, native != nil)
package unwrap

// As walks driver and the drivers it wraps, following Unwrap() D, and
// returns the first one of type T.
// Failure returns T's zero value and false. Wrappers must form an acyclic chain;
// a typed nil driver is returned if it matches T, like an ordinary type assertion.
//
//	if native, found := unwrap.As[*memorycache.Driver](store.Driver()); found {
//		fmt.Println(native != nil)
//	}
func As[T any, D any](driver D) (T, bool) {
	current := any(driver)

	for current != nil {
		if found, ok := current.(T); ok {
			return found, true
		}

		wrapper, ok := current.(interface{ Unwrap() D })

		if !ok {
			break
		}

		current = wrapper.Unwrap()
	}

	var zero T

	return zero, false
}
