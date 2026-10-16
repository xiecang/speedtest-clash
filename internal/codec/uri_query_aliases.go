// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"errors"
	"net/url"
)

func normalizeLegacyURIQuery(u *url.URL) error {
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return errors.New("invalid node URI query")
	}
	changed := false
	for alias, canonical := range map[string]string{"servername": "sni", "peer": "sni", "max_early_data": "ed", "early_data_header_name": "eh"} {
		values, ok := q[alias]
		if !ok {
			continue
		}
		if len(values) != 1 {
			return errors.New("repeated URI alias")
		}
		if existing, ok := q[canonical]; ok && (len(existing) != 1 || existing[0] != values[0]) {
			return errors.New("conflicting URI aliases")
		}
		q.Set(canonical, values[0])
		q.Del(alias)
		changed = true
	}
	if u.Scheme == "hysteria2" && q.Has("security") && q.Get("security") != "tls" {
		return errors.New("Hysteria2 requires TLS")
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return nil
}
