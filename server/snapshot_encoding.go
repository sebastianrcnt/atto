package server

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
)

// MarshalJSONTo keeps revision-2 DTOs byte-shape compatible, while revision-3
// snapshots always include items/hasMore/before, including an empty/false tail.
func (info ThreadInfo) MarshalJSONTo(enc *jsontext.Encoder) error {
	type plain ThreadInfo
	if !info.Paged {
		return jsonv2.MarshalEncode(enc, plain(info), json.DefaultOptionsV1())
	}
	items := info.Items
	if items == nil {
		items = []Item{}
	}
	return jsonv2.MarshalEncode(enc, struct {
		plain
		Items   []Item `json:"items"`
		HasMore bool   `json:"hasMore"`
		Before  string `json:"before"`
	}{plain(info), items, info.HasMore, info.Before}, json.DefaultOptionsV1())
}
