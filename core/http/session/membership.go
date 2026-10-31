package session

// Field joins a nonempty literal member name and binds it to T. Empty names or
// invalid ancestors produce [ErrInvalidField] at use, even when the composed
// name is nonempty. No lifetime or schema metadata is added to persisted JSON.
//
// Example:
//
//	theme := session.NewNamespace[string]("preferences").Field("theme")
//	if err := current.Put(theme, "dark"); err != nil {
//		return err
//	}
func (space Namespace[T]) Field(name string) Field[T] {
	field := Field[T]{name: space.name + space.separator + name, err: space.Err()}

	if name == "" {
		field.err = ErrInvalidField
	}

	return field
}
