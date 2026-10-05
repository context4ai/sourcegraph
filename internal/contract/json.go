package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// DecodeObject rejects duplicate keys at every depth, nonobjects, trailing input,
// unknown fields and Go's otherwise case-insensitive top-level field matching.
func DecodeObject(data []byte, target any) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return fmt.Errorf("expected JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := walkJSON(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("unexpected trailing input")
	}
	t := reflect.TypeOf(target)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("target must be a struct pointer")
	}
	allowed := map[string]bool{}
	for i := 0; i < t.Elem().NumField(); i++ {
		f := t.Elem().Field(i)
		n := strings.Split(f.Tag.Get("json"), ",")[0]
		if n == "" {
			n = f.Name
		}
		if n != "-" {
			allowed[n] = true
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key, value := range fields {
		if !allowed[key] {
			return fmt.Errorf("unknown field %q", key)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("null field %q", key)
		}
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func walkJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return fmt.Errorf("JSON nesting limit exceeded")
	}
	tok, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return fmt.Errorf("duplicate or invalid object key")
			}
			seen[key] = true
			if err := walkJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := walkJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid delimiter")
	}
	_, err = d.Token()
	return err
}
