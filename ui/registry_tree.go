package ui

import (
	"encoding/json"
	"fmt"
)

func findNode(n *Node, key string) *Node {
	if n == nil {
		return nil
	}
	if n.Key == key {
		return n
	}
	if !KnownElement(n.Type) {
		return nil
	}
	for i := range n.Children {
		if c := findNode(&n.Children[i], key); c != nil {
			return c
		}
	}
	return nil
}

func clone(n *Node) *Node {
	if n == nil {
		return nil
	}
	b, _ := json.Marshal(n)
	var out Node
	_ = json.Unmarshal(b, &out)
	out.engine = n.engine
	out.owner = n.owner
	out.seal = n.seal
	for i := range n.Children {
		out.Children[i] = *clone(&n.Children[i])
	}
	return &out
}

func copyMap(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func copyInstance(i Instance) Instance { i.Tree = clone(i.Tree); return i }

func expandDialog(tree *Node, id string, fallback *Node) (*Node, error) {
	refs := 0
	count := 0
	var expand func(*Node, int) (*Node, error)
	expand = func(n *Node, depth int) (*Node, error) {
		if n == nil {
			return nil, nil
		}
		count++
		if count > MaxNodes || depth > MaxDepth {
			return nil, fmt.Errorf("dialog wrapper too large")
		}
		if n.Type == "engine" {
			refs++
			if !n.engine || n.Props["site"] != string(Dialog) || n.Props["id"] != id || len(n.Props) != 2 || len(n.Children) > 0 || len(n.Events) > 0 {
				return nil, fmt.Errorf("invalid dialog reference")
			}
			return clone(fallback), nil
		}
		out := *n
		out.Children = append([]Node(nil), n.Children...)
		for i := range out.Children {
			child, err := expand(&out.Children[i], depth+1)
			if err != nil {
				return nil, err
			}
			out.Children[i] = *child
		}
		return &out, nil
	}
	out, err := expand(tree, 1)
	if err != nil {
		return nil, err
	}
	if refs != 1 {
		return nil, fmt.Errorf("helper dialog must include next exactly once")
	}
	return out, nil
}

func equalTrees(a, b *Node) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func markOwner(n *Node, owner string) {
	if n == nil {
		return
	}
	n.owner = owner
	for i := range n.Children {
		markOwner(&n.Children[i], owner)
	}
}

func prefixKeys(n *Node, owner string) {
	if n == nil || n.Type == "engine" {
		return
	}
	if n.owner == "" {
		n.owner = owner
		if owner != "atto" && n.Key != "" {
			n.Key = owner + "/" + n.Key
		}
	}
	for i := range n.Children {
		prefixKeys(&n.Children[i], owner)
	}
}

func validNextProps(original, next map[string]any, site Site) bool {
	allowed := map[string]bool{}
	for _, key := range overrideFields(site) {
		allowed[key] = true
	}
	a, b := map[string]any{}, map[string]any{}
	for key, value := range original {
		if !allowed[key] {
			a[key] = value
		}
	}
	for key, value := range next {
		if !allowed[key] {
			b[key] = value
		}
	}
	return equalProps(a, b)
}

func sealEngine(n *Node) { b, _ := json.Marshal(n.Props); n.seal = string(b) }

func setEngineOverrides(n *Node, overrides map[string]any) {
	if n == nil {
		return
	}
	if n.Type == "engine" {
		n.Props["overrides"] = overrides
		sealEngine(n)
	}
	for i := range n.Children {
		setEngineOverrides(&n.Children[i], overrides)
	}
}
