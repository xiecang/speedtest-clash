// SPDX-License-Identifier: GPL-3.0-only
package engine

import (
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/xiecang/node-quality/v2/internal/model"
)

func Capabilities() model.BuildInfo {
	result := model.BuildInfo{Profiles: []string{"general", "ai", "availability", "services"}, SchemaVersion: 2, Mihomo: model.MihomoVersion, SingBox: model.SingBoxVersion, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-tags" {
				result.Tags = strings.Split(setting.Value, ",")
			}
		}
	}
	return result
}
