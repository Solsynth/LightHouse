package model

// Snake-key helpers replicating System.Text.Json's JsonNamingPolicy
// SnakeCaseLower, applied to Dictionary<string, object?> values in Ring's
// API responses (AddJsonOptions sets DictionaryKeyPolicy = SnakeCaseLower).
// Property names of structs are handled by their json tags; only map keys
// (and nested maps/lists inside them) are converted, mirroring STJ's policy
// scope.

// toSnake converts a single name with STJ's SnakeCaseLower algorithm:
// an underscore is inserted before an uppercase char when the previous char
// is lowercase/digit, or when the previous char is uppercase and the next
// is lowercase; everything is lowercased. (ASCII scope, matching the fleet's
// dictionary-key usage; STJ's char.IsUpper handles Unicode the same way.)
func toSnake(name string) string {
	if name == "" {
		return name
	}
	out := make([]byte, 0, len(name)+4)
	var prev byte
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				if isLowerOrDigit(prev) {
					out = append(out, '_')
				} else if isUpperAscii(prev) && i+1 < len(name) && isLowerAscii(name[i+1]) {
					out = append(out, '_')
				}
			}
			out = append(out, c+'a'-'A')
		} else {
			out = append(out, c)
		}
		prev = c
	}
	return string(out)
}

func isUpperAscii(b byte) bool   { return b >= 'A' && b <= 'Z' }
func isLowerAscii(b byte) bool   { return b >= 'a' && b <= 'z' }
func isLowerOrDigit(b byte) bool { return isLowerAscii(b) || (b >= '0' && b <= '9') }

// SnakeMapKeys recursively converts map keys (and map keys nested inside
// slices) to snake_case, replicating DictionaryKeyPolicy = SnakeCaseLower on
// outbound serialization. Structs are passed through untouched.
func SnakeMapKeys(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[toSnake(k)] = SnakeMapKeys(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = SnakeMapKeys(val)
		}
		return out
	default:
		return v
	}
}
