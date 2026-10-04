package screenshot2

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 会话 ID ----

func TestGenerateSessionIdUnique(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := generateSessionId()
		if seen[id] {
			t.Fatalf("duplicate session id generated: %s", id)
		}
		seen[id] = true
		if !strings.HasPrefix(id, "screenshot2-") {
			t.Fatalf("unexpected session id format: %s", id)
		}
	}
}

// ---- markReady：会话 + 显示器双重去重与过期过滤 ----

func TestMarkReadyDeduplicatesPerDisplay(t *testing.T) {
	s := newCaptureSession("sess-1")
	s.setExpectedDisplays([]int{0, 1})

	if !s.markReady(0) {
		t.Fatal("first ready for display 0 should be accepted")
	}
	if s.markReady(0) {
		t.Fatal("duplicate ready for display 0 must be rejected")
	}
	if !s.markReady(1) {
		t.Fatal("first ready for display 1 should be accepted")
	}
	if got, expected := s.expectedReady(); got != 2 || expected != 2 {
		t.Fatalf("expected (2,2), got (%d,%d)", got, expected)
	}
	if !s.isReadyComplete() {
		t.Fatal("session should be complete after all displays ready")
	}
}

func TestMarkReadyRejectsBeforeExpectedSet(t *testing.T) {
	s := newCaptureSession("sess-1")
	// 期望集合未声明（窗口尚未创建）：任何 ready 都视为过期/无效
	if s.markReady(0) {
		t.Fatal("ready before expected displays are declared must be rejected")
	}
	if s.isReadyComplete() {
		t.Fatal("zero-window session must never be complete")
	}
}

func TestMarkReadyRejectsOutOfRangeDisplay(t *testing.T) {
	s := newCaptureSession("sess-1")
	s.setExpectedDisplays([]int{0, 1})
	if s.markReady(2) {
		t.Fatal("out-of-range display index must be rejected")
	}
	if s.markReady(-1) {
		t.Fatal("negative display index must be rejected")
	}
}

func TestRemoveExpectedDisplay(t *testing.T) {
	s := newCaptureSession("sess-1")
	s.setExpectedDisplays([]int{0, 1})
	s.removeExpectedDisplay(1) // 模拟窗口 1 创建失败

	if s.markReady(1) {
		t.Fatal("removed display ready must be rejected")
	}
	if !s.markReady(0) {
		t.Fatal("remaining display ready should be accepted")
	}
	// 期望已剔除：单显示器就绪即完成
	if got, expected := s.expectedReady(); got != 1 || expected != 1 {
		t.Fatalf("expected (1,1), got (%d,%d)", got, expected)
	}
	if !s.isReadyComplete() {
		t.Fatal("session should be complete after remaining display ready")
	}
}

func TestMarkReadyAcceptedBeforeWindowCreationCompletes(t *testing.T) {
	// 回归保护：期望集合在窗口创建前声明 —— 早到的 ready 不丢失
	s := newCaptureSession("sess-1")
	s.setExpectedDisplays([]int{0})
	if !s.markReady(0) {
		t.Fatal("ready arriving between declaration and window creation must be accepted")
	}
	if !s.isReadyComplete() {
		t.Fatal("session should be complete")
	}
}

func TestMarkReadyRejectedAfterEnd(t *testing.T) {
	s := newCaptureSession("sess-1")
	s.setExpectedDisplays([]int{0})
	s.finish()
	if s.markReady(0) {
		t.Fatal("ready after session end must be rejected")
	}
}

// ---- 取消 ----

func TestCancelIdempotentAndNotified(t *testing.T) {
	s := newCaptureSession("sess-1")
	s.setExpectedDisplays([]int{0})

	if !s.cancel() {
		t.Fatal("first cancel should return true")
	}
	if s.cancel() {
		t.Fatal("second cancel should return false (idempotent)")
	}
	if !s.IsCancelled() {
		t.Fatal("session should be marked cancelled")
	}

	select {
	case <-s.cancelledCh():
	default:
		t.Fatal("cancel notification channel should be signalled")
	}
}

