package tool

import (
	"bytes"
	"encoding"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"
)

// node 同时用于生成 Schema 和校验，避免两份规则漂移。
type node struct {
	typ              reflect.Type
	kind             string
	fields           map[string]*node
	order            []string
	required         bool
	description      string
	enum             []string
	min, max         *big.Rat
	minJSON, maxJSON json.Number
	item             *node
}

var unmarshaler = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
var textUnmarshaler = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()

func describe(t reflect.Type, active map[reflect.Type]bool) (*node, error) {
	if active[t] {
		return nil, fmt.Errorf("recursive type %s unsupported", t)
	}
	if t.Implements(unmarshaler) || reflect.PointerTo(t).Implements(unmarshaler) || t.Implements(textUnmarshaler) || reflect.PointerTo(t).Implements(textUnmarshaler) {
		return nil, fmt.Errorf("custom decoding on %s unsupported", t)
	}
	active[t] = true
	defer delete(active, t)
	n := &node{typ: t}
	switch t.Kind() {
	case reflect.String:
		n.kind = "string"
	case reflect.Bool:
		n.kind = "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n.kind = "integer"
	case reflect.Float32, reflect.Float64:
		n.kind = "number"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil, fmt.Errorf("byte slices unsupported")
		}
		n.kind = "array"
		var err error
		n.item, err = describe(t.Elem(), active)
		if err != nil {
			return nil, err
		}
	case reflect.Struct:
		n.kind = "object"
		n.fields = map[string]*node{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			if f.PkgPath != "" {
				return nil, fmt.Errorf("field %s must be exported or ignored", f.Name)
			}
			if f.Anonymous {
				return nil, fmt.Errorf("embedded fields unsupported: %s", f.Name)
			}
			parts := strings.Split(tag, ",")
			name := parts[0]
			if name == "" {
				return nil, fmt.Errorf("field %s requires explicit json name", f.Name)
			}
			for _, opt := range parts[1:] {
				if opt != "omitempty" {
					return nil, fmt.Errorf("unsupported json option %q", opt)
				}
			}
			if _, ok := n.fields[name]; ok {
				return nil, fmt.Errorf("duplicate json field %q", name)
			}
			child, err := describe(f.Type, active)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			child.description = f.Tag.Get("description")
			if err := child.constraints(f.Tag.Get("tool")); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			n.fields[name] = child
			n.order = append(n.order, name)
		}
	default:
		return nil, fmt.Errorf("unsupported parameter type %s", t)
	}
	return n, nil
}
func (n *node) constraints(tag string) error {
	if tag == "" {
		return nil
	}
	seen := map[string]bool{}
	for _, rule := range strings.Split(tag, ",") {
		k, v, has := strings.Cut(rule, "=")
		if seen[k] {
			return fmt.Errorf("duplicate constraint %q", k)
		}
		seen[k] = true
		switch k {
		case "required":
			if has {
				return fmt.Errorf("required has no value")
			}
			n.required = true
		case "enum":
			if !has || n.kind != "string" || v == "" {
				return fmt.Errorf("enum requires string alternatives")
			}
			n.enum = strings.Split(v, "|")
			values := map[string]bool{}
			for _, s := range n.enum {
				if values[s] {
					return fmt.Errorf("duplicate enum")
				}
				values[s] = true
			}
		case "min", "max":
			if !has || (n.kind != "integer" && n.kind != "number") || !json.Valid([]byte(v)) {
				return fmt.Errorf("%s requires JSON number", k)
			}
			if !boundedNumber(v) {
				return fmt.Errorf("numeric bound exceeds supported limits")
			}
			r, ok := new(big.Rat).SetString(v)
			if !ok || len(v) > 64 {
				return fmt.Errorf("invalid numeric bound")
			}
			if k == "min" {
				n.min = r
				n.minJSON = json.Number(v)
			} else {
				n.max = r
				n.maxJSON = json.Number(v)
			}
		default:
			return fmt.Errorf("unknown tool constraint %q", k)
		}
	}
	if n.min != nil && n.max != nil && n.min.Cmp(n.max) > 0 {
		return fmt.Errorf("min exceeds max")
	}
	return nil
}
func (n *node) schema() map[string]any {
	s := map[string]any{"type": n.kind}
	if n.description != "" {
		s["description"] = n.description
	}
	if n.enum != nil {
		s["enum"] = n.enum
	}
	if n.min != nil {
		s["minimum"] = n.minJSON
	}
	if n.max != nil {
		s["maximum"] = n.maxJSON
	}
	if n.kind == "array" {
		s["items"] = n.item.schema()
	}
	if n.kind == "object" {
		props := map[string]any{}
		var required []string
		for _, k := range n.order {
			c := n.fields[k]
			props[k] = c.schema()
			if c.required {
				required = append(required, k)
			}
		}
		s["properties"] = props
		s["additionalProperties"] = false
		if len(required) > 0 {
			s["required"] = required
		}
	}
	return s
}
func (n *node) validate(v any, path string) error {
	if v == nil {
		return fmt.Errorf("%s: null is not allowed", path)
	}
	switch n.kind {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object", path)
		}
		for k := range m {
			if _, ok := n.fields[k]; !ok {
				return fmt.Errorf("%s: unknown field %q", path, k)
			}
		}
		for _, k := range n.order {
			c := n.fields[k]
			x, exists := m[k]
			if !exists {
				if c.required {
					return fmt.Errorf("%s.%s: required field missing", path, k)
				}
				continue
			}
			if err := c.validate(x, path+"."+k); err != nil {
				return err
			}
		}
	case "array":
		xs, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array", path)
		}
		for i, x := range xs {
			if err := n.item.validate(x, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s: expected string", path)
		}
		if len(n.enum) > 0 {
			found := false
			for _, e := range n.enum {
				found = found || s == e
			}
			if !found {
				return fmt.Errorf("%s: value outside enum", path)
			}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: expected boolean", path)
		}
	default:
		number, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("%s: expected number", path)
		}
		if !boundedNumber(string(number)) {
			return fmt.Errorf("%s: number exceeds supported limits", path)
		}
		// encoding/json 检查目标 Go 数值类型的溢出与整数词法，避免 float64 中转。
		if err := json.Unmarshal([]byte(number), reflect.New(n.typ).Interface()); err != nil {
			return fmt.Errorf("%s: number cannot fit %s", path, n.typ)
		}
		if n.min != nil || n.max != nil {
			r, ok := new(big.Rat).SetString(string(number))
			if !ok {
				return fmt.Errorf("%s: invalid number", path)
			}
			if n.min != nil && r.Cmp(n.min) < 0 || n.max != nil && r.Cmp(n.max) > 0 {
				return fmt.Errorf("%s: number outside bounds", path)
			}
		}
	}
	return nil
}

// parse 严格拒绝重复键、尾随文档、null（后续节点验证）、过大输入和过深嵌套。
func parse(raw []byte) (any, error) {
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("arguments exceed 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := readValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON data")
	}
	return v, nil
}
func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("arguments exceed nesting limit")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	switch delim {
	case '{':
		m := map[string]any{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("invalid object key")
			}
			if _, ok := m[key]; ok {
				return nil, fmt.Errorf("duplicate field %q", key)
			}
			v, err := readValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("invalid object end")
		}
		return m, nil
	case '[':
		a := []any{}
		for d.More() {
			v, err := readValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("invalid array end")
		}
		return a, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter")
	}
}

// 限制数值文本，避免极端指数使精确有理数比较分配过量内存。
func boundedNumber(s string) bool {
	if len(s) > 128 {
		return false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		return err == nil && e >= -1000 && e <= 1000
	}
	return true
}
