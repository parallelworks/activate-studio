package jsonv

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseYAML reads YAML as the npm yaml package's parse() does with its
// defaults (YAML 1.2, core schema): objects as *Object in document order,
// `on`, `yes` and dates as strings, aliases resolved, `<<` an ordinary key,
// a repeated key an error. An empty document is nil.
func ParseYAML(text string) (any, error) {
	dec := yaml.NewDecoder(strings.NewReader(text))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, errEOF(err)) {
			return nil, nil
		}
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("Source contains multiple documents; please use YAML.parseAllDocuments()")
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	return fromNode(doc.Content[0], 0)
}

func errEOF(err error) error {
	if err != nil && err.Error() == "EOF" {
		return err
	}
	return errors.New("not EOF")
}

var (
	reNull  = regexp.MustCompile(`^(?:~|[Nn]ull|NULL)?$`)
	reTrue  = regexp.MustCompile(`^(?:[Tt]rue|TRUE)$`)
	reFalse = regexp.MustCompile(`^(?:[Ff]alse|FALSE)$`)
	reInt   = regexp.MustCompile(`^[-+]?[0-9]+$`)
	reOct   = regexp.MustCompile(`^0o[0-7]+$`)
	reHex   = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	reFloat = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?$`)
	reInf   = regexp.MustCompile(`^[-+]?\.(?:inf|Inf|INF)$`)
	reNaN   = regexp.MustCompile(`^\.(?:nan|NaN|NAN)$`)
)

func scalar(n *yaml.Node) (any, error) {
	tag := n.Tag
	plain := n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) == 0
	explicit := n.Style&yaml.TaggedStyle != 0
	if !plain && !explicit {
		return n.Value, nil
	}
	if explicit {
		switch tag {
		case "!!str":
			return n.Value, nil
		case "!!null":
			return nil, nil
		case "!!bool":
			return reTrue.MatchString(n.Value), nil
		case "!!int", "!!float":
			return core(n.Value), nil
		}
		// An unknown tag keeps the value as a string, as yaml does with a warning.
		return n.Value, nil
	}
	return core(n.Value), nil
}

// core resolves a plain scalar with the YAML 1.2 core schema.
func core(v string) any {
	switch {
	case reNull.MatchString(v):
		return nil
	case reTrue.MatchString(v):
		return true
	case reFalse.MatchString(v):
		return false
	case reInt.MatchString(v):
		f, _ := strconv.ParseFloat(v, 64)
		return f
	case reOct.MatchString(v):
		u, _ := strconv.ParseUint(v[2:], 8, 64)
		return float64(u)
	case reHex.MatchString(v):
		u, _ := strconv.ParseUint(v[2:], 16, 64)
		return float64(u)
	case reFloat.MatchString(v):
		f, _ := strconv.ParseFloat(v, 64)
		return f
	case reInf.MatchString(v):
		if strings.HasPrefix(v, "-") {
			return math.Inf(-1)
		}
		return math.Inf(1)
	case reNaN.MatchString(v):
		return math.NaN()
	}
	return v
}

func fromNode(n *yaml.Node, depth int) (any, error) {
	if depth > 1000 {
		return nil, errors.New("YAML nesting is too deep")
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return fromNode(n.Content[0], depth+1)
	case yaml.AliasNode:
		return fromNode(n.Alias, depth+1)
	case yaml.ScalarNode:
		return scalar(n)
	case yaml.SequenceNode:
		out := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := fromNode(c, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		o := NewObject()
		for i := 0; i+1 < len(n.Content); i += 2 {
			kv, err := fromNode(n.Content[i], depth+1)
			if err != nil {
				return nil, err
			}
			key := keyString(kv)
			if o.Has(key) {
				return nil, fmt.Errorf("Map keys must be unique at line %d, column %d", n.Content[i].Line, n.Content[i].Column)
			}
			v, err := fromNode(n.Content[i+1], depth+1)
			if err != nil {
				return nil, err
			}
			o.Set(key, v)
		}
		return o, nil
	}
	return nil, fmt.Errorf("unexpected YAML node kind %d", n.Kind)
}

// keyString is the key a JavaScript object gets for a YAML key.
func keyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case *Object, []any:
		b, _ := Marshal(x)
		return string(b)
	}
	return String(v)
}
