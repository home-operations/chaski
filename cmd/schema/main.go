// Command schema prints the JSON Schema for chaski's route-config file
// (chaski.yaml: routes + targets) to stdout. It is a dev/CI tool kept separate
// from the runtime binary so the JSON Schema reflection dependency is never
// linked into chaski itself.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/home-operations/chaski/internal/config"
	"github.com/invopop/jsonschema"
)

// typeString is the JSON Schema primitive name for chaski's string-valued config
// fields (named to satisfy goconst, which flags the repeated literal).
const typeString = "string"

func main() {
	r := &jsonschema.Reflector{
		// Property names come from the yaml tags the loader actually decodes.
		// chaski's config is yaml-only (no json tags), so required-ness is opt-in
		// via `jsonschema:"required"` rather than the absence of `,omitempty`.
		FieldNameTag:               "yaml",
		RequiredFromJSONSchemaTags: true,
		// A typo'd key (e.g. `tilte:`) should be flagged, not silently ignored —
		// the Reflector already emits additionalProperties:false for structs.
	}
	r.Mapper = customTypes

	schema := r.Reflect(&config.RouteConfig{})

	// A Target is an externally-tagged union: exactly one of apprise|http. Struct
	// tags can't express "exactly one", so constrain the generated
	// definition — an object matching both branches (or neither) fails oneOf.
	if target, ok := schema.Definitions["Target"]; ok {
		target.OneOf = []*jsonschema.Schema{
			{Required: []string{"apprise"}},
			{Required: []string{"http"}},
		}
	}

	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "schema: marshal:", err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}

// customTypes maps chaski's custom config types to the shapes a config author
// writes, not their Go underlying representations (int64, []string) that naive
// reflection would emit.
func customTypes(t reflect.Type) *jsonschema.Schema {
	switch t {
	case reflect.TypeFor[config.Duration]():
		// Duration decodes from a YAML string like "10s", not the int64 it wraps.
		// (The examples render; invopop drops Description on Mapper schemas.)
		return &jsonschema.Schema{
			Type:     typeString,
			Examples: []any{"10s", "500ms", "1m30s"},
		}
	case reflect.TypeFor[config.StringList]():
		// StringList accepts either a single scalar or a sequence of scalars.
		return &jsonschema.Schema{
			OneOf: []*jsonschema.Schema{
				{Type: typeString},
				{Type: "array", Items: &jsonschema.Schema{Type: typeString}},
			},
		}
	case reflect.TypeFor[config.TargetRefs]():
		// A target is a name or a {name, whenExpr} object; the field is one such
		// entry or a list of them.
		props := jsonschema.NewProperties()
		props.Set("name", &jsonschema.Schema{Type: typeString})
		props.Set("whenExpr", &jsonschema.Schema{Type: typeString})
		obj := &jsonschema.Schema{
			Type:                 "object",
			Properties:           props,
			Required:             []string{"name"},
			AdditionalProperties: jsonschema.FalseSchema,
		}
		entry := &jsonschema.Schema{OneOf: []*jsonschema.Schema{{Type: typeString}, obj}}
		return &jsonschema.Schema{
			OneOf: []*jsonschema.Schema{
				{Type: typeString},
				obj,
				{Type: "array", Items: entry},
			},
		}
	}
	return nil
}
