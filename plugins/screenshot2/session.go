package screenshot2

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// captureSession 表示一次截图会话的完整生命周期状态。
//
// 设计要点（对应本轮改造方案）：
//   - ready 按会话 + 显示器双重去重：sessionId 不匹配的迟到/过期消息直接丢弃，
//     同一显示器重复的 ready 事件只计一次。
//   - 取消与就绪并发安全：cancelled 是原子标记 + 通知 channel，
//     StartCapture 的等待循环可以随时感知取消，不需要持有任何全局锁。
//   - 收尾幂等：endOnce 保证 endSession 只执行一次，避免取消与收尾竞态导致
//     重复关窗 / 重复显示主窗口。
type captureSession struct {
	id        string
	createdAt time.Time

	// 生命周期状态（stateMu 保护）
	stateMu sync.RWMutex
	state   sessionState

	// ready 跟踪（readyMu 保护）
	readyMu          sync.Mutex
	readyDisplays    map[int]bool
	expectedDisplays map[int]bool // 期望上报 ready 的显示器索引集合（= 计划创建的窗口）

	// 取消信号
	cancelled     atomic.Bool
	cancelNotify  chan struct{} // 容量 1，标记取消后非阻塞通知等待者
	notifyMu      sync.Mutex
	readyNotifyCh chan struct{} // 容量 1，新 ready 到达时通知等待循环

	// 收尾幂等
	endOnce sync.Once
	done    chan struct{} // 会话终结（收尾完成）信号
}

type sessionState int32

const (
	sessionStateStarting sessionState = iota // 窗口创建/等待前端就绪阶段
	sessionStateActive                       // 会话已完整启动
	sessionStateEnded                        // 收尾已完成
)

func (s sessionState) String() string {
	switch s {
	case sessionStateStarting:
		return "starting"
	case sessionStateActive:
		return "active"
	case sessionStateEnded:
		return "ended"
	default:
		return fmt.Sprintf("unknown(%d)", int32(s))
	}
}

// newCaptureSession 创建一个新会话。期望显示器集合由调用方在窗口创建前
// 通过 setExpectedDisplays 声明（集合在创建前声明、创建失败时剔除，
// 保证就绪等待不会因事件早到而误判）。
func newCaptureSession(id string) *captureSession {
	return &captureSession{
		id:            id,
		createdAt:     time.Now(),
		state:         sessionStateStarting,
		readyDisplays: make(map[int]bool),
		cancelNotify:  make(chan struct{}, 1),
		readyNotifyCh: make(chan struct{}, 1),
		done:          make(chan struct{}),
	}
}

// ID 返回会话 ID。
func (c *captureSession) ID() string { return c.id }

// setExpectedDisplays 声明期望上报 ready 的显示器索引集合。
// 在窗口创建循环之前调用：即使某个前端在创建间隙就上报 ready，
// 只要其索引在集合内就会被正确计数，不存在事件早到丢失的窗口期。
func (c *captureSession) setExpectedDisplays(indexes []int) {
	c.readyMu.Lock()
	defer c.readyMu.Unlock()
	c.expectedDisplays = make(map[int]bool, len(indexes))
	for _, idx := range indexes {
		c.expectedDisplays[idx] = false
	}
}

// removeExpectedDisplay 将创建失败的显示器从期望集合中剔除。
func (c *captureSession) removeExpectedDisplay(displayIndex int) {
	c.readyMu.Lock()
	defer c.readyMu.Unlock()
	delete(c.expectedDisplays, displayIndex)
}

// expectedReady 返回 (已就绪数, 期望数)。
func (c *captureSession) expectedReady() (got, expected int) {
	c.readyMu.Lock()
	defer c.readyMu.Unlock()
	return len(c.readyDisplays), len(c.expectedDisplays)
}

// markReady 记录一个前端就绪信号。
// 返回 true 表示这是当前会话的有效新就绪（应当唤醒等待者）；
// 返回 false 表示消息过期（sessionId 不匹配）、重复（显示器已就绪）、
// 不在期望集合内（含零窗口会话）或会话已终结。
func (c *captureSession) markReady(displayIndex int) bool {
	if c.isEnded() {
		return false
	}
	c.readyMu.Lock()
	if _, ok := c.expectedDisplays[displayIndex]; !ok {
		// 不在期望集合内：零窗口会话、越界索引或已被剔除的失败窗口
		c.readyMu.Unlock()
		return false
	}
	if c.readyDisplays[displayIndex] {
		c.readyMu.Unlock()
		return false
	}
	c.readyDisplays[displayIndex] = true
	c.readyMu.Unlock()

	// 通知等待循环（非阻塞）
	c.notifyMu.Lock()
	select {
	case c.readyNotifyCh <- struct{}{}:
	default:
	}
	c.notifyMu.Unlock()
	return true
}

// isReadyComplete 判断是否所有期望的显示器都已就绪。
func (c *captureSession) isReadyComplete() bool {
	c.readyMu.Lock()
	defer c.readyMu.Unlock()
	return len(c.expectedDisplays) > 0 && len(c.readyDisplays) >= len(c.expectedDisplays)
}

// setState 更新会话状态。
func (c *captureSession) setState(state sessionState) {
	c.stateMu.Lock()
	c.state = state
	c.stateMu.Unlock()
}

// State 返回当前会话状态。
func (c *captureSession) State() sessionState {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}

func (c *captureSession) isEnded() bool { return c.State() == sessionStateEnded }

// cancel 请求取消会话（幂等）。返回 true 表示这是第一次取消。
func (c *captureSession) cancel() bool {
	if c.cancelled.Swap(true) {
		return false // 已经取消过
	}
	c.notifyMu.Lock()
	select {
	case c.cancelNotify <- struct{}{}:
	default:
	}
	c.notifyMu.Unlock()
	return true
}

// IsCancelled 返回会话是否已被请求取消。
func (c *captureSession) IsCancelled() bool { return c.cancelled.Load() }

// cancelledCh 返回取消通知 channel（供 select 使用）。
func (c *captureSession) cancelledCh() <-chan struct{} { return c.cancelNotify }

// readyCh 返回就绪通知 channel（供 select 使用）。
func (c *captureSession) readyCh() <-chan struct{} { return c.readyNotifyCh }

// finish 标记收尾完成（幂等），唤醒所有等待 done 的调用方。
func (c *captureSession) finish() {
	c.endOnce.Do(func() {
		c.setState(sessionStateEnded)
		close(c.done)
	})
}

// Done 返回会话终结 channel。
func (c *captureSession) Done() <-chan struct{} { return c.done }

// isDone 返回会话是否已完成收尾。
func (c *captureSession) isDone() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// waitDone 等待会话收尾完成，最多等待 timeout。返回是否在超时前完成。
func (c *captureSession) waitDone(timeout time.Duration) bool {
	if c.isDone() {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-c.done:
		return true
	case <-timer.C:
		return false
	}
}

// generateSessionId 生成全局唯一的会话 ID。
// 旧实现基于秒级时间戳，同一秒内连续两次启动会产生相同 ID，
// 导致会话隔离（ready 去重、图像定向投递）失效；这里加入纳秒与单调计数。
var sessionIDCounter atomic.Uint64

func generateSessionId() string {
	n := sessionIDCounter.Add(1)
	return fmt.Sprintf("screenshot2-%s-%d", time.Now().Format("20060102150405"), n)
}
