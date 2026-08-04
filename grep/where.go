package grep

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// WhereFilter is a predicate that tests whether a match result satisfies a
// where-clause constraint. It returns true if the result passes the filter.
type WhereFilter func(result *Result, source []byte, lang *gotreesitter.Language) bool

// CompileWhere compiles a where-clause string into a WhereFilter function.
//
// Supported constraint forms:
//
//	contains($CAP, <text>)          — capture text contains literal text
//	not contains($CAP, <text>)      — capture text does NOT contain literal text
//	matches($CAP, "regex")          — capture text matches regex
//	not matches($CAP, "regex")      — capture text does NOT match regex
//
// Multiple constraints can be combined with semicolons or newlines; all must
// pass (logical AND).
func CompileWhere(where string) (WhereFilter, error) {
	where = strings.TrimSpace(where)
	if where == "" {
		// Empty where clause matches everything.
		return func(*Result, []byte, *gotreesitter.Language) bool { return true }, nil
	}

	// Split on semicolons and newlines to support multiple constraints.
	clauses := splitClauses(where)

	var filters []WhereFilter
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		f, err := compileClause(clause)
		if err != nil {
			return nil, fmt.Errorf("where clause %q: %w", clause, err)
		}
		filters = append(filters, f)
	}

	if len(filters) == 0 {
		return func(*Result, []byte, *gotreesitter.Language) bool { return true }, nil
	}

	// All filters must pass (AND semantics).
	return func(result *Result, source []byte, lang *gotreesitter.Language) bool {
		for _, f := range filters {
			if !f(result, source, lang) {
				return false
			}
		}
		return true
	}, nil
}

// splitClauses splits a where string on semicolons and newlines.
func splitClauses(s string) []string {
	// Replace newlines with semicolons, then split.
	s = strings.ReplaceAll(s, "\n", ";")
	return strings.Split(s, ";")
}

// compileClause compiles a single where constraint.
func compileClause(clause string) (WhereFilter, error) {
	clause = strings.TrimSpace(clause)

	// Check for "not" prefix.
	negated := false
	if strings.HasPrefix(clause, "not ") {
		negated = true
		clause = strings.TrimSpace(clause[4:])
	}

	// Parse the function call form: funcName($CAP, arg)
	if strings.HasPrefix(clause, "contains(") {
		return compileContains(clause, negated)
	}
	if strings.HasPrefix(clause, "matches(") {
		return compileMatches(clause, negated)
	}

	return nil, fmt.Errorf("unsupported constraint: %q", clause)
}

// compileContains compiles a contains($CAP, <text>) constraint.
func compileContains(clause string, negated bool) (WhereFilter, error) {
	capName, arg, err := parseTwoArgFunc(clause, "contains")
	if err != nil {
		return nil, err
	}

	argBytes := []byte(arg)
	return func(result *Result, source []byte, lang *gotreesitter.Language) bool {
		found := bytes.Contains(captureBytes(result, capName, source), argBytes)
		if negated {
			return !found
		}
		return found
	}, nil
}

// compileMatches compiles a matches($CAP, "regex") constraint.
func compileMatches(clause string, negated bool) (WhereFilter, error) {
	capName, pattern, err := parseTwoArgFunc(clause, "matches")
	if err != nil {
		return nil, err
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %w", pattern, err)
	}

	return func(result *Result, source []byte, lang *gotreesitter.Language) bool {
		matched := re.Match(captureBytes(result, capName, source))
		if negated {
			return !matched
		}
		return matched
	}, nil
}

// parseTwoArgFunc parses a function call of the form funcName($CAP, arg) and
// returns the capture name (without $) and the argument string (with quotes
// stripped if present).
func parseTwoArgFunc(clause, funcName string) (capName, arg string, err error) {
	// Strip "funcName(" prefix and ")" suffix.
	inner := strings.TrimPrefix(clause, funcName+"(")
	if !strings.HasSuffix(inner, ")") {
		return "", "", fmt.Errorf("expected closing ')' in %s call", funcName)
	}
	inner = inner[:len(inner)-1]

	// Split on the first comma.
	commaIdx := strings.Index(inner, ",")
	if commaIdx < 0 {
		return "", "", fmt.Errorf("%s requires two arguments: %s($CAP, value)", funcName, funcName)
	}

	capRef := strings.TrimSpace(inner[:commaIdx])
	arg = strings.TrimSpace(inner[commaIdx+1:])

	// Strip the leading $ from the capture reference.
	if strings.HasPrefix(capRef, "$") {
		capName = capRef[1:]
	} else {
		capName = capRef
	}

	if capName == "" {
		return "", "", fmt.Errorf("%s: empty capture name", funcName)
	}

	// Strip surrounding quotes from the argument if present.
	arg = stripQuotes(arg)

	return capName, arg, nil
}

// stripQuotes removes surrounding double quotes or single quotes from s.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// captureBytes returns the text of a named capture as bytes, without any
// allocation: it aliases the capture's already-materialized Text (or the source
// sub-slice). The where-filter callers only read the result (bytes.Contains /
// Regexp.Match), so aliasing is safe and avoids the per-result string copy the
// old captureText helper paid.
func captureBytes(result *Result, capName string, source []byte) []byte {
	cap, ok := result.Captures[capName]
	if !ok {
		return nil
	}
	if len(cap.Text) > 0 {
		return cap.Text
	}
	// Fallback to source bytes.
	if int(cap.EndByte) <= len(source) {
		return source[cap.StartByte:cap.EndByte]
	}
	return nil
}
