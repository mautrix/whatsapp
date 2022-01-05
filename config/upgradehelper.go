// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type YAMLMap map[string]YAMLNode
type YAMLList []YAMLNode

type YAMLNode struct {
	*yaml.Node
	Map  YAMLMap
	List YAMLList
	Key  *yaml.Node
}

type YAMLType uint32

const (
	Null YAMLType = 1 << iota
	Bool
	Str
	Int
	Float
	Timestamp
	List
	Map
	Binary
)

func (t YAMLType) String() string {
	switch t {
	case Null:
		return NullTag
	case Bool:
		return BoolTag
	case Str:
		return StrTag
	case Int:
		return IntTag
	case Float:
		return FloatTag
	case Timestamp:
		return TimestampTag
	case List:
		return SeqTag
	case Map:
		return MapTag
	case Binary:
		return BinaryTag
	default:
		panic(fmt.Errorf("can't convert type %d to string", t))
	}
}

func tagToType(tag string) YAMLType {
	switch tag {
	case NullTag:
		return Null
	case BoolTag:
		return Bool
	case StrTag:
		return Str
	case IntTag:
		return Int
	case FloatTag:
		return Float
	case TimestampTag:
		return Timestamp
	case SeqTag:
		return List
	case MapTag:
		return Map
	case BinaryTag:
		return Binary
	default:
		return 0
	}
}

const (
	NullTag      = "!!null"
	BoolTag      = "!!bool"
	StrTag       = "!!str"
	IntTag       = "!!int"
	FloatTag     = "!!float"
	TimestampTag = "!!timestamp"
	SeqTag       = "!!seq"
	MapTag       = "!!map"
	BinaryTag    = "!!binary"
)

func fromNode(node, key *yaml.Node) YAMLNode {
	switch node.Kind {
	case yaml.DocumentNode:
		return fromNode(node.Content[0], nil)
	case yaml.AliasNode:
		return fromNode(node.Alias, nil)
	case yaml.MappingNode:
		return YAMLNode{
			Node: node,
			Map:  parseYAMLMap(node),
			Key:  key,
		}
	case yaml.SequenceNode:
		return YAMLNode{
			Node: node,
			List: parseYAMLList(node),
		}
	default:
		return YAMLNode{Node: node, Key: key}
	}
}

func (yn *YAMLNode) toNode() *yaml.Node {
	switch {
	case yn.Map != nil && yn.Node.Kind == yaml.MappingNode:
		yn.Content = yn.Map.toNodes()
	case yn.List != nil && yn.Node.Kind == yaml.SequenceNode:
		yn.Content = yn.List.toNodes()
	}
	return yn.Node
}

func parseYAMLList(node *yaml.Node) YAMLList {
	data := make(YAMLList, len(node.Content))
	for i, item := range node.Content {
		data[i] = fromNode(item, nil)
	}
	return data
}

func (yl YAMLList) toNodes() []*yaml.Node {
	nodes := make([]*yaml.Node, len(yl))
	for i, item := range yl {
		nodes[i] = item.toNode()
	}
	return nodes
}

func parseYAMLMap(node *yaml.Node) YAMLMap {
	if len(node.Content)%2 != 0 {
		panic(fmt.Errorf("uneven number of items in YAML map (%d)", len(node.Content)))
	}
	data := make(YAMLMap, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]
		if key.Kind == yaml.ScalarNode {
			data[key.Value] = fromNode(value, key)
		}
	}
	return data
}

func (ym YAMLMap) toNodes() []*yaml.Node {
	nodes := make([]*yaml.Node, len(ym)*2)
	i := 0
	for key, value := range ym {
		nodes[i] = makeStringNode(key)
		nodes[i+1] = value.toNode()
		i += 2
	}
	return nodes
}

func makeStringNode(val string) *yaml.Node {
	var node yaml.Node
	node.SetString(val)
	return &node
}

type UpgradeHelper struct {
	base YAMLNode
	cfg  YAMLNode
}

func NewUpgradeHelper(base, cfg *yaml.Node) *UpgradeHelper {
	return &UpgradeHelper{
		base: fromNode(base, nil),
		cfg:  fromNode(cfg, nil),
	}
}

func (helper *UpgradeHelper) Copy(allowedTypes YAMLType, path ...string) {
	base, cfg := helper.base, helper.cfg
	var ok bool
	for _, item := range path {
		base = base.Map[item]
		cfg, ok = cfg.Map[item]
		if !ok {
			return
		}
	}
	if allowedTypes&tagToType(cfg.Tag) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "Ignoring incorrect config field type %s at %s\n", cfg.Tag, strings.Join(path, "->"))
		return
	}
	base.Tag = cfg.Tag
	base.Style = cfg.Style
	switch base.Kind {
	case yaml.ScalarNode:
		base.Value = cfg.Value
	case yaml.SequenceNode, yaml.MappingNode:
		base.Content = cfg.Content
	}
}

func getNode(cfg YAMLNode, path []string) *YAMLNode {
	var ok bool
	for _, item := range path {
		cfg, ok = cfg.Map[item]
		if !ok {
			return nil
		}
	}
	return &cfg
}

func (helper *UpgradeHelper) GetNode(path ...string) *YAMLNode {
	return getNode(helper.cfg, path)
}

func (helper *UpgradeHelper) GetBaseNode(path ...string) *YAMLNode {
	return getNode(helper.base, path)
}

func (helper *UpgradeHelper) Get(tag YAMLType, path ...string) (string, bool) {
	node := helper.GetNode(path...)
	if node == nil || node.Kind != yaml.ScalarNode || tag&tagToType(node.Tag) == 0 {
		return "", false
	}
	return node.Value, true
}

func (helper *UpgradeHelper) GetBase(path ...string) string {
	return helper.GetBaseNode(path...).Value
}

func (helper *UpgradeHelper) Set(tag YAMLType, value string, path ...string) {
	base := helper.base
	for _, item := range path {
		base = base.Map[item]
	}
	base.Tag = tag.String()
	base.Value = value
}

func (helper *UpgradeHelper) SetMap(value YAMLMap, path ...string) {
	base := helper.base
	for _, item := range path {
		base = base.Map[item]
	}
	if base.Tag != MapTag || base.Kind != yaml.MappingNode {
		panic(fmt.Errorf("invalid target for SetMap(%+v): tag:%s, kind:%d", path, base.Tag, base.Kind))
	}
	base.Content = value.toNodes()
}
