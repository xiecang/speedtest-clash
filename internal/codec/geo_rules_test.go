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
		{"GEOIP,cn,DIRECT", "rule_set", []string{"geoip-cn"}},
		{"GEOIP,CN,DIRECT", "rule_set", []string{"geoip-cn"}},
		{"GEOIP,us,DIRECT,no-resolve", "rule_set", []string{"geoip-us"}},
	} {
		got, err := ConvertRule(tc.rule)
		if err != nil || !reflect.DeepEqual(got[tc.key], tc.want) {
			t.Fatalf("%s: %v %v", tc.rule, got, err)
		}
	}
	for _, rule := range []string{"GEOSITE,../private,DIRECT", "GEOSITE,,DIRECT", "GEOSITE,cn@ads,DIRECT", "GEOIP,usa,DIRECT", "GEOIP,c1,DIRECT"} {
		if _, err := ConvertRule(rule); err == nil {
			t.Fatalf("accepted %s", rule)
		}
	}
}

func TestLogicalRuleOuterParens(t *testing.T) {
	notRule, err := ConvertRule("NOT,((DOMAIN,example.com)),REJECT")
	if err != nil {
		t.Fatalf("NOT double-parens failed: %v", err)
	}
	if notRule["type"] != "logical" || notRule["invert"] != true || notRule["action"] != "reject" {
		t.Fatalf("unexpected NOT rule: %v", notRule)
	}

	for _, valid := range []string{
		"NOT,(DOMAIN,example.com),REJECT",
		"AND,(((DOMAIN,a.com),(DOMAIN,b.com))),DIRECT",
		"OR,((DOMAIN,a.com),(DST-PORT,443)),DIRECT",
		"AND,((DOMAIN,only.com)),REJECT",
		"NOT,((AND,((DOMAIN,a.com),(NETWORK,UDP)))),REJECT",
	} {
		if _, err := ConvertRule(valid); err != nil {
			t.Fatalf("valid rule %q failed: %v", valid, err)
		}
	}

	for _, invalid := range []string{
		"(DOMAIN,a.com),(DOMAIN,b.com),DIRECT",
		"NOT,((DOMAIN,a.com),(DOMAIN,b.com)),REJECT",
		"AND,(),REJECT",
		"NOT,((DOMAIN,example.com),REJECT",
	} {
		if _, err := ConvertRule(invalid); err == nil {
			t.Fatalf("invalid rule %q accepted", invalid)
		}
	}
}
