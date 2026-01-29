package ssh

import "strings"

// ShellEscape escapes a string for safe use as a single-quoted shell argument.
// It wraps the value in single quotes and escapes any embedded single quotes
// using the '\'' idiom (end quote, literal quote, restart quote).
func ShellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
