//go:build windows

package screenshot2

import (
	"errors"
	"strings"
	"testing"
)

// DWM 关闭：按平台能力仅做可见性确认，不调用 DwmFlush，返回成功。
func TestConfirmDWMQuietDisabledComposition(t *testing.T) {
	origEnabled, origFlush := dwmCompositionEnabledFn, dwmFlushFn
	defer func() { dwmCompositionEnabledFn, dwmFlushFn = origEnabled, origFlush }()

	dwmCompositionEnabledFn = func() bool { return false }
	flushCalls := 0
	dwmFlushFn = func() error {
		flushCalls++
		return nil
	}

	if err := confirmDWMQuiet(); err != nil {
		t.Fatalf("visibility-only confirmation should succeed, got: %v", err)
	}
	if flushCalls != 0 {
		t.Fatalf("DwmFlush must not be called when composition is disabled, got %d calls", flushCalls)
	}
}

// DWM 开启且 DwmFlush 成功：恰好两个合成周期，返回成功。
func TestConfirmDWMQuietSuccess(t *testing.T) {
	origEnabled, origFlush := dwmCompositionEnabledFn, dwmFlushFn
	defer func() { dwmCompositionEnabledFn, dwmFlushFn = origEnabled, origFlush }()

	dwmCompositionEnabledFn = func() bool { return true }
	flushCalls := 0
	dwmFlushFn = func() error {
		flushCalls++
		return nil
	}

	if err := confirmDWMQuiet(); err != nil {
		t.Fatalf("expected nil error on successful flushes, got: %v", err)
	}
	if flushCalls != 2 {
		t.Fatalf("expected exactly 2 flush calls, got %d", flushCalls)
	}
}

// DwmFlush 失败：不得报告已确认，返回携带真实 HRESULT 的错误，且失败后不再重试。
func TestConfirmDWMQuietFlushFailureNotConfirmed(t *testing.T) {
	origEnabled, origFlush := dwmCompositionEnabledFn, dwmFlushFn
	defer func() { dwmCompositionEnabledFn, dwmFlushFn = origEnabled, origFlush }()

	dwmCompositionEnabledFn = func() bool { return true }
	flushCalls := 0
	dwmFlushFn = func() error {
		flushCalls++
		return errors.New("DwmFlush failed: HRESULT 0x8000FFFF")
	}

	err := confirmDWMQuiet()
	if err == nil {
		t.Fatal("DwmFlush failure must not be reported as confirmed")
	}
	if flushCalls != 1 {
		t.Fatalf("flush loop must stop at first failure, got %d calls", flushCalls)
	}
	if !strings.Contains(err.Error(), "HRESULT 0x") {
		t.Fatalf("error must carry the real HRESULT, got: %v", err)
	}
}
