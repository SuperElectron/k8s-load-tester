package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The pipeline's extraction schemas are Pydantic models: every field is
// Optional (anyOf [T, null]), nested models live under $defs. The reply fills
// every field with a plausible value so the downstream steps (save, link,
// rules) see a document with content, not an empty one.

const maxSchemaDepth = 12

var (
	dateField   = regexp.MustCompile(`^(data|date)(_|$)|scadenza|^prossima_|^validita`)
	expiryField = regexp.MustCompile(`scadenza|^prossima_|^validita|^data_fine`)
)

type schemaGen struct {
	defs  map[string]any
	today time.Time
}

// instanceFor returns a value valid for the JSON schema root.
func instanceFor(root map[string]any, today time.Time) (any, error) {
	defs, _ := root["$defs"].(map[string]any)
	g := schemaGen{defs: defs, today: today}
	return g.value(root, "", 0)
}

func (g schemaGen) value(s map[string]any, field string, depth int) (any, error) {
	if depth > maxSchemaDepth {
		return nil, fmt.Errorf("schema nested deeper than %d at %q", maxSchemaDepth, field)
	}
	if ref, ok := s["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		def, ok := g.defs[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("unresolved $ref %q", ref)
		}
		return g.value(def, field, depth+1)
	}
	if c, ok := s["const"]; ok {
		return c, nil
	}
	if enum, ok := s["enum"].([]any); ok && len(enum) > 0 {
		return enum[0], nil
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		variants, ok := s[key].([]any)
		if !ok {
			continue
		}
		for _, v := range variants {
			vs, ok := v.(map[string]any)
			if !ok || vs["type"] == "null" {
				continue
			}
			return g.value(vs, field, depth+1)
		}
		return nil, nil
	}

	switch t := s["type"].(type) {
	case string:
		return g.typed(t, s, field, depth)
	case []any:
		for _, v := range t {
			if name, ok := v.(string); ok && name != "null" {
				return g.typed(name, s, field, depth)
			}
		}
		return nil, nil
	}
	if _, ok := s["properties"]; ok {
		return g.typed("object", s, field, depth)
	}
	return nil, fmt.Errorf("no type for field %q", field)
}

func (g schemaGen) typed(t string, s map[string]any, field string, depth int) (any, error) {
	switch t {
	case "object":
		props, _ := s["properties"].(map[string]any)
		out := make(map[string]any, len(props))
		for name, p := range props {
			ps, ok := p.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("property %q is not a schema", name)
			}
			v, err := g.value(ps, name, depth+1)
			if err != nil {
				return nil, err
			}
			out[name] = v
		}
		return out, nil
	case "array":
		n := 1
		if m, ok := s["minItems"].(float64); ok && int(m) > n {
			n = int(m)
		}
		items, _ := s["items"].(map[string]any)
		if items == nil {
			items = map[string]any{"type": "string"}
		}
		out := make([]any, 0, n)
		for range n {
			v, err := g.value(items, field, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case "string":
		if s["format"] == "date" || dateField.MatchString(field) {
			return g.date(field), nil
		}
		if s["format"] == "date-time" {
			return g.today.Format(time.RFC3339), nil
		}
		if field == "" {
			return "mock", nil
		}
		return "mock " + strings.ReplaceAll(field, "_", " "), nil
	case "integer":
		return 1, nil
	case "number":
		return 1.0, nil
	case "boolean":
		return true, nil
	case "null":
		return nil, nil
	}
	return nil, fmt.Errorf("unsupported type %q for field %q", t, field)
}

// date keeps the document valid: an expiry a year ahead, any other date
// (issue, visit, course) a month back.
func (g schemaGen) date(field string) string {
	if expiryField.MatchString(field) {
		return g.today.AddDate(1, 0, 0).Format(time.DateOnly)
	}
	return g.today.AddDate(0, -1, 0).Format(time.DateOnly)
}
