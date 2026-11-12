// Package budget holds the neutral budget vocabulary shared by the flow/config parsers, the engine and
// the API: limits, scope IDs, overrides, spend aggregation and view DTOs. It imports only stdlib and yaml.v3.
package budget

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Budget field names shared by the YAML and JSON decoders; money is "money" in YAML and "money_usd" in JSON.
const (
	keyTokens     = "tokens"
	keyStopPolicy = "stop_policy"
	keyMoneyUSD   = "money_usd"
)

// Resolved YAML scalar tags.
const (
	yamlStr   = "!!str"
	yamlInt   = "!!int"
	yamlFloat = "!!float"
)

const (
	// MaxTokens keeps limits exactly representable in JSON/TS numbers (2^53-1).
	MaxTokens uint64 = 1<<53 - 1
	// MaxMoneyUSD keeps nano-USD arithmetic inside int64.
	MaxMoneyUSD = 1e9
)

// Policy is what happens when a limit is reached.
type Policy string

const (
	PolicyHard   Policy = "hard"
	PolicyMedium Policy = "medium"
	PolicyInfo   Policy = "info"
)

// Valid reports whether p is one of the three policies; the empty value is not valid here (see Spec.Normalize).
func (p Policy) Valid() bool {
	return p == PolicyHard || p == PolicyMedium || p == PolicyInfo
}

// Spec is one budget limit. A zero metric means "not set". It is comparable, so the engine can detect a
// changed base with ==; parsing normalizes an omitted StopPolicy to hard.
type Spec struct {
	Tokens     uint64
	MoneyUSD   float64
	StopPolicy Policy
}

// Normalize returns s with the default policy (hard) filled in.
func (s Spec) Normalize() Spec {
	if s.StopPolicy == "" {
		s.StopPolicy = PolicyHard
	}
	return s
}

// Validate rejects an empty spec, out-of-range or non-finite metrics and an unknown policy.
func (s Spec) Validate() error {
	if s.Tokens == 0 && s.MoneyUSD == 0 {
		return errors.New("budget needs tokens or money")
	}
	if s.Tokens > MaxTokens {
		return fmt.Errorf("budget.tokens: %d exceeds the maximum %d", s.Tokens, MaxTokens)
	}
	if math.IsNaN(s.MoneyUSD) || math.IsInf(s.MoneyUSD, 0) || s.MoneyUSD < 0 || s.MoneyUSD > MaxMoneyUSD {
		return fmt.Errorf("budget.money: %v must be a positive number up to %v", s.MoneyUSD, MaxMoneyUSD)
	}
	if s.Normalize().StopPolicy.Valid() {
		return nil
	}
	return fmt.Errorf("budget.stop_policy: unknown policy %q (want hard, medium or info)", s.StopPolicy)
}

// ParseTokens parses "500k", "1.5m": a decimal with a k (up to 3 fraction digits) or m (up to 6) suffix.
func ParseTokens(s string) (uint64, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("tokens %q: want a number with a k or m suffix, e.g. 500k", s)
	}
	var mult uint64
	var fracDigits int
	switch s[len(s)-1] {
	case 'k', 'K':
		mult, fracDigits = 1_000, 3
	case 'm', 'M':
		mult, fracDigits = 1_000_000, 6
	default:
		return 0, fmt.Errorf("tokens %q: want a number with a k or m suffix, e.g. 500k", s)
	}
	whole, frac, _ := strings.Cut(s[:len(s)-1], ".")
	if !isDigits(whole) || (strings.Contains(s, ".") && !isDigits(frac)) {
		return 0, fmt.Errorf("tokens %q: not a decimal number", s)
	}
	if len(frac) > fracDigits {
		return 0, fmt.Errorf("tokens %q: at most %d digits after the point", s, fracDigits)
	}
	wholeN, err := strconv.ParseUint(whole, 10, 64)
	if err != nil || wholeN > MaxTokens/mult {
		return 0, fmt.Errorf("tokens %q: exceeds the maximum %d", s, MaxTokens)
	}
	var fracN uint64
	if frac != "" {
		padded := frac + strings.Repeat("0", fracDigits-len(frac))
		fracN, _ = strconv.ParseUint(padded, 10, 64) // digits only, at most 6
	}
	n := wholeN*mult + fracN*(mult/pow10(fracDigits))
	if n == 0 {
		return 0, fmt.Errorf("tokens %q: must be positive", s)
	}
	if n > MaxTokens {
		return 0, fmt.Errorf("tokens %q: exceeds the maximum %d", s, MaxTokens)
	}
	return n, nil
}

// ParseMoney parses "10$" or "0.50$": a positive decimal USD amount with a $ suffix.
func ParseMoney(s string) (float64, error) {
	body, ok := strings.CutSuffix(s, "$")
	if !ok {
		return 0, fmt.Errorf("money %q: want a USD amount with a $ suffix, e.g. 10$", s)
	}
	whole, frac, hasPoint := strings.Cut(body, ".")
	if !isDigits(whole) || (hasPoint && !isDigits(frac)) {
		return 0, fmt.Errorf("money %q: not a decimal number", s)
	}
	v, err := strconv.ParseFloat(body, 64)
	if err != nil {
		return 0, fmt.Errorf("money %q: %w", s, err)
	}
	return v, checkMoney(v)
}

