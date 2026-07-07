package util

import (
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// exists is a placeholder type to represent the existence of a string in the set.
type exists = struct{}

// StringSet is a set of strings represented as a map with empty struct values.
type StringSet map[string]exists

func ParseStringSet(tfSet types.Set) (*StringSet, diag.Diagnostics) {
	if tfSet.IsNull() || tfSet.IsUnknown() {
		return &StringSet{}, nil
	}

	goSet := make(StringSet, tfSet.Length(basetypes.CollectionLengthOptions{}))
	diags := diag.Diagnostics{}

	for _, el := range tfSet.Elements() {
		// Type should be checked by the framework
		sv, ok := el.(basetypes.StringValue)
		if !ok {
			diags.AddError("Invalid set element type", "Expected a string value in the set, but got a different type.")
			continue
		}
		if sv.IsNull() || sv.IsUnknown() {
			continue
		}
		goSet[sv.ValueString()] = exists{}
	}

	return &goSet, nil
}

func (current *StringSet) Diff(expected *StringSet) (*StringSet, *StringSet) {
	missing := make(StringSet, 0)
	extra := make(StringSet, 0)

	for role := range *expected {
		if _, ok := (*current)[role]; !ok {
			missing[role] = exists{}
		}
	}

	for role := range *current {
		if _, ok := (*expected)[role]; !ok {
			extra[role] = exists{}
		}
	}

	return &missing, &extra
}

func (current *StringSet) Union(expected *StringSet) *StringSet {
	union := make(StringSet, len(*current))
	for role := range *current {
		union[role] = exists{}
	}
	for role := range *expected {
		union[role] = exists{}
	}
	return &union
}

func (s *StringSet) ToSetValue() (basetypes.SetValue, diag.Diagnostics) {
	elements := make([]attr.Value, len(*s))
	i := 0
	for str := range *s {
		elements[i] = basetypes.NewStringValue(str)
		i++
	}
	return basetypes.NewSetValue(types.StringType, elements)
}

func (s *StringSet) ToList() []string {
	keys := make([]string, len(*s))
	i := 0
	for str := range *s {
		keys[i] = str
		i++
	}
	return keys
}

func (s *StringSet) Add(key string) {
	(*s)[key] = exists{}
}
