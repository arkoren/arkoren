// Package index owns Meilisearch settings, document fields, and query syntax.
package index

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strconv"
	"strings"

	"arkoren.dev/core/service/search"
)

const (
	// IDField is the deployed primary-key field added to each document.
	IDField = "arkoren_id"
	// idField is the same field used by the private codecs.
	idField = IDField
	// orderField preserves byte-ordered IDs under case-insensitive ranking.
	orderField = "arkoren_order"
	// reservedPrefix protects driver-owned document fields.
	reservedPrefix = "arkoren_"
	// scoreField is the native ranking score returned with hits.
	scoreField = "_rankingScore"
)

// orderKey encodes a document ID so that Meilisearch, which compares strings
// case-insensitively, orders IDs in byte order: every byte becomes two
// lowercase letters, a for 0 through p for 15, high nibble first. The encoded
// strings are all lowercase, so case folding leaves them alone, and they
// compare like the IDs' bytes.
func orderKey(id string) string {
	key := make([]byte, 0, 2*len(id))

	for _, char := range []byte(id) {
		key = append(key, 'a'+char>>4, 'a'+char&0xf)
	}

	return string(key)
}

// EncodeDocument returns the Meilisearch document for doc: its fields plus
// arkoren_id and arkoren_order. It returns an error wrapping
// [search.ErrUnsupported] when doc has a driver-reserved or native score field.
func EncodeDocument(doc search.Document) (jsontext.Value, error) {
	var fields map[string]jsontext.Value

	if err := json.Unmarshal(doc.Source, &fields); err != nil {
		return nil, fmt.Errorf("document %s: %w", doc.ID, err)
	}

	for name := range fields {
		if strings.HasPrefix(name, reservedPrefix) || name == scoreField {
			return nil, fmt.Errorf("document %s: field %q: %w: driver and native score fields are reserved", doc.ID, name, search.ErrUnsupported)
		}
	}

	// IDs and their order keys hold only [A-Za-z0-9_-], which needs no escaping.
	fields[idField] = jsontext.Value(strconv.Quote(doc.ID))
	fields[orderField] = jsontext.Value(strconv.Quote(orderKey(doc.ID)))

	return json.Marshal(fields)
}

// DecodeHit converts a Meilisearch hit to a match: the ID and score are taken
// from the arkoren_id and _rankingScore fields, which then are removed with
// the other fields the driver added, leaving the document as it was put.
func DecodeHit(hit map[string]jsontext.Value, scored bool) (search.Match, error) {
	var match search.Match

	if err := json.Unmarshal(hit[idField], &match.ID); err != nil {
		return match, fmt.Errorf("hit without %s: %w", idField, err)
	}

	if scored {
		if err := json.Unmarshal(hit[scoreField], &match.Score); err != nil {
			return match, fmt.Errorf("hit %s without %s: %w", match.ID, scoreField, err)
		}
	}

	delete(hit, scoreField)
	delete(hit, idField)
	delete(hit, orderField)

	var err error

	if match.Source, err = json.Marshal(hit); err != nil {
		return match, fmt.Errorf("hit %s: %w", match.ID, err)
	}

	return match, nil
}
