package jsonstrict

// SingleObject refuses multiple or incomplete objects so syntax repair cannot accept a truncated prefix.
func SingleObject(raw []byte) ([]byte, bool) {
	var candidate []byte
	depth, start, count := 0, 0, 0
	inString, escaped := false, false
	for i, b := range raw {
		if depth == 0 {
			if b == '{' {
				depth, start, inString, escaped = 1, i, false, false
			}
			continue
		}
		if inString {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			default:
				// Any other byte inside a string is content.
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				count++
				candidate = raw[start : i+1]
			}
		default:
			// Bytes outside strings and braces do not change the nesting.
		}
	}
	return candidate, count == 1 && depth == 0
}