func checkMoney(v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > MaxMoneyUSD {
		return fmt.Errorf("money %v: must be a positive number up to %v", v, MaxMoneyUSD)
	}
	return nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func pow10(n int) uint64 {
	p := uint64(1)
	for range n {
		p *= 10
	}
	return p
}

// UnmarshalYAML accepts only a mapping with tokens/money/stop_policy; unknown, duplicate and null keys fail.
func (s *Spec) UnmarshalYAML(value *yaml.Node) error {
	node := resolveAlias(value)
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: budget must be a mapping with tokens, money and stop_policy", node.Line)
	}
	var out Spec
	seen := map[string]bool{}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, val := node.Content[i], resolveAlias(node.Content[i+1])
		var name string // as yaml.v3 decodes it: an alias key is its target, not its anchor name
		if err := key.Decode(&name); err != nil {
			return fmt.Errorf("line %d: budget key: %w", key.Line, err)
		}
		if seen[name] {
			return fmt.Errorf("line %d: budget.%s is duplicated", key.Line, name)
		}
		seen[name] = true
		if val.ShortTag() == "!!null" {
			return fmt.Errorf("line %d: budget.%s must not be null", key.Line, name)
		}
		var err error
		switch name {
		case keyTokens:
			out.Tokens, err = tokensFromYAML(val)
		case "money":
			out.MoneyUSD, err = moneyFromYAML(val)
		case keyStopPolicy:
			out.StopPolicy, err = policyFromYAML(val)
		default:
			err = errors.New("unknown field (want tokens, money or stop_policy)")
		}
		if err != nil {
			return fmt.Errorf("line %d: budget.%s: %w", key.Line, name, err)
		}
	}
	out = out.Normalize()
	if err := out.Validate(); err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*s = out
	return nil
}

func resolveAlias(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

func tokensFromYAML(n *yaml.Node) (uint64, error) {
	switch n.ShortTag() {
	case yamlInt:
		var v int64
		if err := n.Decode(&v); err != nil {
			return 0, fmt.Errorf("not a valid integer: %q", n.Value)
		}
		return checkTokens(v)
	case yamlFloat:
		var f float64
		if err := n.Decode(&f); err != nil || f != math.Trunc(f) || math.Abs(f) > float64(MaxTokens) {
			return 0, fmt.Errorf("want an integer or a k/m string, got %q", n.Value)
		}
		return checkTokens(int64(f))
	case yamlStr:
		return ParseTokens(n.Value)
	}
	return 0, fmt.Errorf("want an integer or a k/m string, got %q", n.Value)
}

func checkTokens(v int64) (uint64, error) {
	if v <= 0 {
		return 0, fmt.Errorf("must be positive, got %d", v)
	}
	if uint64(v) > MaxTokens {
		return 0, fmt.Errorf("%d exceeds the maximum %d", v, MaxTokens)
	}
	return uint64(v), nil
}

func moneyFromYAML(n *yaml.Node) (float64, error) {
	switch n.ShortTag() {
	case yamlInt, yamlFloat:
		var f float64
		if err := n.Decode(&f); err != nil {
			return 0, fmt.Errorf("not a valid number: %q", n.Value)
		}
		return f, checkMoney(f)
	case yamlStr:
		return ParseMoney(n.Value)
	}
	return 0, fmt.Errorf("want a number or an amount with a $ suffix, got %q", n.Value)
}

func policyFromYAML(n *yaml.Node) (Policy, error) {
	p := Policy(n.Value)
	if n.ShortTag() != yamlStr || !p.Valid() {
		return "", fmt.Errorf("unknown policy %q (want hard, medium or info)", n.Value)
	}
	return p, nil
}

type specYAML struct {
	Tokens     uint64  `yaml:"tokens,omitempty"`
	Money      float64 `yaml:"money,omitempty"`
	StopPolicy Policy  `yaml:"stop_policy"`
}

// MarshalYAML writes the canonical form: integer tokens, numeric money and an explicit policy.
func (s Spec) MarshalYAML() (any, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	n := s.Normalize()
	return specYAML{Tokens: n.Tokens, Money: n.MoneyUSD, StopPolicy: n.StopPolicy}, nil
}

type specJSON struct {
	Tokens     uint64  `json:"tokens,omitempty"`
	MoneyUSD   float64 `json:"money_usd,omitempty"`
	StopPolicy Policy  `json:"stop_policy"`
}

// MarshalJSON writes {"tokens":N,"money_usd":X,"stop_policy":"hard"}; unset metrics are omitted.
func (s Spec) MarshalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	n := s.Normalize()
	return json.Marshal(specJSON(n))
}

// UnmarshalJSON checks every supplied key itself, so an explicit zero or empty value is an error
// instead of looking like an omitted one. Keys must be exact; unknown, repeated and null keys fail.
func (s *Spec) UnmarshalJSON(data []byte) error {
	obj, err := decodeObject(data, keyTokens, keyMoneyUSD, keyStopPolicy)
	if err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	var out Spec
	for key, raw := range obj {
		switch key {
		case keyTokens:
			var n uint64
			if err = json.Unmarshal(raw, &n); err == nil && (n == 0 || n > MaxTokens) {
				err = fmt.Errorf("%d is outside 1..%d", n, MaxTokens)
			}
			out.Tokens = n
		case keyMoneyUSD:
			var f float64
			if err = json.Unmarshal(raw, &f); err == nil {
				err = checkMoney(f)
			}
			out.MoneyUSD = f
		case keyStopPolicy:
			var p Policy
			if err = json.Unmarshal(raw, &p); err == nil && !p.Valid() {
				err = fmt.Errorf("unknown policy %q (want hard, medium or info)", p)
			}
			out.StopPolicy = p
		default:
			err = errors.New("unknown field (want tokens, money_usd or stop_policy)")
		}
		if err != nil {
			return fmt.Errorf("budget.%s: %w", key, err)
		}
	}
	out = out.Normalize()
	if err := out.Validate(); err != nil {
		return err
	}
	*s = out
	return nil
}
