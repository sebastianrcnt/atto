package ui

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	MaxNodes     = 2048
	MaxDepth     = 32
	MaxBytes     = 256 << 10
	MaxText      = 128 << 10
	MaxSites     = 256
	MaxLiveBytes = 4 << 20
)

var localID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func ValidID(id string) bool {
	parts := strings.Split(id, "/")
	if len(parts) != 2 {
		return false
	}
	return localID.MatchString(parts[0]) && localID.MatchString(parts[1]) && len(id) <= 128
}

var themes = map[string]bool{"text": true, "muted": true, "accent": true, "success": true, "warning": true, "error": true, "border": true, "surface": true, "diffAdd": true, "diffRemove": true, "diffHunk": true}

// schema mini-language: s=string, b=bool, n=finite number, i=integer;
// ! marks required, numeric ranges and enum values follow a colon.
var schemas = map[string]string{
	"Box":      "flexDirection:s:column,row gap:i:0,16 padding:i:0,16 width:w height:i:1,256 grow:n:0,1000000 align:s:start,center,end borderStyle:s:none,single,round,double,ascii",
	"Text":     "text:s bold:b italic:b underline:b wrap:s:wrap,truncate maxLines:i:1,256",
	"Markdown": "text!:s maxLines:i:1,256",
	"Code":     "source!:s language:s path:s startLine:i:1,9007199254740991 lineNumbers:b wrap:s:wrap,truncate",
	"Diff":     "source!:s path:s lineNumbers:b wrap:s:wrap,truncate",
	"Link":     "href!:s label:s",
	"Button":   "label!:s plain:b hotkey:s disabled:b autoFocus:b",
	"Input":    "label:s value:s placeholder:s submitLabel:s maxLength:i:0,131072 disabled:b autoFocus:b",
	"Select":   "options!:a label:s value:s disabled:b autoFocus:b",
	"List":     "mode:s:list,table rows!:a columns:a emptyText:s",
	"Progress": "value:n:0,1 label:s width:i:1,512",
	"Collapse": "title!:s defaultOpen:b previewLines:i:0,256",
	"Image":    "resource!:s alt!:s columns:i:1,512 rows:i:1,256 fit:s:contain,cover",
}

