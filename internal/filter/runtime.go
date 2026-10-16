// SPDX-License-Identifier: GPL-3.0-only
package filter

import (
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/metacubex/wazero"
	"github.com/metacubex/wazero/api"
	"github.com/metacubex/wazero/imports/wasi_snapshot_preview1"
)

//go:embed assets/quickjs.wasm
var quickJS []byte

//go:embed assets/filter.js
var filterJS string

var compilationCache = sync.OnceValue(wazero.NewCompilationCache)

// Two isolated machines bound concurrent JS memory independently of node count.
var machines = make(chan *machine, 2)
var slots = make(chan struct{}, 2)

type machine struct {
	runtime wazero.Runtime
	module  api.Module
	js      uint64
}

func runJS(ctx context.Context, script string) (string, error) {
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	var m *machine
	select {
	case m = <-machines:
	default:
	}
	if m == nil {
		var err error
		m, err = newMachine(ctx)
		if err != nil {
			return "", err
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := m.eval(callCtx, script)
	if err != nil {
		// Cancellation closes the module. Never call JS destructors on it.
		m.runtime.Close(context.Background())
		return "", err
	}
	machines <- m
	return result, nil
}

func newMachine(parent context.Context) (_ *machine, err error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	config := wazero.NewRuntimeConfig().WithCompilationCache(compilationCache()).WithCloseOnContextDone(true).WithMemoryLimitPages(1024)
	m := &machine{runtime: wazero.NewRuntimeWithConfig(context.Background(), config)}
	defer func() {
		if err != nil {
			m.runtime.Close(context.Background())
		}
	}()
	if _, err = wasi_snapshot_preview1.Instantiate(ctx, m.runtime); err != nil {
		return nil, err
	}
	// No Go objects, filesystem, sockets or environment are exposed to expressions.
	if _, err = m.runtime.NewHostModuleBuilder("env").NewFunctionBuilder().WithFunc(func(uint32, uint64, uint32, uint32) uint64 { return 0 }).Export("jsFunctionProxy").Instantiate(ctx); err != nil {
		return nil, err
	}
	compiled, err := m.runtime.CompileModule(ctx, quickJS)
	if err != nil {
		return nil, err
	}
	defer compiled.Close(context.Background())
	m.module, err = m.runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithStartFunctions())
	if err != nil {
		return nil, err
	}
	rt, err := m.call(ctx, "New_QJS", 32<<20, 1<<20, 0, 8<<20)
	if err != nil {
		return nil, fmt.Errorf("initialize filter runtime: %w", err)
	}
	if rt == 0 {
		return nil, errors.New("cannot initialize filter runtime")
	}
	m.js, err = m.call(ctx, "QJS_GetContext", rt)
	if err != nil {
		return nil, err
	}
	if _, err = m.eval(ctx, filterJS+"\n'initialized'"); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *machine) call(ctx context.Context, name string, args ...uint64) (uint64, error) {
	fn := m.module.ExportedFunction(name)
	if fn == nil {
		return 0, fmt.Errorf("missing filter runtime function %s", name)
	}
	values, err := fn.Call(ctx, args...)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("filter runtime %s: %w", name, err)
	}
	if len(values) == 0 {
		return 0, nil
	}
	return values[0], nil
}

func (m *machine) alloc(ctx context.Context, value string) (uint64, error) {
	p, err := m.call(ctx, "malloc", uint64(len(value)+1))
	if err != nil {
		return 0, err
	}
	if p == 0 || !m.module.Memory().Write(uint32(p), append([]byte(value), 0)) {
		return 0, errors.New("filter memory limit exceeded")
	}
	return p, nil
}

func (m *machine) eval(ctx context.Context, script string) (string, error) {
	code, err := m.alloc(ctx, script)
	if err != nil {
		return "", err
	}
	file, err := m.alloc(ctx, "filter")
	if err != nil {
		return "", err
	}
	option, err := m.call(ctx, "QJS_CreateEvalOption", code, 0, 0, file, 8)
	if err != nil {
		return "", err
	}
	value, err := m.call(ctx, "QJS_Eval", m.js, option)
	if err != nil {
		return "", err
	}
	hasException, err := m.call(ctx, "JS_HasException", m.js)
	if err != nil {
		return "", err
	}
	if hasException != 0 {
		// Do not expose expression contents or node credentials in diagnostics.
		return "", errors.New("invalid filter expression or filter resource limit exceeded")
	}
	text, err := m.stringValue(ctx, value)
	if err != nil {
		return "", err
	}
	for _, p := range []uint64{code, file, option} {
		if _, err = m.call(ctx, "free", p); err != nil {
			return "", err
		}
	}
	_, err = m.call(ctx, "QJS_FreeValue", m.js, value)
	return text, err
}

func (m *machine) stringValue(ctx context.Context, value uint64) (string, error) {
	packet, err := m.call(ctx, "QJS_ToCString", m.js, value)
	if err != nil {
		return "", err
	}
	packed, ok := m.module.Memory().Read(uint32(packet), 8)
	if packet == 0 || !ok {
		return "", errors.New("cannot read filter result")
	}
	n := binary.LittleEndian.Uint64(packed)
	ptr, length := uint32(n>>32), uint32(n)
	bytes, ok := m.module.Memory().Read(ptr, length)
	if !ok || length > 1<<20 {
		return "", errors.New("invalid filter result")
	}
	result := string(bytes)
	if _, err = m.call(ctx, "JS_FreeCString", m.js, uint64(ptr)); err != nil {
		return "", err
	}
	_, err = m.call(ctx, "free", packet)
	return result, err
}
