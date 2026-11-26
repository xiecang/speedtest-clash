// SPDX-License-Identifier: GPL-3.0-only
package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"sync"
)

func CloneMap(in map[string]any) map[string]any {
	result := make(map[string]any, len(in))
	for k, v := range in {
		result[k] = cloneValue(v)
	}
	return result
}

func cloneValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		if val == nil {
			return (map[string]any)(nil)
		}
		return CloneMap(val)
	case map[string]string:
		if val == nil {
			return (map[string]string)(nil)
		}
		out := make(map[string]string, len(val))
		for k, item := range val {
			out[k] = item
		}
		return out
	case map[string][]string:
		if val == nil {
			return (map[string][]string)(nil)
		}
		out := make(map[string][]string, len(val))
		for k, slice := range val {
			if slice == nil {
				out[k] = nil
				continue
			}
			cp := make([]string, len(slice))
			copy(cp, slice)
			out[k] = cp
		}
		return out
	case []any:
		if val == nil {
			return ([]any)(nil)
		}
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = cloneValue(item)
		}
		return out
	case []string:
		if val == nil {
			return ([]string)(nil)
		}
		out := make([]string, len(val))
		copy(out, val)
		return out
	case []int:
		if val == nil {
			return ([]int)(nil)
		}
		out := make([]int, len(val))
		copy(out, val)
		return out
	default:
		return v
	}
}

func isIdentityExcludedField(field string) bool {
	switch field {
	case "name", "tag", "dialer-proxy", "detour", "speed", "delay", "latency", "tested_at", "updated_at", "date", "quality", "hidden":
		return true
	default:
		return false
	}
}

var identityBufPool = sync.Pool{
	New: func() any {
		return bytes.NewBuffer(make([]byte, 0, 512))
	},
}

// IdentityKey excludes display names and hashes the actual dependency identities.
func IdentityKey(config map[string]any, format Format, dependencies []string) (string, error) {
	sum, err := identitySum(config, format, dependencies)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum[:]), nil
}

func identitySum(config map[string]any, format Format, dependencies []string) ([32]byte, error) {
	buf := identityBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	if writeFastIdentityJSON(buf, config, format, dependencies) {
		sum := sha256.Sum256(buf.Bytes())
		if buf.Cap() <= 64<<10 {
			identityBufPool.Put(buf)
		}
		return sum, nil
	}
	if buf.Cap() <= 64<<10 {
		identityBufPool.Put(buf)
	}

	identity := make(map[string]any, len(config))
	for k, v := range config {
		if !isIdentityExcludedField(k) {
			identity[k] = v
		}
	}
	content, err := json.Marshal(struct {
		Format       Format
		Config       map[string]any
		Dependencies []string
	}{format, identity, dependencies})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(content), nil
}

func ValidateIdentity(node Node) error {
	if len(node.Key) != 64 || node.Revision != node.Key {
		return errors.New("node identity does not match configuration and path")
	}
	var want [32]byte
	for i := 0; i < 32; i++ {
		hi, ok1 := fromLowerHex(node.Key[i*2])
		lo, ok2 := fromLowerHex(node.Key[i*2+1])
		if !ok1 || !ok2 {
			return errors.New("node identity does not match configuration and path")
		}
		want[i] = (hi << 4) | lo
	}
	sum, err := identitySum(node.Config, node.Format, node.Dependencies)
	if err != nil {
		return err
	}
	if sum != want {
		return errors.New("node identity does not match configuration and path")
	}
	return nil
}

func fromLowerHex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	default:
		return 0, false
	}
}

func writeFastIdentityJSON(buf *bytes.Buffer, config map[string]any, format Format, dependencies []string) bool {
	buf.WriteString(`{"Format":`)
	if !writeFastJSONString(buf, string(format)) {
		return false
	}
	buf.WriteString(`,"Config":{`)
	if !writeFastJSONMapBody(buf, config, true) {
		return false
	}
	buf.WriteString(`},"Dependencies":`)
	if dependencies == nil {
		buf.WriteString("null")
	} else {
		buf.WriteByte('[')
		for i, dep := range dependencies {
			if i > 0 {
				buf.WriteByte(',')
			}
			if !writeFastJSONString(buf, dep) {
				return false
			}
		}
		buf.WriteByte(']')
	}
	buf.WriteByte('}')
	return true
}

