# jsonschema

Small JSON Schema helpers shared by provider-neutral LLM APIs.

The package intentionally uses explicit schema structs and builders rather than
reflection-based generation. Tool definitions and structured outputs can share
the same representation while applications keep full control over the schema
shape.

```go
schema := jsonschema.Obj(
	jsonschema.Required("path", jsonschema.Str{
		Description: "Path under workspace root",
	}),
).NoAdditionalProperties()
```

Typed builders are regular values. `Required` and `Optional` keep object
requiredness next to the field declaration:

```go
schema := jsonschema.Obj(
	jsonschema.Required("query", jsonschema.Str{
		Description: "Literal text to search for",
		MinLength:   new(1),
	}),
	jsonschema.Optional("case_sensitive", jsonschema.Bool{
		Description: "Whether matching is case-sensitive",
		Default:     false,
		HasDefault:  true,
	}),
	jsonschema.Optional("limit", jsonschema.Int{
		Description: "Maximum matches",
		Default:     50,
		Minimum:     new(1),
		Maximum:     new(200),
	}),
).NoAdditionalProperties()
```

`Default` is a plain typed value. When the intended default is the type's zero
value, such as `false`, `0`, or `""`, set `HasDefault: true` too.

The lower-level `Object(map[string]Schema, required...)` builder remains
available when constructing maps programmatically. The typed specs are the
canonical way to declare constraints; `Schema` fields are exported for the
rare case a keyword has no spec equivalent.

Common JSON Schema keywords are available for strings, numbers, arrays, object
maps, composition, refs, constants, defaults, and nullable values:

```go
schema := jsonschema.Obj(
	jsonschema.Required("id", jsonschema.Ref("#/$defs/id")),
	jsonschema.Optional("nickname", jsonschema.String("Display name").Nullable()),
	jsonschema.Optional("labels", jsonschema.Map(jsonschema.String("Label value"), "Free-form labels")),
).
	WithDef("id", jsonschema.Int{Description: "Identifier", Minimum: new(0)}).
	NoAdditionalProperties()
```

`Schema.Strict()` returns a copy transformed for OpenAI strict mode: every
object recursively gets `additionalProperties: false` and all properties
required. Map schemas (schema-valued `additionalProperties`) are kept as-is;
strict-mode providers that do not support them will reject the request
explicitly.

The package does not validate JSON instances. It only builds schema documents
for providers and other packages to serialize.
