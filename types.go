// SPDX-License-Identifier: GPL-3.0-only
package nodequality

import (
	"github.com/xiecang/node-quality/v2/internal/codec"
	"github.com/xiecang/node-quality/v2/internal/engine"
	"github.com/xiecang/node-quality/v2/internal/measure"
	"github.com/xiecang/node-quality/v2/internal/model"
	"github.com/xiecang/node-quality/v2/internal/probe"
	"github.com/xiecang/node-quality/v2/internal/source"
)

// ErrNoNodes distinguishes a completely parsed empty generation from a failed load.
var ErrNoNodes = source.ErrNoNodes

// Public aliases retain the original V2 values and method sets without copies.
type (
	BuildInfo     = model.BuildInfo
	Budget        = measure.Budget
	ProbeOptions  = probe.ProbeOptions
	Batch         = engine.Batch
	SourceSummary = source.SourceSummary
	SourceRequest = source.SourceRequest
	SourceResult  = source.SourceResult
	Status        = model.Status
	FailureOrigin = model.FailureOrigin
	Core          = model.Core
	Format        = model.Format
	Node          = model.Node
	Diagnostic    = model.Diagnostic
	DecodeResult  = model.DecodeResult
	DialContext   = model.DialContext
	StreamResult  = model.StreamResult
	ServiceResult = model.ServiceResult
	IPResult      = model.IPResult
	Metrics       = model.Metrics
	Report        = model.Report
	ServiceCheck  = model.ServiceCheck
	AIPlan        = model.AIPlan
	Plan          = model.Plan
)

const (
	SchemaVersion            = model.SchemaVersion
	MetricVersion            = model.MetricVersion
	MihomoVersion            = model.MihomoVersion
	SingBoxVersion           = model.SingBoxVersion
	MaxSourceBytes           = codec.MaxSourceBytes
	Pass                     = model.Pass
	Fail                     = model.Fail
	Unknown                  = model.Unknown
	FailureOriginNode        = model.FailureOriginNode
	FailureOriginEnvironment = model.FailureOriginEnvironment
	FailureOriginProbe       = model.FailureOriginProbe
	FailureOriginUnknown     = model.FailureOriginUnknown
	Mihomo                   = model.Mihomo
	SingBox                  = model.SingBox
	ClashYAML                = model.ClashYAML
	SingBoxJSON              = model.SingBoxJSON
	URI                      = model.URI
)
