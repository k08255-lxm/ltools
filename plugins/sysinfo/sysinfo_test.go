package sysinfo

import (
	"sync"
	"testing"
	"time"
)

// shortenIntervals 缩短采集周期，使生命周期测试无需等待真实的 1s 采样窗口 / 2s tick。
func shortenIntervals(t *testing.T) {
	t.Helper()
	origRefresh, origTick, origWindow := refreshInterval, cpuSampleTick, cpuSampleWindow
	refreshInterval = 20 * time.Millisecond
	cpuSampleTick = 20 * time.Millisecond
	cpuSampleWindow = 10 * time.Millisecond
	t.Cleanup(func() {
		// 恢复全局间隔变量（循环 goroutine 已由各测试的 waitLoopsExit 保证退出，
		// 不会与恢复写入竞争）。
		refreshInterval, cpuSampleTick, cpuSampleWindow = origRefresh, origTick, origWindow
	})
}

func viewState(p *SysInfoPlugin) (refs int, running bool) {
	p.viewMutex.Lock()
	defer p.viewMutex.Unlock()
	return p.viewRefs, p.viewStop != nil
}

// waitLoopsExit 等待本插件所有采集循环 goroutine 真正退出
// （它们可能仍阻塞在一次真实系统调用中，stop 关闭后到 select 检查之间不可控）。
func waitLoopsExit(t *testing.T, p *SysInfoPlugin) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		p.loopsWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sampling loops did not exit within 5s")
	}
}

// TestViewLifecycleRefCounting 验证引用计数语义：
// 首个消费者进入时启动采集循环、最后一个离开时停止、多余的 Leave 不下溢。
func TestViewLifecycleRefCounting(t *testing.T) {
	shortenIntervals(t)
	p := NewSysInfoPlugin()

	if refs, running := viewState(p); refs != 0 || running {
		t.Fatalf("initial state: viewRefs=%d running=%v, want 0/false", refs, running)
	}

	if err := p.OnViewEnter(nil); err != nil {
		t.Fatalf("OnViewEnter: %v", err)
	}
	if err := p.OnViewEnter(nil); err != nil {
		t.Fatalf("OnViewEnter #2: %v", err)
	}
	if refs, running := viewState(p); refs != 2 || !running {
		t.Fatalf("after 2 enters: viewRefs=%d running=%v, want 2/true", refs, running)
	}

	// 一个消费者离开：仍有消费者，循环必须继续运行
	if err := p.OnViewLeave(nil); err != nil {
		t.Fatalf("OnViewLeave: %v", err)
	}
	if refs, running := viewState(p); refs != 1 || !running {
		t.Fatalf("after 1 leave: viewRefs=%d running=%v, want 1/true", refs, running)
	}

	// 最后一个消费者离开：循环停止
	if err := p.OnViewLeave(nil); err != nil {
		t.Fatalf("OnViewLeave #2: %v", err)
	}
	if refs, running := viewState(p); refs != 0 || running {
		t.Fatalf("after 2 leaves: viewRefs=%d running=%v, want 0/false", refs, running)
	}

	// 多余的 Leave：不得下溢、不得 panic
	if err := p.OnViewLeave(nil); err != nil {
		t.Fatalf("extra OnViewLeave: %v", err)
	}
	if refs, _ := viewState(p); refs != 0 {
		t.Fatalf("after extra leave: viewRefs=%d, want 0 (no underflow)", refs)
	}

	waitLoopsExit(t, p)
}

// TestLoopsExitWhenStopped 验证两个采集循环在 stop 关闭后有界退出
// （sampleCPUPeriodically 最多再等待一个采样窗口，refreshPeriodically 立即返回）。
func TestLoopsExitWhenStopped(t *testing.T) {
	shortenIntervals(t)
	p := NewSysInfoPlugin()

	stop := make(chan struct{})
	close(stop) // 已关闭 → 循环应立即（至多一个采样窗口内）返回

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.refreshPeriodically(stop)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refreshPeriodically did not exit after stop closed")
	}

	doneSample := make(chan struct{})
	go func() {
		defer close(doneSample)
		p.sampleCPUPeriodically(stop)
	}()
	select {
	case <-doneSample:
	case <-time.After(3 * time.Second):
		t.Fatal("sampleCPUPeriodically did not exit after stop closed")
	}
}

// TestServiceShutdownStopsLoops 验证 ServiceShutdown 清理运行状态：
// 循环标记清除、后续重复 shutdown / Leave 不会 panic（幂等）。
func TestServiceShutdownStopsLoops(t *testing.T) {
	shortenIntervals(t)
	p := NewSysInfoPlugin()

	if err := p.OnViewEnter(nil); err != nil {
		t.Fatalf("OnViewEnter: %v", err)
	}
	if _, running := viewState(p); !running {
		t.Fatal("expected sampling loops to be running after enter")
	}

	if err := p.ServiceShutdown(nil); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if _, running := viewState(p); running {
		t.Fatal("after ServiceShutdown: sampling loops still marked running")
	}

	// 幂等：再次 shutdown / 额外 Leave 不得 panic 或 double-close
	if err := p.ServiceShutdown(nil); err != nil {
		t.Fatalf("ServiceShutdown #2: %v", err)
	}
	if err := p.OnViewLeave(nil); err != nil {
		t.Fatalf("OnViewLeave after shutdown: %v", err)
	}

	waitLoopsExit(t, p)
}

// TestConcurrentEnterLeave 并发快速进出：引用计数串行保护下收敛正确、无数据竞争。
// 需以 -race 运行以检测竞争。
func TestConcurrentEnterLeave(t *testing.T) {
	shortenIntervals(t)
	p := NewSysInfoPlugin()

	const workers = 8
	const iters = 50
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if err := p.OnViewEnter(nil); err != nil {
					t.Errorf("OnViewEnter: %v", err)
					return
				}
				if err := p.OnViewLeave(nil); err != nil {
					t.Errorf("OnViewLeave: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if refs, running := viewState(p); refs != 0 || running {
		t.Fatalf("after concurrent enter/leave: viewRefs=%d running=%v, want 0/false", refs, running)
	}

	waitLoopsExit(t, p)
}

// TestSetEnabledPausesAndResumes 验证停用插件立即停止采集（保留消费者计数），
// 重新启用且仍有可见消费者时恢复采集。
func TestSetEnabledPausesAndResumes(t *testing.T) {
	shortenIntervals(t)
	p := NewSysInfoPlugin()

	if err := p.OnViewEnter(nil); err != nil {
		t.Fatalf("OnViewEnter: %v", err)
	}
	if refs, running := viewState(p); refs != 1 || !running {
		t.Fatalf("after enter: viewRefs=%d running=%v, want 1/true", refs, running)
	}

	if err := p.SetEnabled(false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if refs, running := viewState(p); refs != 1 || running {
		t.Fatalf("after disable: viewRefs=%d running=%v, want 1/false (refs kept)", refs, running)
	}

	if err := p.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	if refs, running := viewState(p); refs != 1 || !running {
		t.Fatalf("after re-enable: viewRefs=%d running=%v, want 1/true", refs, running)
	}

	// 收尾：停止循环避免泄漏到其他测试
	if err := p.OnViewLeave(nil); err != nil {
		t.Fatalf("OnViewLeave: %v", err)
	}
	if _, running := viewState(p); running {
		t.Fatal("after final leave: sampling loops still running")
	}

	waitLoopsExit(t, p)
}
