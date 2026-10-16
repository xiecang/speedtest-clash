// SPDX-License-Identifier: GPL-3.0-only
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

func CloneMap(in map[string]any) map[string]any {
	result := make(map[string]any, len(in))
	for k, v := range in {
		result[k] = v
	}
	return result
}

// IdentityKey excludes display names and hashes the actual dependency identities.
func IdentityKey(config map[string]any, format Format, dependencies []string) (string, error) {
	identity := CloneMap(config)
	for _, field := range []string{"name", "tag", "dialer-proxy", "detour", "speed", "delay", "latency", "tested_at", "updated_at", "date", "quality", "hidden"} {
		delete(identity, field)
	}
	content, err := json.Marshal(struct {
		Format       Format
		Config       map[string]any
		Dependencies []string
	}{format, identity, dependencies})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func ValidateIdentity(node Node) error {
	key, err := IdentityKey(node.Config, node.Format, node.Dependencies)
	if err != nil {
		return err
	}
	if key != node.Key || node.Revision != key {
		return errors.New("node identity does not match configuration and path")
	}
	return nil
}
