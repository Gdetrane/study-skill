package plugincontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// Shape checks that a JSON value is spelled as the Go type that describes
// it: every field the contract requires is there, under exactly its name,
// and holds the kind of value it should. What the contract does not name is
// let through, since a plugin may say more. It returns what is wrong.
//
// Reading the JSON into the type does not tell: a field that is missing, or
// null, reads as the type's zero, so a cancel that answers {} would have
// stopped nothing and a search without scores would have scored 0; and a
// name in another letter case is read as if it were the contract's. study
// holds a plugin's answers to this, and the conformance suite does too.
func Shape(raw json.RawMessage, typ reflect.Type) []string { return shape("", raw, typ) }

func shape(path string, raw json.RawMessage, typ reflect.Type) []string {
	at := func(what string) string {
		if path == "" {
			return what
		}
		return path + ": " + what
	}
	raw = bytes.TrimSpace(raw)
	if typ == reflect.TypeFor[json.RawMessage]() {
		return nil // read by whoever asked for it as it is
	}
	if typ.Kind() == reflect.Pointer {
		if string(raw) == "null" {
			return nil
		}
		return shape(path, raw, typ.Elem())
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &object) != nil {
			return []string{at("it is not a JSON object")}
		}
		var problems []string
		for _, f := range fields(typ) {
			// A name in another letter case is read into the field as if
			// it were the contract's, and over the contract's own when it
			// comes later: so it is wrong whether or not the name itself
			// is there too.
			spelled := false
			for _, key := range slices.Sorted(maps.Keys(object)) {
				if key != f.name && strings.EqualFold(key, f.name) {
					problems = append(problems, at(fmt.Sprintf("%q is spelled %q", f.name, key)))
					spelled = true
				}
			}
			value, ok := object[f.name]
			if !ok {
				if !spelled && !f.optional {
					problems = append(problems, at(fmt.Sprintf("%q is missing", f.name)))
				}
				continue
			}
			if f.optional && string(bytes.TrimSpace(value)) == "null" {
				continue
			}
			problems = append(problems, shape(strings.TrimPrefix(path+"."+f.name, "."), value, f.typ)...)
		}
		return problems
	case reflect.Slice:
		var items []json.RawMessage
		if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &items) != nil {
			return []string{at("it is not a list")}
		}
		var problems []string
		for i, item := range items {
			problems = append(problems, shape(fmt.Sprintf("%s[%d]", path, i), item, typ.Elem())...)
		}
		return problems
	case reflect.String:
		if len(raw) == 0 || raw[0] != '"' {
			return []string{at("it is not text")}
		}
	case reflect.Bool:
		if string(raw) != "true" && string(raw) != "false" {
			return []string{at("it is not true or false")}
		}
	case reflect.Int:
		var n int64
		if json.Unmarshal(raw, &n) != nil {
			return []string{at("it is not a whole number")}
		}
	case reflect.Float64:
		var n float64
		if len(raw) == 0 || raw[0] == '"' || json.Unmarshal(raw, &n) != nil {
			return []string{at("it is not a number")}
		}
	}
	return nil
}

// field is one field of a contract type, as JSON names it.
type field struct {
	name     string
	optional bool
	typ      reflect.Type
}

// fields lists a contract type's fields, those of an embedded type among
// them.
func fields(typ reflect.Type) []field {
	var out []field
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.Anonymous {
			out = append(out, fields(f.Type)...)
			continue
		}
		name, options, _ := strings.Cut(f.Tag.Get("json"), ",")
		out = append(out, field{name: name, optional: strings.Contains(options, "omitempty"), typ: f.Type})
	}
	return out
}
