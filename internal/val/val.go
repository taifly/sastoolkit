// Package val coerces the loosely typed values SAS4 sends (a number may come
// as 14, 14.0 or "14") the way the Python app's str()/int()/truthiness did.
package val

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// M is one JSON object.
type M = map[string]any

// Str is Python's str() for JSON values, except that nil is "".
func Str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if x == math.Trunc(x) && !math.IsInf(x, 0) && math.Abs(x) < 1e16 {
			return strconv.FormatFloat(x, 'f', 1, 64)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case map[string]string:
		out := make(M, len(x))
		for k, s := range x {
			out[k] = s
		}
		return Dumps(out)
	default:
		return Dumps(x)
	}
}

// Float parses a number from any JSON value.
func Float(v any) (float64, bool) {
	switch x := v.(type) {
	case nil, bool:
		return 0, false
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(Str(v)), 64)
	return f, err == nil
}

// Int is Python's int(): whole numbers only ("14" and 14.0 pass, "1.5" does
// not).
func Int(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		if x == math.Trunc(x) {
			return int(x), true
		}
		return 0, false
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n), true
		}
		return 0, false
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		return n, err == nil
	}
	return 0, false
}

// Blank is `v in (None, "")`.
func Blank(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && s == ""
}

// BlankTrim is `v is None or str(v).strip() == ""`.
func BlankTrim(v any) bool {
	return v == nil || strings.TrimSpace(Str(v)) == ""
}

// Truthy is Python's bool().
func Truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case M:
		return len(x) > 0
	}
	return true
}

// Fold is str.casefold() for matching names.
func Fold(s string) string { return strings.ToLower(s) }

// Dumps is json.dumps(v, ensure_ascii=False).
func Dumps(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return pySpacing(strings.TrimRight(buf.String(), "\n"))
}

// pySpacing adds the space json.dumps puts after ',' and ':' outside strings,
// so report columns read the same as the Python app's.
func pySpacing(compact string) string {
	var out strings.Builder
	out.Grow(len(compact) + len(compact)/8)
	inString, escaped := false, false
	for i := 0; i < len(compact); i++ {
		c := compact[i]
		out.WriteByte(c)
		switch {
		case inString && escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case !inString && (c == ',' || c == ':'):
			out.WriteByte(' ')
		}
	}
	return out.String()
}

// Cut keeps the first n characters (not bytes) of s, like s[:n].
func Cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// Copy is a shallow dict copy.
func Copy(m M) M {
	out := make(M, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}
