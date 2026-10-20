package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var ErrInvalid = errors.New("objeto JSON inválido")

func Object(body []byte, allowed ...string) (map[string]json.RawMessage, error) {
	return Fields(body, allowed, nil)
}

func Fields(body []byte, required, optional []string) (map[string]json.RawMessage, error) {
	allowed := append(append([]string{}, required...), optional...)
	if !utf8.Valid(body) {
		return nil, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, ErrInvalid
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrInvalid
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrInvalid
		}
		known := false
		for _, candidate := range allowed {
			if key == candidate {
				known = true
				break
			}
		}
		if _, duplicate := fields[key]; !known || duplicate {
			return nil, ErrInvalid
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrInvalid
		}
		fields[key] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, ErrInvalid
	}
	for _, key := range required {
		if _, exists := fields[key]; !exists {
			return nil, ErrInvalid
		}
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	return fields, nil
}
