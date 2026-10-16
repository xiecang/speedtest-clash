// SPDX-License-Identifier: GPL-3.0-only
package codec

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// ConvertRule preserves supported Clash routing predicates; unsupported
// predicates fail publication rather than silently broadening the match.
func ConvertRule(rule string) (map[string]any, error) {
	parts, err := splitRule(rule)
	if err != nil || len(parts) < 2 {
		return nil, errors.New("invalid routing rule")
	}
	if parts[len(parts)-1] == "no-resolve" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 || parts[len(parts)-1] == "" {
		return nil, errors.New("empty routing target")
	}
	target := parts[len(parts)-1]
	predicate := strings.Join(parts[:len(parts)-1], ",")
	if parts[0] == "MATCH" && len(parts) != 2 {
		return nil, errors.New("invalid MATCH rule")
	}
	var result map[string]any
	if parts[0] == "MATCH" {
		result = map[string]any{}
	} else {
		result, err = rulePredicate(predicate)
		if err != nil {
			return nil, err
		}
	}
	if target == "REJECT" {
		result["action"] = "reject"
	} else {
		if target == "DIRECT" {
			target = "direct"
		}
		result["action"] = "route"
		result["outbound"] = target
	}
	return result, nil
}

// RewriteRuleTarget changes only the outer target, retaining logical predicates
// and no-resolve. The same parser validates both source and resulting rules.
func RewriteRuleTarget(rule, target string) (string, error) {
	if strings.TrimSpace(target) == "" || strings.ContainsAny(target, ",()\n\r") {
		return "", errors.New("invalid rule target")
	}
	if _, err := ConvertRule(rule); err != nil {
		return "", err
	}
	parts, err := splitRule(rule)
	if err != nil {
		return "", err
	}
	index := len(parts) - 1
	if parts[index] == "no-resolve" {
		index--
	}
	parts[index] = target
	result := strings.Join(parts, ",")
	_, err = ConvertRule(result)
	return result, err
}

func splitRule(text string) ([]string, error) {
	var parts []string
	depth, start := 0, 0
	for i, r := range text {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, errors.New("unbalanced rule")
			}
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(text[start:i]))
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, errors.New("unbalanced rule")
	}
	parts = append(parts, strings.TrimSpace(text[start:]))
	return parts, nil
}

func rulePredicate(text string) (map[string]any, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") {
		text = text[1 : len(text)-1]
	}
	parts, err := splitRule(text)
	if err != nil || len(parts) < 2 {
		return nil, errors.New("invalid rule predicate")
	}
	if parts[0] != "AND" && parts[0] != "OR" && parts[0] != "NOT" && (len(parts) != 2 || parts[1] == "") {
		return nil, errors.New("invalid rule value")
	}
	kind, value := parts[0], strings.Join(parts[1:], ",")
	result := map[string]any{}
	switch kind {
	case "AND", "OR", "NOT":
		children, err := splitRule(strings.TrimSuffix(strings.TrimPrefix(value, "("), ")"))
		if err != nil {
			return nil, err
		}
		nested := []map[string]any{}
		if kind == "NOT" {
			children = []string{value}
		}
		for _, child := range children {
			predicate, err := rulePredicate(child)
			if err != nil {
				return nil, err
			}
			nested = append(nested, predicate)
		}
		if kind == "NOT" && len(nested) != 1 {
			return nil, errors.New("NOT requires one predicate")
		}
		mode := "and"
		if kind == "OR" {
			mode = "or"
		}
		result = map[string]any{"type": "logical", "mode": mode, "rules": nested}
		if kind == "NOT" {
			result["invert"] = true
		}
	case "DOMAIN":
		result["domain"] = []string{value}
	case "DOMAIN-SUFFIX":
		result["domain_suffix"] = []string{value}
	case "DOMAIN-KEYWORD":
		result["domain_keyword"] = []string{value}
	case "NETWORK":
		if value != "TCP" && value != "UDP" {
			return nil, errors.New("invalid network")
		}
		result["network"] = strings.ToLower(value)
	case "DST-PORT", "SRC-PORT":
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return nil, errors.New("invalid rule port")
		}
		field := "port"
		if kind == "SRC-PORT" {
			field = "source_port"
		}
		result[field] = []int{port}
	case "IP-CIDR", "IP-CIDR6", "SRC-IP-CIDR":
		if _, err := netip.ParsePrefix(value); err != nil {
			return nil, errors.New("invalid rule CIDR")
		}
		field := "ip_cidr"
		if kind == "SRC-IP-CIDR" {
			field = "source_ip_cidr"
		}
		result[field] = []string{value}
	case "PROCESS-NAME":
		result["process_name"] = []string{value}
	case "PROCESS-PATH":
		result["process_path"] = []string{value}
	case "RULE-SET":
		result["rule_set"] = []string{value}
	case "GEOSITE":
		if strings.IndexFunc(value, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '!')
		}) >= 0 {
			return nil, errors.New("invalid GEOSITE name")
		}
		result["rule_set"] = []string{"geosite-" + strings.ToLower(value)}
	case "GEOIP":
		if strings.EqualFold(value, "private") || strings.EqualFold(value, "LAN") {
			result["ip_is_private"] = true
			break
		}
		if len(value) != 2 || strings.IndexFunc(value, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
			return nil, errors.New("GEOIP requires an ISO country code")
		}
		result["rule_set"] = []string{"geoip-" + strings.ToLower(value)}
	default:
		return nil, fmt.Errorf("routing predicate %q cannot be converted safely", kind)
	}
	return result, nil
}
