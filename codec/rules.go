package codec

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// ConvertRule converts supported Clash routing predicates to sing-box 1.14 rule objects.
// Unsupported predicates return an error rather than silently broadening matches.
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

// RewriteRuleTarget replaces only the outer target while preserving logical predicates and no-resolve.
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

func stripMatchingOuterParens(text string) string {
	for {
		text = strings.TrimSpace(text)
		if len(text) < 2 || text[0] != '(' || text[len(text)-1] != ')' {
			return text
		}
		depth := 0
		matched := true
		for i := 0; i < len(text)-1; i++ {
			switch text[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth <= 0 {
					matched = false
				}
			}
			if !matched {
				break
			}
		}
		if !matched || depth != 1 {
			return text
		}
		text = text[1 : len(text)-1]
	}
}

func rulePredicate(text string) (map[string]any, error) {
	text = stripMatchingOuterParens(text)
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
		inner := stripMatchingOuterParens(value)
		children := []string{inner}
		if strings.HasPrefix(inner, "(") {
			children, err = splitRule(inner)
			if err != nil {
				return nil, err
			}
		}
		if kind == "NOT" && len(children) != 1 {
			return nil, errors.New("NOT requires one predicate")
		}
		nested := make([]map[string]any, 0, len(children))
		for _, child := range children {
			predicate, err := rulePredicate(child)
			if err != nil {
				return nil, err
			}
			nested = append(nested, predicate)
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
	case "DOMAIN-REGEX":
		if _, err := regexp.Compile(value); err != nil {
			return nil, fmt.Errorf("invalid DOMAIN-REGEX: %w", err)
		}
		result["domain_regex"] = []string{value}
	case "NETWORK":
		if !strings.EqualFold(value, "TCP") && !strings.EqualFold(value, "UDP") {
			return nil, errors.New("invalid network")
		}
		result["network"] = strings.ToLower(value)
	case "DST-PORT", "SRC-PORT":
		ports, ranges, err := parseRulePortSpec(value)
		if err != nil {
			return nil, err
		}
		portField, rangeField := "port", "port_range"
		if kind == "SRC-PORT" {
			portField, rangeField = "source_port", "source_port_range"
		}
		if len(ports) > 0 {
			result[portField] = ports
		}
		if len(ranges) > 0 {
			result[rangeField] = ranges
		}
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
	case "PROCESS-PATH-REGEX", "PROCESS-NAME-REGEX":
		if _, err := regexp.Compile(value); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", kind, err)
		}
		result["process_path_regex"] = []string{value}
	case "RULE-SET":
		result["rule_set"] = []string{value}
	case "GEOSITE":
		invert := false
		if strings.HasPrefix(value, "!") {
			invert = true
			value = value[1:]
		}
		if value == "" || strings.IndexFunc(value, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '!')
		}) >= 0 {
			return nil, errors.New("invalid GEOSITE name")
		}
		result["rule_set"] = []string{"geosite-" + strings.ToLower(value)}
		if invert {
			result["invert"] = true
		}
	case "GEOIP":
		invert := false
		if strings.HasPrefix(value, "!") {
			invert = true
			value = value[1:]
		}
		if strings.EqualFold(value, "private") || strings.EqualFold(value, "LAN") {
			result["ip_is_private"] = true
			if invert {
				result["invert"] = true
			}
			break
		}
		value = strings.ToUpper(value)
		if len(value) != 2 || strings.IndexFunc(value, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
			return nil, errors.New("GEOIP requires an ISO country code")
		}
		result["rule_set"] = []string{"geoip-" + strings.ToLower(value)}
		if invert {
			result["invert"] = true
		}
	default:
		return nil, fmt.Errorf("routing predicate %q cannot be converted safely", kind)
	}
	return result, nil
}

func parseRulePortSpec(spec string) ([]int, []string, error) {
	var ports []int
	var ranges []string
	tokens := strings.FieldsFunc(spec, func(r rune) bool {
		return r == '/' || r == ' ' || r == '\t'
	})
	if len(tokens) == 0 {
		return nil, nil, errors.New("invalid rule port")
	}
	for _, token := range tokens {
		sep := ""
		if strings.Contains(token, "-") {
			sep = "-"
		} else if strings.Contains(token, ":") {
			sep = ":"
		}
		if sep != "" {
			startStr, endStr, ok := strings.Cut(token, sep)
			if !ok || startStr == "" || endStr == "" || strings.ContainsAny(endStr, "-:") {
				return nil, nil, errors.New("invalid rule port range")
			}
			start, err1 := strconv.Atoi(startStr)
			end, err2 := strconv.Atoi(endStr)
			if err1 != nil || err2 != nil || start < 1 || end > 65535 || start > end {
				return nil, nil, errors.New("invalid rule port range")
			}
			ranges = append(ranges, fmt.Sprintf("%d:%d", start, end))
			continue
		}
		port, err := strconv.Atoi(token)
		if err != nil || port < 1 || port > 65535 {
			return nil, nil, errors.New("invalid rule port")
		}
		ports = append(ports, port)
	}
	return ports, ranges, nil
}
