package query

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
)

// A posição e o limite superior são ligados à carteira. O token é opaco para o
// cliente, versionado e validado; não substitui a autorização da rota.
type Cursor struct {
	Version      int    `json:"v"`
	WalletID     string `json:"w"`
	UpperVersion int64  `json:"u"`
	AfterVersion int64  `json:"a"`
	AfterID      string `json:"i"`
}

func (c Cursor) Validate(id string) error {
	if c.Version != 1 || c.WalletID != id || !ValidID(id) || c.UpperVersion < 1 || c.AfterVersion < 1 || c.AfterVersion >= c.UpperVersion || c.AfterID == "" || len(c.AfterID) > 512 {
		return ErrInvalidCursor
	}
	return nil
}
func EncodeCursor(c Cursor) (string, error) {
	if err := c.Validate(c.WalletID); err != nil {
		return "", err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
func DecodeCursor(token, id string) (Cursor, error) {
	if token == "" {
		return Cursor{}, nil
	}
	if len(token) > 2048 {
		return Cursor{}, ErrInvalidCursor
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var c Cursor
	if decoder.Decode(&c) != nil || decoder.Decode(new(any)) != io.EOF || c.Validate(id) != nil {
		return Cursor{}, ErrInvalidCursor
	}
	// Forma canônica também rejeita campos duplicados, números alternativos e lixo.
	canonical, err := EncodeCursor(c)
	if err != nil || canonical != token {
		return Cursor{}, ErrInvalidCursor
	}
	return c, nil
}
