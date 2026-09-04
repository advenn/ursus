package dtype

// Field is a named, typed column slot.
//
// Nullable is a static property of the schema, not a count of nulls actually
// present: a non-nullable field is one where the engine may skip validity
// handling entirely. Operations propagate it conservatively — an arithmetic
// result is nullable if either operand is, a non-strict cast is always nullable,
// IsNull is never nullable.
//
// Field is comparable, because DataType is.
type Field struct {
	Name     string
	Type     DataType
	Nullable bool
}

// Of builds a nullable field. Nullable is the safe default: claiming a column has
// no nulls when it does causes silent wrong answers, while the reverse only costs
// a little speed.
func Of(name string, t DataType) Field {
	return Field{Name: name, Type: t, Nullable: true}
}

// NotNull builds a non-nullable field.
func NotNull(name string, t DataType) Field {
	return Field{Name: name, Type: t, Nullable: false}
}

// Rename returns a copy of f with a new name. Used on every naming path —
// Alias, Name().Prefix, join suffixing.
func (f Field) Rename(name string) Field {
	f.Name = name
	return f
}

// WithType returns a copy of f with a new type, preserving name and nullability.
func (f Field) WithType(t DataType) Field {
	f.Type = t
	return f
}

// AsNullable returns a copy of f that is nullable.
func (f Field) AsNullable() Field {
	f.Nullable = true
	return f
}

// Equal reports exact field equality, including nullability.
func (f Field) Equal(o Field) bool { return f == o }

// String renders "name: Type", or "name: Type!" when the field is non-nullable.
// Stable: golden files depend on it.
func (f Field) String() string {
	if f.Nullable {
		return f.Name + ": " + f.Type.String()
	}
	return f.Name + ": " + f.Type.String() + "!"
}