func SafeURL(s string) bool {
	if len(s) > 2048 || !utf8.ValidString(s) || strings.ContainsAny(s, "\\ \t\r\n") {
		return false
	}
	for _, r := range s {
		if r < 32 || r >= 127 && r <= 159 {
			return false
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func validateProps(p map[string]any, spec string) error {
	allowed := map[string]string{"color": "s", "backgroundColor": "s"}
	for field := range strings.FieldsSeq(spec) {
		a, b, _ := strings.Cut(field, ":")
		required := strings.HasSuffix(a, "!")
		a = strings.TrimSuffix(a, "!")
		allowed[a] = b
		if _, ok := p[a]; required && !ok {
			return fmt.Errorf("missing %s", a)
		}
	}
	for k, v := range p {
		rule, ok := allowed[k]
		if !ok {
			return fmt.Errorf("unknown prop %s", k)
		}
		typ, rng, _ := strings.Cut(rule, ":")
		valid := false
		switch typ {
		case "s":
			_, valid = v.(string)
			if valid && rng != "" {
				valid = false
				for opt := range strings.SplitSeq(rng, ",") {
					if opt == v {
						valid = true
					}
				}
			}
		case "b":
			_, valid = v.(bool)
		case "a":
			_, valid = v.([]any)
		case "w":
			if v == "fill" {
				valid = true
			} else if f, ok := v.(float64); ok {
				valid = f >= 1 && f <= 512 && f == math.Trunc(f)
			}
		case "i", "n":
			if f, ok := v.(float64); ok {
				valid = !math.IsNaN(f) && !math.IsInf(f, 0) && (typ != "i" || f == math.Trunc(f))
				if rng != "" {
					var lo, hi float64
					_, _ = fmt.Sscanf(rng, "%f,%f", &lo, &hi)
					valid = valid && f >= lo && f <= hi
				}
			}
		}
		if !valid {
			return fmt.Errorf("invalid prop %s", k)
		}
		if k == "color" || k == "backgroundColor" {
			if !themes[v.(string)] {
				return fmt.Errorf("unknown theme %v", v)
			}
		}
	}
	return nil
}

// Validate checks untrusted wire nodes before layout. Engine references must be
// minted by Registry; use ValidateDisplay for replayed, already-persisted refs.
func Validate(site Site, tree Node) error { return validate(site, tree, false, "", nil) }
func ValidateDisplay(site Site, id string, tree Node) error {
	return validate(site, tree, true, id, nil)
}
func validate(site Site, tree Node, replay bool, id string, engineNode *Node) error {
	if !ValidSite(site) {
		return fmt.Errorf("unknown site %q", site)
	}
	// Count first so oversized/cyclic child slices never reach JSON recursion.
	count := 0
	propText, propBytes := 0, 0
	keys := map[string]bool{}
	hotkeys := map[string]bool{}
	refs := 0
	var walk func(Node, int) error
	walk = func(n Node, depth int) error {
		count++
		if err := preflightProps(reflect.ValueOf(n.Props), 0, &propText, &propBytes); err != nil {
			return err
		}
		if count > MaxNodes || depth > MaxDepth {
			return fmt.Errorf("tree exceeds node/depth limit")
		}
		if n.Type == "" || len(n.Type) > 128 {
			return fmt.Errorf("invalid type")
		}
		if n.Key != "" {
			if n.Key == "$site" || len(n.Key) > 128 || !utf8.ValidString(n.Key) || strings.ContainsAny(n.Key, "\x00\x1b") || keys[n.Key] {
				return fmt.Errorf("invalid/duplicate key %q", n.Key)
			}
			keys[n.Key] = true
		}
		if n.Type == "engine" {
			if len(n.Props) != 3 {
				return fmt.Errorf("invalid engine props")
			}
			over, ok := n.Props["overrides"].(map[string]any)
			if !ok {
				return fmt.Errorf("invalid overrides")
			}
			for k, v := range over {
				valid := false
				for _, field := range overrideFields(site) {
					if field == k {
						valid = true
					}
				}
				if _, ok := v.(string); !ok || !valid {
					return fmt.Errorf("illegal display override")
				}
			}
			refs++
			if !IsItem(site) || refs > 1 || len(n.Children) > 0 || len(n.Events) > 0 {
				return fmt.Errorf("illegal engine reference")
			}
			b, _ := json.Marshal(n.Props)
			if !replay && (!n.engine || n.seal != string(b)) {
				return fmt.Errorf("unminted engine reference")
			}
			if n.Props["site"] != string(site) || id != "" && n.Props["id"] != id {
				return fmt.Errorf("cross-site engine reference")
			}
			if engineNode != nil && !equalProps(n.Props, engineNode.Props) {
				return fmt.Errorf("modified engine reference")
			}
			return nil
		}
		spec, known := schemas[n.Type]
		if known {
			// Normalize typed maps/slices/numbers to wire JSON types, bounded globally below.
			b, err := json.Marshal(n.Props)
			if err != nil || len(b) > MaxBytes {
				return fmt.Errorf("invalid/oversized props")
			}
			var p map[string]any
			if err = json.Unmarshal(b, &p); err != nil {
				return err
			}
			if err = validateProps(p, spec); err != nil {
				return fmt.Errorf("%s: %w", n.Type, err)
			}
			control := n.Type == "Button" || n.Type == "Input" || n.Type == "Select"
			if control || n.Type == "Collapse" {
				if n.Key == "" {
					return fmt.Errorf("%s requires key", n.Type)
				}
			}
			if (site == Status || site == Toast) && (control || n.Type == "Collapse") {
				return fmt.Errorf("passive site contains control")
			}
			required := EventType("")
			allowed := map[EventType]bool{}
			switch n.Type {
			case "Button":
				required = Press
				allowed[Press] = true
			case "Input":
				required = Submit
				allowed[Submit] = true
				allowed[InputEvent] = true
			case "Select":
				required = SelectEvent
				allowed[SelectEvent] = true
			}
			seen := map[EventType]bool{}
			for _, e := range n.Events {
				if !allowed[e] || seen[e] {
					return fmt.Errorf("illegal/duplicate event")
				}
				seen[e] = true
			}
			if required != "" && !seen[required] {
				return fmt.Errorf("missing %s event", required)
			}
			if h, ok := p["hotkey"].(string); ok {
				if len(h) != 1 || !strings.Contains("abcdefghijklmnopqrstuvwxyz0123456789", h) || hotkeys[h] {
					return fmt.Errorf("invalid/duplicate hotkey")
				}
				hotkeys[h] = true
			}
			if n.Type == "Link" && !SafeURL(p["href"].(string)) {
				return fmt.Errorf("unsafe link")
			}
			if n.Type == "Markdown" {
				for _, m := range markdownLinks.FindAllStringSubmatch(p["text"].(string), -1) {
					target := m[1]
					if target == "" {
						target = m[2]
					}
					if !SafeURL(target) {
						return fmt.Errorf("unsafe Markdown link")
					}
				}
			}
			if n.Type == "Image" {
				r := p["resource"].(string)
				if !localID.MatchString(r) {
					return fmt.Errorf("invalid image resource")
				}
			}
			if n.Type == "Select" {
				opts := p["options"].([]any)
				seenValues := map[string]bool{}
				enabled := 0
				for _, o := range opts {
					op, ok := o.(map[string]any)
					if !ok {
						return fmt.Errorf("invalid option")
					}
					if err := validatePropsNested(op, "value!:s label!:s description:s disabled:b"); err != nil {
						return err
					}
					v := op["value"].(string)
					if seenValues[v] {
						return fmt.Errorf("duplicate option")
					}
					seenValues[v] = true
					if op["disabled"] != true {
						enabled++
					}
				}
				if enabled == 0 {
					return fmt.Errorf("no enabled options")
				}
				if v, ok := p["value"]; ok && !seenValues[v.(string)] {
					return fmt.Errorf("unknown selected value")
				}
			}
			if n.Type == "List" {
				columns, _ := p["columns"].([]any)
				nc := 1
				if p["mode"] == "table" {
					nc = len(columns)
					if nc == 0 {
						return fmt.Errorf("table requires columns")
					}
				}
				for _, c := range columns {
					cp, ok := c.(map[string]any)
					if !ok {
						return fmt.Errorf("invalid column")
					}
					if err := validatePropsNested(cp, "label!:s width:i:1,512 align:s:start,end"); err != nil {
						return err
					}
				}
				seenRows := map[string]bool{}
				for _, r := range p["rows"].([]any) {
					rp, ok := r.(map[string]any)
					if !ok {
						return fmt.Errorf("invalid row")
					}
					if err := validatePropsNested(rp, "key!:s cells!:a"); err != nil {
						return err
					}
					key := rp["key"].(string)
					cells := rp["cells"].([]any)
					if key == "" || len(key) > 128 || seenRows[key] || len(cells) != nc {
						return fmt.Errorf("invalid row key/cells")
					}
					seenRows[key] = true
					for _, c := range cells {
						if _, ok := c.(string); !ok {
							return fmt.Errorf("invalid cell")
						}
					}
				}
			}
			if n.Type != "Box" && n.Type != "Collapse" && n.Type != "Text" && len(n.Children) > 0 {
				return fmt.Errorf("leaf has children")
			}
		}
		for _, c := range n.Children {
			if known && n.Type == "Text" && c.Type != "Text" {
				return fmt.Errorf("non-Text span")
			}
			if err := walk(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(tree, 1); err != nil {
		return err
	}
	b, err := json.Marshal(tree)
	if err != nil {
		return err
	}
	if len(b) > MaxBytes {
		return fmt.Errorf("serialized tree exceeds 256 KiB")
	}
	var wire any
	_ = json.Unmarshal(b, &wire)
	textBytes := 0
	var stringsWalk func(any)
	stringsWalk = func(v any) {
		switch x := v.(type) {
		case string:
			textBytes += len(x)
			if !utf8.ValidString(x) {
				textBytes = MaxText + 1
			}
		case map[string]any:
			for _, v := range x {
				stringsWalk(v)
			}
		case []any:
			for _, v := range x {
				stringsWalk(v)
			}
		}
	}
	stringsWalk(wire)
	if textBytes > MaxText {
		return fmt.Errorf("text exceeds 128 KiB")
	}
	return nil
}

var markdownLinks = regexp.MustCompile(`\]\(([^)]*)\)|<(https?://[^>]+)>`)

func validatePropsNested(p map[string]any, spec string) error {
	if _, ok := p["color"]; ok {
		return fmt.Errorf("nested color not allowed")
	}
	if _, ok := p["backgroundColor"]; ok {
		return fmt.Errorf("nested background not allowed")
	}
	return validateProps(p, spec)
}
func equalProps(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Preflight bounds text/JSON growth before allocating a serialized props object.
// Wire props are JSON data, never custom marshalers or executable values.
func preflightProps(v reflect.Value, depth int, textBytes, jsonBytes *int) error {
	if depth > MaxDepth {
		return fmt.Errorf("props exceed depth limit")
	}
	if !v.IsValid() {
		return nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		return preflightProps(v.Elem(), depth, textBytes, jsonBytes)
	}
	if v.CanInterface() {
		if _, executable := reflect.TypeAssert[json.Marshaler](v); executable {
			return fmt.Errorf("custom prop marshalers are forbidden")
		}
	}
	*jsonBytes += 2
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		if !utf8.ValidString(s) {
			return fmt.Errorf("invalid UTF-8 prop")
		}
		*textBytes += len(s)
		*jsonBytes += len(s)
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("non-string prop map key")
		}
		iter := v.MapRange()
		for iter.Next() {
			*jsonBytes += len(iter.Key().String()) + 3
			if err := preflightProps(iter.Value(), depth+1, textBytes, jsonBytes); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := preflightProps(v.Index(i), depth+1, textBytes, jsonBytes); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("props must contain only JSON data")
	}
	if *textBytes > MaxText || *jsonBytes > MaxBytes {
		return fmt.Errorf("props exceed text/serialized budget")
	}
	return nil
}
