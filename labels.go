package statok

import "strings"

var labelReplacer = strings.NewReplacer(
	"=", "_",
	"|", "_",
	"\n", "_",
	"\r", "_",
)

// Label formats a key/value pair using "k=v" syntax expected by the collector.
func Label(key, value string) string {
	key = labelReplacer.Replace(key)
	value = labelReplacer.Replace(value)

	return key + "=" + value
}

func cloneLabels(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	cp := make([]string, len(in))
	copy(cp, in)
	return cp
}
