// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"reflect"
	"testing"
)

func TestGeoRules(t *testing.T) {
	for _, tc := range []struct {
		rule, key string
		want      any
	}{
		{"GEOSITE,CN,DIRECT", "rule_set", []string{"geosite-cn"}},
		{"GEOSITE,geolocation-!cn,proxy", "rule_set", []string{"geosite-geolocation-!cn"}},
		{"GEOIP,LAN,DIRECT,no-resolve", "ip_is_private", true},
	} {
		got, err := ConvertRule(tc.rule)
		if err != nil || !reflect.DeepEqual(got[tc.key], tc.want) {
			t.Fatalf("%s: %v %v", tc.rule, got, err)
		}
	}
	for _, rule := range []string{"GEOSITE,../private,DIRECT", "GEOSITE,,DIRECT", "GEOSITE,cn@ads,DIRECT"} {
		if _, err := ConvertRule(rule); err == nil {
			t.Fatalf("accepted %s", rule)
		}
	}
}
