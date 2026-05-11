package statok

import (
	"hash/fnv"
	"math"
	"strconv"
)

func canonicalUniqueID(id any) (string, bool) {
	switch v := id.(type) {
	case int:
		if v < 0 || uint64(v) > math.MaxUint32 {
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
		if v < 0 || uint64(v) > math.MaxUint32 {
			return "", false
		}
		return strconv.FormatUint(uint64(v), 10), true
	case uint:
		if uint64(v) > math.MaxUint32 {
			return "", false
		}
		return strconv.FormatUint(uint64(v), 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		if v > math.MaxUint32 {
			return "", false
		}
		return strconv.FormatUint(v, 10), true
	case uintptr:
		if uint64(v) > math.MaxUint32 {
			return "", false
		}
		return strconv.FormatUint(uint64(v), 10), true
	case string:
		return hashUniqueString(v)
	case []byte:
		if len(v) == 0 {
			return "", false
		}
		return hashUniqueBytes(v), true
	default:
		return "", false
	}
}

func hashUniqueString(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	return hashUniqueBytes([]byte(s)), true
}

func hashUniqueBytes(b []byte) string {
	h := fnv.New32a()
	_, _ = h.Write(b)
	return strconv.FormatUint(uint64(h.Sum32()), 10)
}
