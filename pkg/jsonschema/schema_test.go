package jsonschema

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestObjectSchemaJSON(t *testing.T) {
	schema := Object(map[string]Schema{
		"path": String("Path under workspace root"),
		"mode": StringEnum("Read mode", "full", "head"),
	}, "path", "path").NoAdditionalProperties()

	assertJSON(t, schema, `{"type":"object","properties":{"mode":{"type":"string","description":"Read mode","enum":["full","head"]},"path":{"type":"string","description":"Path under workspace root"}},"required":["path"],"additionalProperties":false}`)
}

func TestObjPropertyDSL(t *testing.T) {
	schema := Obj(
		Required("query", Str{
			Description: "Literal text to search for",
			MinLength:   new(1),
		}),
		Optional("case_sensitive", Bool{
			Description: "Whether matching is case-sensitive",
			Default:     false,
			HasDefault:  true,
		}),
		Optional("limit", Int{
			Description: "Maximum matches",
			Default:     50,
			Minimum:     new(0),
			Maximum:     new(200),
		}),
		Optional("score", Num{
			Description: "Score",
			Minimum:     new(0.0),
			Maximum:     new(1.0),
			MultipleOf:  new(0.25),
		}),
		Optional("tags", Arr{
			Description: "Tags",
			Items: Str{
				Description: "Tag",
			},
			MinItems:    new(0),
			MaxItems:    new(4),
			UniqueItems: new(false),
		}),
		Optional("", String("Ignored")),
	).NoAdditionalProperties()

	assertJSON(t, schema, `{"type":"object","properties":{"case_sensitive":{"type":"boolean","description":"Whether matching is case-sensitive","default":false},"limit":{"type":"integer","description":"Maximum matches","default":50,"minimum":0,"maximum":200},"query":{"type":"string","description":"Literal text to search for","minLength":1},"score":{"type":"number","description":"Score","minimum":0,"maximum":1,"multipleOf":0.25},"tags":{"type":"array","description":"Tags","items":{"type":"string","description":"Tag"},"minItems":0,"maxItems":4,"uniqueItems":false}},"required":["query"],"additionalProperties":false}`)
}

func TestTypedPropertySpecsCanEmitZeroDefaults(t *testing.T) {
	schema := Obj(
		Optional("empty", Str{
			Default:    "",
			HasDefault: true,
		}),
		Optional("zero", Int{
			Default:    0,
			HasDefault: true,
		}),
		Optional("off", Bool{
			Default:    false,
			HasDefault: true,
		}),
	).NoAdditionalProperties()

	assertJSON(t, schema, `{"type":"object","properties":{"empty":{"type":"string","default":""},"off":{"type":"boolean","default":false},"zero":{"type":"integer","default":0}},"additionalProperties":false}`)
}

func TestArraySchemaJSON(t *testing.T) {
	schema := Array(Integer("Item id"), "Ids")

	assertJSON(t, schema, `{"type":"array","description":"Ids","items":{"type":"integer","description":"Item id"}}`)
}

func TestStringConstraintsJSON(t *testing.T) {
	schema := Str{
		Title:       "Email address",
		Description: "Email",
		Default:     "hello@example.com",
		Examples:    []string{"a@example.com", "b@example.com"},
		Format:      "email",
		Pattern:     ".+@.+",
		MinLength:   new(3),
		MaxLength:   new(254),
	}.BuildSchema()

	assertJSON(t, schema, `{"type":"string","title":"Email address","description":"Email","default":"hello@example.com","examples":["a@example.com","b@example.com"],"format":"email","pattern":".+@.+","minLength":3,"maxLength":254}`)
}

func TestNumericAndArrayConstraintsKeepZeroValues(t *testing.T) {
	schema := Arr{
		Description: "Scores",
		Items: Num{
			Description: "Score",
			Minimum:     new(0.0),
			Maximum:     new(1.0),
			MultipleOf:  new(0.25),
		},
		MinItems:    new(0),
		MaxItems:    new(4),
		UniqueItems: new(false),
	}.BuildSchema()

	assertJSON(t, schema, `{"type":"array","description":"Scores","items":{"type":"number","description":"Score","minimum":0,"maximum":1,"multipleOf":0.25},"minItems":0,"maxItems":4,"uniqueItems":false}`)
}