func writeFastJSONMapBody(buf *bytes.Buffer, m map[string]any, excludeTopLevel bool) bool {
	var stackKeys [32]string
	keys := stackKeys[:0]
	for k := range m {
		if excludeTopLevel && isIdentityExcludedField(k) {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) <= 32 {
		for i := 1; i < len(keys); i++ {
			cur := keys[i]
			j := i - 1
			for j >= 0 && keys[j] > cur {
				keys[j+1] = keys[j]
				j--
			}
			keys[j+1] = cur
		}
	} else {
		sort.Strings(keys)
	}
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		if !writeFastJSONString(buf, k) {
			return false
		}
		buf.WriteByte(':')
		if !writeFastJSONValue(buf, m[k]) {
			return false
		}
	}
	return true
}

func writeFastJSONString(buf *bytes.Buffer, s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			return false
		}
	}
	buf.WriteByte('"')
	buf.WriteString(s)
	buf.WriteByte('"')
	return true
}

func writeFastJSONValue(buf *bytes.Buffer, v any) bool {
	switch val := v.(type) {
	case nil:
		buf.WriteString("null")
		return true
	case string:
		return writeFastJSONString(buf, val)
	case bool:
		if val {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return true
	case int:
		var tmp [24]byte
		buf.Write(strconv.AppendInt(tmp[:0], int64(val), 10))
		return true
	case int64:
		var tmp [24]byte
		buf.Write(strconv.AppendInt(tmp[:0], val, 10))
		return true
	case int32:
		var tmp [16]byte
		buf.Write(strconv.AppendInt(tmp[:0], int64(val), 10))
		return true
	case uint:
		var tmp [24]byte
		buf.Write(strconv.AppendUint(tmp[:0], uint64(val), 10))
		return true
	case uint64:
		var tmp [24]byte
		buf.Write(strconv.AppendUint(tmp[:0], val, 10))
		return true
	case uint32:
		var tmp [16]byte
		buf.Write(strconv.AppendUint(tmp[:0], uint64(val), 10))
		return true
	case float64:
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return false
		}
		if val >= -9007199254740992 && val <= 9007199254740992 && math.Trunc(val) == val && !(val == 0 && math.Signbit(val)) {
			var tmp [24]byte
			buf.Write(strconv.AppendInt(tmp[:0], int64(val), 10))
			return true
		}
		return false
	case map[string]any:
		if val == nil {
			buf.WriteString("null")
			return true
		}
		buf.WriteByte('{')
		if !writeFastJSONMapBody(buf, val, false) {
			return false
		}
		buf.WriteByte('}')
		return true
	case map[string]string:
		if val == nil {
			buf.WriteString("null")
			return true
		}
		var stackKeys [16]string
		keys := stackKeys[:0]
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if !writeFastJSONString(buf, k) {
				return false
			}
			buf.WriteByte(':')
			if !writeFastJSONString(buf, val[k]) {
				return false
			}
		}
		buf.WriteByte('}')
		return true
	case []any:
		if val == nil {
			buf.WriteString("null")
			return true
		}
		buf.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			if !writeFastJSONValue(buf, item) {
				return false
			}
		}
		buf.WriteByte(']')
		return true
	case []string:
		if val == nil {
			buf.WriteString("null")
			return true
		}
		buf.WriteByte('[')
		for i, item := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			if !writeFastJSONString(buf, item) {
				return false
			}
		}
		buf.WriteByte(']')
		return true
	case []int:
		if val == nil {
			buf.WriteString("null")
			return true
		}
		buf.WriteByte('[')
		var tmp [24]byte
		for i, item := range val {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(strconv.AppendInt(tmp[:0], int64(item), 10))
		}
		buf.WriteByte(']')
		return true
	default:
		return false
	}
}
