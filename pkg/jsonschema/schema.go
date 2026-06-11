package jsonschema

import "sort"

// Type is a JSON Schema primitive type name.
type Type string

const (
	TypeObject  Type = "object"
	TypeArray   Type = "array"
	TypeString  Type = "string"
	TypeNumber  Type = "number"
	TypeInteger Type = "integer"
	TypeBoolean Type = "boolean"
	TypeNull    Type = "null"
)

// Schema is a small explicit JSON Schema representation for LLM tools and
// structured outputs. It intentionally covers common JSON Schema keywords
// without attempting reflection-based schema generation.
type Schema struct {
	Type                 Type              `json:"type,omitempty"`
	Title                string            `json:"title,omitempty"`
	Description          string            `json:"description,omitempty"`
	Properties           map[string]Schema `json:"properties,omitempty"`
	Required             []string          `json:"required,omitempty"`
	Items                *Schema           `json:"items,omitempty"`
	Enum                 []any             `json:"enum,omitempty"`
	Const                *any              `json:"const,omitempty"`
	Default              *any              `json:"default,omitempty"`
	Examples             []any             `json:"examples,omitempty"`
	Format               string            `json:"format,omitempty"`
	Pattern              string            `json:"pattern,omitempty"`
	MinLength            *int              `json:"minLength,omitempty"`
	MaxLength            *int              `json:"maxLength,omitempty"`
	Minimum              *float64          `json:"minimum,omitempty"`
	Maximum              *float64          `json:"maximum,omitempty"`
	ExclusiveMinimum     *float64          `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum     *float64          `json:"exclusiveMaximum,omitempty"`
	MultipleOf           *float64          `json:"multipleOf,omitempty"`
	MinItems             *int              `json:"minItems,omitempty"`
	MaxItems             *int              `json:"maxItems,omitempty"`
	UniqueItems          *bool             `json:"uniqueItems,omitempty"`
	MinProperties        *int              `json:"minProperties,omitempty"`
	MaxProperties        *int              `json:"maxProperties,omitempty"`
	AnyOf                []Schema          `json:"anyOf,omitempty"`
	OneOf                []Schema          `json:"oneOf,omitempty"`
	AllOf                []Schema          `json:"allOf,omitempty"`
	Not                  *Schema           `json:"not,omitempty"`
	Ref                  string            `json:"$ref,omitempty"`
	Defs                 map[string]Schema `json:"$defs,omitempty"`
	AdditionalProperties any               `json:"additionalProperties,omitempty"`
}

type SchemaBuilder interface {
	BuildSchema() Schema
}

func (s Schema) BuildSchema() Schema {
	return s
}

// Property is an object property declaration used by Obj. It keeps the
// required flag next to the property schema, which is usually easier to read
// when defining tool parameters by hand.
type Property struct {
	Name     string
	Schema   Schema
	Required bool
}

func Required(name string, schema SchemaBuilder) Property {
	return Property{Name: name, Schema: buildSchema(schema), Required: true}
}

func Optional(name string, schema SchemaBuilder) Property {
	return Property{Name: name, Schema: buildSchema(schema)}
}

func Obj(properties ...Property) Schema {
	props := make(map[string]Schema, len(properties))
	required := make([]string, 0, len(properties))
	for _, property := range properties {
		if property.Name == "" {
			continue
		}
		props[property.Name] = property.Schema
		if property.Required {
			required = append(required, property.Name)
		}
	}
	return Object(props, required...)
}

func Object(properties map[string]Schema, required ...string) Schema {
	return Schema{
		Type:       TypeObject,
		Properties: cloneSchemaMap(properties),
		Required:   cleanStrings(required),
	}
}

type Str struct {
	Title       string
	Description string
	Default     string
	HasDefault  bool
	Const       *string
	Enum        []string
	Examples    []string
	Format      string
	Pattern     string
	MinLength   *int
	MaxLength   *int
}

func (s Str) BuildSchema() Schema {
	out := String(s.Description)
	out.Title = s.Title
	if s.HasDefault || s.Default != "" {
		out.Default = anyPtr(s.Default)
	}
	if s.Const != nil {
		out.Const = anyPtr(*s.Const)
	}
	if len(s.Enum) > 0 {
		out.Enum = stringsToAny(s.Enum)
	}
	if len(s.Examples) > 0 {
		out.Examples = stringsToAny(s.Examples)
	}
	out.Format = s.Format
	out.Pattern = s.Pattern
	out.MinLength = cloneIntPtr(s.MinLength)
	out.MaxLength = cloneIntPtr(s.MaxLength)
	return out
}

type Int struct {
	Title            string
	Description      string
	Default          int
	HasDefault       bool
	Const            *int
	Enum             []int
	Examples         []int
	Minimum          *int
	Maximum          *int
	ExclusiveMinimum *int
	ExclusiveMaximum *int
	MultipleOf       *int
}

func (s Int) BuildSchema() Schema {
	out := Integer(s.Description)
	out.Title = s.Title
	if s.HasDefault || s.Default != 0 {
		out.Default = anyPtr(s.Default)
	}
	if s.Const != nil {
		out.Const = anyPtr(*s.Const)
	}
	if len(s.Enum) > 0 {
		out.Enum = intsToAny(s.Enum)
	}
	if len(s.Examples) > 0 {
		out.Examples = intsToAny(s.Examples)
	}
	out.Minimum = intAsFloatPtr(s.Minimum)
	out.Maximum = intAsFloatPtr(s.Maximum)
	out.ExclusiveMinimum = intAsFloatPtr(s.ExclusiveMinimum)
	out.ExclusiveMaximum = intAsFloatPtr(s.ExclusiveMaximum)
	out.MultipleOf = intAsFloatPtr(s.MultipleOf)
	return out
}

type Num struct {
	Title            string
	Description      string
	Default          float64
	HasDefault       bool
	Const            *float64
	Enum             []float64
	Examples         []float64
	Minimum          *float64
	Maximum          *float64
	ExclusiveMinimum *float64
	ExclusiveMaximum *float64
	MultipleOf       *float64
}

func (s Num) BuildSchema() Schema {
	out := Number(s.Description)
	out.Title = s.Title
	if s.HasDefault || s.Default != 0 {
		out.Default = anyPtr(s.Default)
	}
	if s.Const != nil {
		out.Const = anyPtr(*s.Const)
	}
	if len(s.Enum) > 0 {
		out.Enum = floatsToAny(s.Enum)
	}
	if len(s.Examples) > 0 {
		out.Examples = floatsToAny(s.Examples)
	}
	out.Minimum = cloneFloatPtr(s.Minimum)
	out.Maximum = cloneFloatPtr(s.Maximum)
	out.ExclusiveMinimum = cloneFloatPtr(s.ExclusiveMinimum)
	out.ExclusiveMaximum = cloneFloatPtr(s.ExclusiveMaximum)
	out.MultipleOf = cloneFloatPtr(s.MultipleOf)
	return out
}

type Bool struct {
	Title       string
	Description string
	Default     bool
	HasDefault  bool
	Const       *bool
	Enum        []bool
	Examples    []bool
}

func (s Bool) BuildSchema() Schema {
	out := Boolean(s.Description)
	out.Title = s.Title
	if s.HasDefault || s.Default {
		out.Default = anyPtr(s.Default)
	}
	if s.Const != nil {
		out.Const = anyPtr(*s.Const)
	}
	if len(s.Enum) > 0 {
		out.Enum = boolsToAny(s.Enum)
	}
	if len(s.Examples) > 0 {
		out.Examples = boolsToAny(s.Examples)
	}
	return out
}

type Arr struct {
	Title       string
	Description string
	Items       SchemaBuilder
	MinItems    *int
	MaxItems    *int
	UniqueItems *bool
}

func (s Arr) BuildSchema() Schema {
	out := Schema{
		Type:        TypeArray,
		Title:       s.Title,
		Description: s.Description,
		MinItems:    cloneIntPtr(s.MinItems),
		MaxItems:    cloneIntPtr(s.MaxItems),
		UniqueItems: cloneBoolPtr(s.UniqueItems),
	}
	if s.Items != nil {
		items := buildSchema(s.Items)
		out.Items = &items
	}
	return out
}

func Array(items SchemaBuilder, description string) Schema {
	itemSchema := buildSchema(items)
	return Schema{
		Type:        TypeArray,
		Description: description,
		Items:       &itemSchema,
	}
}

func Map(values SchemaBuilder, description string) Schema {
	return Object(nil).WithDescription(description).WithAdditionalProperties(buildSchema(values))
}

func String(description string) Schema {
	return Schema{
		Type:        TypeString,
		Description: description,
	}
}

func Number(description string) Schema {
	return Schema{
		Type:        TypeNumber,
		Description: description,
	}
}

func Integer(description string) Schema {
	return Schema{
		Type:        TypeInteger,
		Description: description,
	}
}

func Boolean(description string) Schema {
	return Schema{
		Type:        TypeBoolean,
		Description: description,
	}
}

func Null(description string) Schema {
	return Schema{
		Type:        TypeNull,
		Description: description,
	}
}

func Any(description string) Schema {
	return Schema{Description: description}
}

func Enum(description string, values ...any) Schema {
	return Schema{
		Description: description,
		Enum:        cloneAnySlice(values),
	}
}

func StringEnum(description string, values ...string) Schema {
	enum := make([]any, 0, len(values))
	for _, value := range values {
		enum = append(enum, value)
	}
	return Schema{
		Type:        TypeString,
		Description: description,
		Enum:        enum,
	}
}

func Const(description string, value any) Schema {
	return Schema{
		Description: description,
		Const:       anyPtr(value),
	}
}

func Ref(ref string) Schema {
	return Schema{Ref: ref}
}

func AnyOf(schemas ...SchemaBuilder) Schema {
	return Schema{AnyOf: buildSchemas(schemas)}
}

func OneOf(schemas ...SchemaBuilder) Schema {
	return Schema{OneOf: buildSchemas(schemas)}
}

func AllOf(schemas ...SchemaBuilder) Schema {
	return Schema{AllOf: buildSchemas(schemas)}
}

func Not(schema SchemaBuilder) Schema {
	out := buildSchema(schema)
	return Schema{Not: &out}
}

// WithDescription returns a copy with the description set. Schema constraints
// themselves are declared via the typed specs (Str, Int, Num, Bool, Arr) or by
// setting Schema fields directly.
func (s Schema) WithDescription(description string) Schema {
	s.Description = description
	return s
}

func (s Schema) WithDef(name string, schema SchemaBuilder) Schema {
	if name == "" {
		return s
	}
	s.Defs = cloneSchemaMap(s.Defs)
	if s.Defs == nil {
		s.Defs = make(map[string]Schema, 1)
	}
	s.Defs[name] = buildSchema(schema)
	return s
}

func (s Schema) Nullable() Schema {
	description := s.Description
	title := s.Title
	s.Description = ""
	s.Title = ""
	out := AnyOf(s, Null(""))
	out.Description = description
	out.Title = title
	return out
}

func (s Schema) Strict() Schema {
	out := s.clone()
	out.applyStrict()
	return out
}

func (s Schema) WithAdditionalProperties(value any) Schema {
	if builder, ok := value.(SchemaBuilder); ok {
		value = buildSchema(builder)
	}
	s.AdditionalProperties = value
	return s
}

func (s Schema) NoAdditionalProperties() Schema {
	s.AdditionalProperties = false
	return s
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func buildSchema(schema SchemaBuilder) Schema {
	if schema == nil {
		return Schema{}
	}
	return schema.BuildSchema()
}

func buildSchemas(schemas []SchemaBuilder) []Schema {
	if len(schemas) == 0 {
		return nil
	}
	out := make([]Schema, 0, len(schemas))
	for _, schema := range schemas {
		out = append(out, buildSchema(schema))
	}
	return out
}

func cloneSchemaMap(in map[string]Schema) map[string]Schema {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]Schema, len(in))
	for key, value := range in {
		out[key] = value.clone()
	}
	return out
}

func (s Schema) clone() Schema {
	out := s
	out.Properties = cloneSchemaMap(s.Properties)
	out.Required = cloneStringSlice(s.Required)
	out.Enum = cloneAnySlice(s.Enum)
	out.Default = cloneAnyPtr(s.Default)
	out.Const = cloneAnyPtr(s.Const)
	out.Examples = cloneAnySlice(s.Examples)
	out.Items = cloneSchemaPtr(s.Items)
	out.AnyOf = cloneSchemaSlice(s.AnyOf)
	out.OneOf = cloneSchemaSlice(s.OneOf)
	out.AllOf = cloneSchemaSlice(s.AllOf)
	out.Not = cloneSchemaPtr(s.Not)
	out.Defs = cloneSchemaMap(s.Defs)
	out.AdditionalProperties = cloneAdditionalProperties(s.AdditionalProperties)
	return out
}

func (s *Schema) applyStrict() {
	for name, property := range s.Properties {
		property.applyStrict()
		s.Properties[name] = property
	}
	if len(s.Properties) > 0 {
		s.Required = sortedSchemaKeys(s.Properties)
	}
	// Schema-valued additionalProperties (Map) is kept, not replaced with false:
	// silently closing the object would make every instance invalid. Providers
	// that forbid map schemas in strict mode will reject it explicitly.
	switch additional := s.AdditionalProperties.(type) {
	case Schema:
		additional.applyStrict()
		s.AdditionalProperties = additional
	case *Schema:
		if additional != nil {
			additional.applyStrict()
		}
	default:
		if s.Type == TypeObject || len(s.Properties) > 0 {
			s.AdditionalProperties = false
		}
	}
	if s.Items != nil {
		s.Items.applyStrict()
	}
	for i := range s.AnyOf {
		s.AnyOf[i].applyStrict()
	}
	for i := range s.OneOf {
		s.OneOf[i].applyStrict()
	}
	for i := range s.AllOf {
		s.AllOf[i].applyStrict()
	}
	if s.Not != nil {
		s.Not.applyStrict()
	}
	for name, def := range s.Defs {
		def.applyStrict()
		s.Defs[name] = def
	}
}

func sortedSchemaKeys(values map[string]Schema) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneSchemaPtr(in *Schema) *Schema {
	if in == nil {
		return nil
	}
	out := in.clone()
	return &out
}

func cloneSchemaSlice(in []Schema) []Schema {
	if len(in) == 0 {
		return nil
	}
	out := make([]Schema, len(in))
	for i, schema := range in {
		out[i] = schema.clone()
	}
	return out
}

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneAnyPtr(in *any) *any {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneAdditionalProperties(value any) any {
	switch typed := value.(type) {
	case Schema:
		return typed.clone()
	case *Schema:
		return cloneSchemaPtr(typed)
	default:
		return value
	}
}

func cloneAnySlice(in []any) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	copy(out, in)
	return out
}

func stringsToAny(in []string) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	for i, value := range in {
		out[i] = value
	}
	return out
}

func intsToAny(in []int) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	for i, value := range in {
		out[i] = value
	}
	return out
}

func floatsToAny(in []float64) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	for i, value := range in {
		out[i] = value
	}
	return out
}

func boolsToAny(in []bool) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	for i, value := range in {
		out[i] = value
	}
	return out
}

func cloneIntPtr(in *int) *int {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneFloatPtr(in *float64) *float64 {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneBoolPtr(in *bool) *bool {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func intAsFloatPtr(in *int) *float64 {
	if in == nil {
		return nil
	}
	value := float64(*in)
	return &value
}

func anyPtr(value any) *any {
	return &value
}
