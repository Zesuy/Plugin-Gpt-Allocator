package importer

import (
	"fmt"
	"strings"
	"unicode"
)

func AvailableAuthFile(email, provider, identity string, reserved map[string]struct{}) string {
	base := sanitizeName(strings.ToLower(strings.TrimSpace(email)))
	if base == "" {
		base = sanitizeName(strings.ToLower(strings.TrimSpace(provider)))
		if base == "" {
			base = "credential"
		}
		if len(identity) >= 8 {
			base += "_" + identity[:8]
		}
	}
	for suffix := 0; ; suffix++ {
		name := base + ".json"
		if suffix > 0 {
			name = fmt.Sprintf("%s_%d.json", base, suffix)
		}
		if _, exists := reserved[strings.ToLower(name)]; !exists {
			reserved[strings.ToLower(name)] = struct{}{}
			return name
		}
	}
}

func sanitizeName(value string) string {
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '@', r == '.', r == '-', r == '_':
			builder.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				builder.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(builder.String(), "._-")
}
