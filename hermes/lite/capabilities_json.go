package hermes

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// Evidence is a versioned contract, not permissive application configuration.
// Reject duplicate keys before typed decoding so Python, Node and Go cannot
// disagree about which signed field is authoritative.
func decodeDesktopEvidenceJSON(data []byte, value any) bool {
	if !utf8.Valid(data) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	remaining := 200000
	var readValue func(int) bool
	readValue = func(depth int) bool {
		remaining--
		if depth > 64 || remaining < 0 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return true
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
				if !readValue(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for decoder.More() {
				if !readValue(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !readValue(0) {
		return false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return false
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value) == nil && desktopExactJSONFields(data, reflect.TypeOf(value))
}

// encoding/json normally accepts case-insensitive struct field names. The
// evidence protocol uses exact spelling, including inside maps of descriptors.
func desktopExactJSONFields(data []byte, kind reflect.Type) bool {
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if kind == reflect.TypeFor[json.RawMessage]() {
		return true
	}
	switch kind.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			return false
		}
		allowed := map[string]reflect.Type{}
		for index := 0; index < kind.NumField(); index++ {
			field := kind.Field(index)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				allowed[name] = field.Type
			}
		}
		for name, raw := range fields {
			child, ok := allowed[name]
			if !ok || !desktopExactJSONFields(raw, child) {
				return false
			}
		}
	case reflect.Map:
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			return false
		}
		for _, raw := range fields {
			if !desktopExactJSONFields(raw, kind.Elem()) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		var fields []json.RawMessage
		if json.Unmarshal(data, &fields) != nil {
			return false
		}
		for _, raw := range fields {
			if !desktopExactJSONFields(raw, kind.Elem()) {
				return false
			}
		}
	}
	return true
}
