package budget

import "fmt"

// OverrideKind is the runtime override state of one scope.
type OverrideKind string

const (
	OverrideNone      OverrideKind = "none"
	OverrideLimit     OverrideKind = "limit"
	OverrideUnlimited OverrideKind = "unlimited"
)

// Override replaces the declared budget of one scope as a whole. "unlimited" removes the scope's own
// limit even when the base comes from a profile or YAML.
type Override struct {
	Kind OverrideKind `json:"kind"`
	Spec Spec         `json:"spec,omitzero"`
}

// Validate checks that Spec is present exactly when Kind is limit.
func (o Override) Validate() error {
	switch o.Kind {
	case "", OverrideNone, OverrideUnlimited: // the zero value means no override
		if o.Spec != (Spec{}) {
			return fmt.Errorf("override %q must not carry a spec", o.Kind)
		}
		return nil
	case OverrideLimit:
		return o.Spec.Validate()
	}
	return fmt.Errorf("unknown override kind %q (want none, limit or unlimited)", o.Kind)
}

// UnmarshalJSON decodes strictly (exact keys, none repeated or null) and validates the kind/spec pair.
func (o *Override) UnmarshalJSON(data []byte) error {
	if _, err := decodeObject(data, "kind", "spec"); err != nil {
		return fmt.Errorf("override: %w", err)
	}
	type plain Override
	var in plain
	if err := strictUnmarshal(data, &in); err != nil {
		return fmt.Errorf("override: %w", err)
	}
	out := Override(in)
	if out.Kind == OverrideLimit {
		out.Spec = out.Spec.Normalize()
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*o = out
	return nil
}

// Source says where a scope's base budget comes from.
type Source string

const (
	SourceDeclaration Source = "declaration"
	SourceProfile     Source = "profile"
	SourceRequest     Source = "request"
	SourceNone        Source = "none"
)

// ResolveBase picks the base budget: an explicit declaration replaces the profile default as a whole.
// The engine relabels SourceDeclaration to SourceRequest for budgets that came from a creation request.
func ResolveBase(explicit, profileDefault *Spec) (*Spec, Source) {
	switch {
	case explicit != nil:
		c := explicit.Normalize()
		return &c, SourceDeclaration
	case profileDefault != nil:
		c := profileDefault.Normalize()
		return &c, SourceProfile
	}
	return nil, SourceNone
}

// Effective applies an override to the base; nil means the scope has no limit of its own.
func Effective(base *Spec, o Override) *Spec {
	switch o.Kind {
	case OverrideLimit:
		c := o.Spec.Normalize()
		return &c
	case OverrideUnlimited:
		return nil
	}
	if base == nil {
		return nil
	}
	c := base.Normalize()
	return &c
}
