package main

import (
	"bytes"
	"encoding/json"

	"github.com/jmoiron/sqlx"
)

func RowToJSON(rows *sqlx.Rows) ([]byte, error) {
	cols, err := rows.Columns()
	if err != nil {
		return []byte{}, err
	}

	vals, err := rows.SliceScan()
	if err != nil {
		return []byte{}, err
	}

	buffer := new(bytes.Buffer)
	buffer.WriteByte('{')
	for i := range cols {
		if i > 0 {
			buffer.WriteByte(',')
		}

		buffer.WriteByte('"')
		buffer.WriteString(cols[i])
		buffer.WriteString(`":`)

		if b, ok := vals[i].([]byte); ok {
			vals[i] = string(b)
		}

		v, err := json.Marshal(vals[i])
		if err != nil {
			return []byte{}, err
		}
		buffer.Write(v)
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

func RowsToJSON(rows *sqlx.Rows) ([]byte, error) {
	buffer := new(bytes.Buffer)
	buffer.WriteByte('[')

	first := true
	for rows.Next() {
		if !first {
			buffer.WriteByte(',')
		}

		data, err := RowToJSON(rows)
		if err != nil {
			return []byte{}, err
		}

		buffer.Write(data)
		first = false
	}

	buffer.WriteByte(']')
	return buffer.Bytes(), nil
}
