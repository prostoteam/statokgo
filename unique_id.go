package statok

import (
	"strconv"
)

func canonicalUniqueID(id any) (string, bool) {
	switch v := id.(type) {
	case int:
		if v < 0 {
			return "", false
		}
		return strconv.FormatUint(uint64(v), 10), true
	case int8:
		if v < 0 {
			return "", false
		}
		return strconv.FormatUint(uint64(v), 10), true
	case int16:
		if v < 0 {
			return "", false
		}
		return strconv.FormatUint(uint64(v), 10), true
	case int32:
		if v < 0 {
			return "", false
		}
		return strconv.FormatUint(uint64(v), 10), true
	case int64:
		if v < 0 {
			return "", false
		}
		return strconv.FormatUint(uint64(v), 10), true
	case uint:
		return strconv.FormatUint(uint64(v), 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	case uintptr:
		return strconv.FormatUint(uint64(v), 10), true
	case string:
		return canonicalUniqueDecimalString(v)
	case []byte:
		if len(v) == 0 {
			return "", false
		}
		return canonicalUniqueDecimalString(string(v))
	default:
		return "", false
	}
}

func canonicalUniqueDecimalString(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return "", false
	}
	return strconv.FormatUint(v, 10), true
}
