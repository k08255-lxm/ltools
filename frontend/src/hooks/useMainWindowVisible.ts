import { useEffect, useState } from 'react'
import { Events } from '@wailsio/runtime'

/**
 * 主窗口可见性订阅。
 *
 * 页面挂载 ≠ 窗口可见：主窗口 Close 到托盘（WindowClosing hook → Hide）
 * 或截图会话隐藏主窗口后，React 树仍保持挂载，仅靠 mount/unmount 无法
 * 感知窗口已不可见。本 hook 让消费方（如 sysinfo 后台采集的门控）在
 * 窗口隐藏时暂停、重显时恢复。
 *
 * 事件来源（后端 main.go，per-window 钩子只捕获主窗口自身）：
 *   - windows:WindowShow/Hide（WM_SHOWWINDOW，wails v3 webview_window_windows.go）
 *   - mac:WindowShow/Hide
 * 后端转发为专用事件 "main-window:visibility"，携带 bool。
 * 全局窗口事件不携带窗口名（CustomEvent.Sender 为空），搜索窗口、
 * 截图覆盖层等其他窗口的显示/隐藏不会触发本 hook。
 *
 * 初始值 true：主窗口启动即显示；前端未就绪期间的早期事件天然丢失，
 * 由初始值兜底。mock/纯浏览器模式无事件 → 保持 true（行为与现状一致）。
 */
export function useMainWindowVisible(): boolean {
  const [visible, setVisible] = useState(true)

  useEffect(() => {
    const off = Events.On('main-window:visibility', (ev: unknown) => {
      const data = (ev as { data?: unknown }).data
      setVisible(!!data)
    })
    return () => {
      if (off && typeof off === 'function') off()
    }
  }, [])

  return visible
}
