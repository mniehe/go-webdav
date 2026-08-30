package carddav

import (
	"errors"
	"net/http"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/mniehe/davkit/internal"
)

// validateFilter enforces the grammar rules the XML shape alone cannot: an
// is-not-defined test asks about absence, so pairing it with conditions on the
// value is contradictory (RFC 6352 §10.5.1, §10.5.2).
func validateFilter(filter *filterReq) error {
	for i := range filter.Props {
		prop := &filter.Props[i]
		if prop.IsNotDefined != nil && (len(prop.TextMatches) > 0 || len(prop.Params) > 0) {
			return internal.HTTPErrorf(http.StatusBadRequest, "carddav: prop-filter with is-not-defined cannot also carry text-match or param-filter")
		}
		for j := range prop.Params {
			param := &prop.Params[j]
			if param.IsNotDefined != nil && param.TextMatch != nil {
				return internal.HTTPErrorf(http.StatusBadRequest, "carddav: param-filter with is-not-defined cannot also carry text-match")
			}
		}
	}
	return nil
}

// matchFilter reports whether a card satisfies the query's filter. The test
// attribute decides whether any or all prop-filters have to hold; the default
// is anyof (RFC 6352 §10.5).
func matchFilter(filter *filterReq, card vcard.Card) (bool, error) {
	allOf := filter.Test == filterAllOf
	for i := range filter.Props {
		ok, err := matchPropFilter(&filter.Props[i], card)
		if err != nil {
			return false, err
		}
		if ok && !allOf {
			return true, nil
		}
		if !ok && allOf {
			return false, nil
		}
	}
	return allOf, nil
}

// candidateFields returns the properties a prop-filter name selects. RFC 6352
// §10.5.1: a name without a group prefix matches the property whatever group it
// carries, while a grouped name such as X-ABC.TEL matches only that group.
// go-vcard keys the card by the bare property name and holds the group on the
// field, so an ungrouped name needs no special handling and a grouped one has
// to be split before the lookup.
func candidateFields(card vcard.Card, filterName string) []*vcard.Field {
	group, name := "", filterName
	if i := strings.IndexByte(filterName, '.'); i >= 0 {
		group, name = filterName[:i], filterName[i+1:]
	}

	fields := card[strings.ToUpper(name)]
	if group == "" {
		return fields
	}
	grouped := make([]*vcard.Field, 0, len(fields))
	for _, field := range fields {
		if strings.EqualFold(field.Group, group) {
			grouped = append(grouped, field)
		}
	}
	return grouped
}

func matchPropFilter(prop *propFilterReq, card vcard.Card) (bool, error) {
	fields := candidateFields(card, prop.Name)
	if len(fields) == 0 {
		return prop.IsNotDefined != nil, nil
	}
	if prop.IsNotDefined != nil {
		return false, nil
	}

	// A property may occur several times. One occurrence has to satisfy every
	// condition: gathering them from different occurrences would match a card
	// that no single property justifies.
	for _, field := range fields {
		ok, err := matchPropField(prop, field)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// matchPropField reports whether one occurrence of the named property satisfies
// the filter. RFC 6352 §10.5.1: an empty prop-filter is satisfied by the
// property existing, and otherwise the test attribute decides whether any or
// all of the text-match and param-filter tests — taken together, not
// separately — have to hold.
func matchPropField(prop *propFilterReq, field *vcard.Field) (bool, error) {
	if len(prop.Params) == 0 && len(prop.TextMatches) == 0 {
		return true, nil
	}

	allOf := prop.Test == filterAllOf
	for i := range prop.Params {
		ok, err := matchParamFilter(&prop.Params[i], field)
		if err != nil {
			return false, err
		}
		if ok && !allOf {
			return true, nil
		}
		if !ok && allOf {
			return false, nil
		}
	}
	for i := range prop.TextMatches {
		ok, err := matchTextMatchValue(&prop.TextMatches[i], field.Value)
		if err != nil {
			return false, err
		}
		if ok && !allOf {
			return true, nil
		}
		if !ok && allOf {
			return false, nil
		}
	}
	return allOf, nil
}

// matchParamFilter reports whether one param-filter holds for the matched
// property (RFC 6352 §10.5.2).
func matchParamFilter(filter *paramFilterReq, field *vcard.Field) (bool, error) {
	values, present := field.Params[strings.ToUpper(filter.Name)]
	if !present || len(values) == 0 {
		return filter.IsNotDefined != nil, nil
	}
	if filter.IsNotDefined != nil {
		return false, nil
	}
	if filter.TextMatch == nil {
		return true, nil
	}
	// A parameter may carry several values; matching any one of them matches.
	for _, v := range values {
		ok, err := matchTextMatchValue(filter.TextMatch, v)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// fold applies the text-match collation, defaulting to i;unicode-casemap
// (RFC 6352 §10.5.4) so an unqualified text-match is case-insensitive.
func fold(txt *textMatchReq, s string) (string, error) {
	folded, err := internal.FoldForCollation(txt.Collation, internal.CollationUnicodeCasemap, s)
	if errors.Is(err, internal.ErrUnsupportedCollation) {
		return "", internal.NewPreconditionError(http.StatusForbidden, supportedCollationName)
	}
	return folded, err
}

func matchTextMatchValue(txt *textMatchReq, value string) (bool, error) {
	needle, err := fold(txt, txt.Text)
	if err != nil {
		return false, err
	}
	haystack, err := fold(txt, value)
	if err != nil {
		return false, err
	}

	var ok bool
	switch txt.MatchType {
	case matchEquals:
		ok = needle == haystack
	case matchContains, "":
		ok = strings.Contains(haystack, needle)
	case matchStartsWith:
		ok = strings.HasPrefix(haystack, needle)
	case matchEndsWith:
		ok = strings.HasSuffix(haystack, needle)
	}

	if bool(txt.NegateCondition) {
		ok = !ok
	}
	return ok, nil
}
