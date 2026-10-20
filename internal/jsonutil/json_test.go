package jsonutil

import (
	"strings"
	"testing"
)

func TestDecodeObjectRejectsAmbiguousInput(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, ``, `{} {}`, `{"typo":1}`, `{"value":1} garbage`} {
		var v struct {
			Value int `json:"value"`
		}
		if DecodeObject(strings.NewReader(raw), &v) == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	var v struct {
		Value int `json:"value"`
	}
	if err := DecodeObject(strings.NewReader(`{"value":2}`), &v); err != nil || v.Value != 2 {
		t.Fatalf("valid object: %+v, %v", v, err)
	}
}