func TestFinishIdempotent(t *testing.T) {
	s := newCaptureSession("sess-1")
	s.finish()
	s.finish() // 第二次 finish 不得 panic（endOnce 保护）
	s.finish()

	select {
	case <-s.Done():
	default:
		t.Fatal("done channel should be closed after finish")
	}
	if s.State() != sessionStateEnded {
		t.Fatalf("state should be ended, got %s", s.State())
	}
}

func TestWaitDoneReturns(t *testing.T) {
	s := newCaptureSession("sess-1")
	if s.waitDone(50 * time.Millisecond) {
		t.Fatal("waitDone should time out for unfinished session")
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		s.finish()
	}()
	if !s.waitDone(2 * time.Second) {
		t.Fatal("waitDone should return true after finish")
	}
}

// ---- waitForFrontendReady（WindowManager 方法，仅依赖 session）----

func TestWaitForFrontendReadyCompletes(t *testing.T) {
	m := &WindowManager{}
	s := newCaptureSession("sess-1")
	s.setExpectedDisplays([]int{0, 1})

	go func() {
		time.Sleep(10 * time.Millisecond)
		s.markReady(0)
		time.Sleep(10 * time.Millisecond)
		s.markReady(1)
	}()

	got, expected, cancelled := m.waitForFrontendReady(s, 2*time.Second)
	if cancelled {
		t.Fatal("should not be cancelled")
	}
	if got != 2 || expected != 2 {
		t.Fatalf("expected (2,2), got (%d,%d)", got, expected)
	}
}

func TestWaitForFrontendReadyTimeoutOnPartial(t *testing.T) {
	m := &WindowManager{}
	s := newCaptureSession("sess-1")
	s.setExpectedDisplays([]int{0, 1})
	s.markReady(0) // 只有一个显示器就绪

	got, expected, cancelled := m.waitForFrontendReady(s, 100*time.Millisecond)
	if cancelled {
		t.Fatal("timeout is not cancellation")
	}
	if got != 1 || expected != 2 {
		t.Fatalf("expected (1,2), got (%d,%d)", got, expected)
	}
}

func TestWaitForFrontendReadyRespondsToCancel(t *testing.T) {
	m := &WindowManager{}
	s := newCaptureSession("sess-1")
	s.setExpectedDisplays([]int{0, 1})

	go func() {
		time.Sleep(20 * time.Millisecond)
		s.cancel()
	}()

	_, _, cancelled := m.waitForFrontendReady(s, 5*time.Second)
	if !cancelled {
		t.Fatal("waitForFrontendReady should report cancellation")
	}
}

func TestWaitForFrontendReadyZeroWindowNeverCompletes(t *testing.T) {
	m := &WindowManager{}
	s := newCaptureSession("sess-1")
	// expected=0（未设置）：不得误判为完成
	got, expected, _ := m.waitForFrontendReady(s, 100*time.Millisecond)
	if got != 0 || expected != 0 {
		t.Fatalf("zero-window session should report (0,0), got (%d,%d)", got, expected)
	}
	if s.isReadyComplete() {
		t.Fatal("zero-window session must not be considered complete")
	}
}

// ---- 并发安全（-race 下运行）----

func TestMarkReadyConcurrent(t *testing.T) {
	s := newCaptureSession("sess-1")
	const displays = 8
	indexes := make([]int, displays)
	for i := range indexes {
		indexes[i] = i
	}
	s.setExpectedDisplays(indexes)

	var wg sync.WaitGroup
	accepted := make([]bool, displays)
	var mu sync.Mutex
	for i := 0; i < displays; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ok := s.markReady(idx)
			// 重复投递：每个索引投两次，只有一次应被接受
			s.markReady(idx)
			mu.Lock()
			accepted[idx] = ok
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	for i, ok := range accepted {
		if !ok {
			t.Errorf("display %d ready should have been accepted exactly once", i)
		}
	}
	if !s.isReadyComplete() {
		t.Fatal("session should be complete")
	}
}
