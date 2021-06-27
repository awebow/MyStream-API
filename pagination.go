package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"time"

	"github.com/oklog/ulid/v2"
)

type pagination struct {
	searchTime time.Time
	score      float64
	id         ulid.ULID
}

func (page pagination) tokenize() string {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, page.searchTime.UnixNano())
	binary.Write(buf, binary.LittleEndian, page.score)
	buf.Write(page.id[:])

	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func parsePagination(token string) (*pagination, error) {
	page := pagination{}

	data, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}

	buf := bytes.NewBuffer(data)

	var nano int64
	binary.Read(buf, binary.LittleEndian, &nano)
	page.searchTime = time.Unix(0, nano)

	binary.Read(buf, binary.LittleEndian, &page.score)

	copy(page.id[:], data[16:])

	return &page, nil
}