func TestMapConstNullableRefAndDefs(t *testing.T) {
	schema := Obj(
		Required("id", Ref("#/$defs/id")),
		Required("nickname", String("Display name").Nullable()),
		Optional("labels", Map(String("Label value"), "Free-form labels")),
	).
		WithDef("id", Int{Description: "Identifier", Minimum: new(0)}).
		NoAdditionalProperties()

	assertJSON(t, schema, `{"type":"object","properties":{"id":{"$ref":"#/$defs/id"},"labels":{"type":"object","description":"Free-form labels","additionalProperties":{"type":"string","description":"Label value"}},"nickname":{"description":"Display name","anyOf":[{"type":"string"},{"type":"null"}]}},"required":["id","nickname"],"$defs":{"id":{"type":"integer","description":"Identifier","minimum":0}},"additionalProperties":false}`)
}

func TestCompositionBuildersJSON(t *testing.T) {
	schema := AnyOf(
		String("Name"),
		AllOf(Int{Description: "Count", Minimum: new(1)}, Not(Const("Forbidden", 13))),
	).WithDescription("Flexible value")

	assertJSON(t, schema, `{"description":"Flexible value","anyOf":[{"type":"string","description":"Name"},{"allOf":[{"type":"integer","description":"Count","minimum":1},{"not":{"description":"Forbidden","const":13}}]}]}`)
}

func TestConstAndDefaultCanBeNull(t *testing.T) {
	schema := Const("Null marker", nil)
	schema.Default = anyPtr(nil)

	assertJSON(t, schema, `{"description":"Null marker","const":null,"default":null}`)
}

func TestWithDefDoesNotMutateOriginal(t *testing.T) {
	base := Object(map[string]Schema{"a": String("A")}).WithDef("base", String("Base"))
	next := base.WithDef("next", String("Next"))

	if _, ok := base.Defs["next"]; ok {
		t.Fatalf("WithDef mutated original defs: %#v", base.Defs)
	}
	if _, ok := next.Defs["base"]; !ok {
		t.Fatalf("next schema missed def base: %#v", next.Defs)
	}
	if _, ok := next.Defs["next"]; !ok {
		t.Fatalf("next schema missed def next: %#v", next.Defs)
	}
}

func TestStrictSchemaRequiresAllObjectPropertiesRecursively(t *testing.T) {
	schema := Obj(
		Optional("name", String("Name")),
		Optional("profile", Obj(
			Optional("age", Integer("Age")),
			Optional("tags", Array(Obj(
				Optional("label", String("Label")),
			), "Tags")),
		)),
	).WithDef("metadata", Obj(
		Optional("source", String("Source")),
	)).Strict()

	assertJSON(t, schema, `{"type":"object","properties":{"name":{"type":"string","description":"Name"},"profile":{"type":"object","properties":{"age":{"type":"integer","description":"Age"},"tags":{"type":"array","description":"Tags","items":{"type":"object","properties":{"label":{"type":"string","description":"Label"}},"required":["label"],"additionalProperties":false}}},"required":["age","tags"],"additionalProperties":false}},"required":["name","profile"],"$defs":{"metadata":{"type":"object","properties":{"source":{"type":"string","description":"Source"}},"required":["source"],"additionalProperties":false}},"additionalProperties":false}`)
}

func TestStrictKeepsMapValueSchema(t *testing.T) {
	schema := Map(Obj(
		Optional("label", String("Label")),
	), "Free-form labels").Strict()

	assertJSON(t, schema, `{"type":"object","description":"Free-form labels","additionalProperties":{"type":"object","properties":{"label":{"type":"string","description":"Label"}},"required":["label"],"additionalProperties":false}}`)
}

func TestStrictDoesNotMutateOriginalSchema(t *testing.T) {
	base := Obj(
		Optional("nested", Obj(
			Optional("value", String("Value")),
		)),
	)

	strict := base.Strict()

	if len(base.Required) != 0 {
		t.Fatalf("base required = %#v, want empty", base.Required)
	}
	if base.AdditionalProperties != nil {
		t.Fatalf("base additionalProperties = %#v, want nil", base.AdditionalProperties)
	}
	if len(base.Properties["nested"].Required) != 0 {
		t.Fatalf("nested base required = %#v, want empty", base.Properties["nested"].Required)
	}
	if strict.Properties["nested"].AdditionalProperties != false {
		t.Fatalf("strict nested additionalProperties = %#v, want false", strict.Properties["nested"].AdditionalProperties)
	}
}

func assertJSON(t *testing.T, schema Schema, want string) {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var gotValue any
	if err := json.Unmarshal(data, &gotValue); err != nil {
		t.Fatalf("Unmarshal(got) error = %v; json=%s", err, data)
	}
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("Unmarshal(want) error = %v; json=%s", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("json = %s, want %s", data, want)
	}
}
