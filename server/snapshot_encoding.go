package server

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
)

// MarshalJSONTo writes a ThreadInfo shaped as a snapshot (Paged) with
// items/hasMore/before always present, including an empty/false tail; other
// ThreadInfo values (summaries) omit them.
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
