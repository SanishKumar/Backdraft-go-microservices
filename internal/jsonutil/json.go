// Package jsonutil decodes configuration and API objects without silently
// accepting misspelled fields, null objects, or trailing JSON documents.
package jsonutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func DecodeObject(r io.Reader, v any) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return fmt.Errorf("expected a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON object")
	}
	return nil
}
