package models

import (
	"context"
	"github.com/metacubex/mihomo/constant"
)

type CheckType string

const (
	CheckTypeGPTWeb     CheckType = "gpt_web"
	CheckTypeGPTAndroid CheckType = "gpt_android"
	CheckTypeGPTIOS     CheckType = "gpt_ios"
)

type CheckResult struct {
	OK    bool
	Value string
	Type  CheckType
}

func NewCheckResult(tp CheckType, ok bool, value string) CheckResult {
	return CheckResult{
		Type:  tp,
		OK:    ok,
		Value: value,
	}
}

type Checker interface {
	Check(ctx context.Context, proxy constant.Proxy) (result CheckResult, err error)
}
