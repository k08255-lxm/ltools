import { useEffect, useState } from 'react'
import { Events, Window } from '@wailsio/runtime'
import {
  CopyIcon,
  Cross2Icon,
  DashIcon,
  SquareIcon,
} from '@radix-ui/react-icons'

/**
 * 读取 Wails 注入的环境信息(生产环境在任何模块执行前由 runtime 脚本注入)。
 * mock/纯浏览器模式下不存在,返回 undefined。
 */
export function getWailsOS(): string | undefined {
  const env = (window as unknown as { _wails?: { environment?: { OS?: string } } })._wails
    ?.environment
  return env?.OS
}

/**
 * 是否显示自绘窗口控件:
 * - Windows 主窗口为 Frameless,无系统标题栏,需要自绘控件;
 * - mock(纯浏览器)模式下环境未注入(undefined),同样显示,便于 UI 验收;
 * - macOS 使用原生交通灯、Linux 保留系统标题栏,均不显示。
 */
export function shouldShowWindowControls(): boolean {
  const os = getWailsOS()
  return os === undefined || os === 'windows'
}

/** 最小化 — 顶部横线(@radix-ui/react-icons) */
function MinimiseIcon() {
  return <DashIcon width={21} height={11} preserveAspectRatio="none" aria-hidden="true" />
}

/** 最大化 — 方框(@radix-ui/react-icons) */
function MaximiseIcon() {
  return <SquareIcon width={10} height={10} aria-hidden="true" />
}

/** 还原 — 前后两个方框(@radix-ui/react-icons) */
function RestoreIcon() {
  return <CopyIcon width={10} height={10} aria-hidden="true" />
}

/** 关闭 — 对角叉(@radix-ui/react-icons) */
function CloseIcon() {
  return <Cross2Icon width={12} height={12} aria-hidden="true" />
}

/**
 * Windows 风格的自绘窗口控件(最小化 / 最大化-还原 / 关闭)。
 *
 * - 关闭走 Window.Close():与原生标题栏一致,由后端 WindowClosing hook
 *   隐藏到托盘(main.go),绝不调用 Quit;
 * - 最大化状态通过 windows:WindowMaximise / windows:WindowRestore 事件跟随,
 *   初始状态取自 Window.IsMaximised()(mock 模式下静默失败,保持默认);
 * - 容器声明 --wails-draggable: no-drag,点击不会触发窗口拖动。
 */
export function WindowControls() {
  const [maximised, setMaximised] = useState(false)

  useEffect(() => {
    let disposed = false

    Window.IsMaximised()
      .then((v) => {
        if (!disposed) setMaximised(!!v)
      })
      .catch(() => {
        /* mock 模式或调用失败:保持默认状态 */
      })

    const offs = [
      Events.On('windows:WindowMaximise', () => setMaximised(true)),
      Events.On('windows:WindowRestore', () => setMaximised(false)),
    ]

    return () => {
      disposed = true
      for (const off of offs) off()
    }
  }, [])

  const handleMinimise = () => {
    Window.Minimise().catch((err) => console.error('[WindowControls] Minimise failed:', err))
  }

  const handleToggleMaximise = () => {
    Window.ToggleMaximise().catch((err) =>
      console.error('[WindowControls] ToggleMaximise failed:', err),
    )
  }

  const handleClose = () => {
    // 正常 Close:后端 WindowClosing hook 会隐藏窗口到托盘,而不是退出应用。
    Window.Close().catch((err) => console.error('[WindowControls] Close failed:', err))
  }

  return (
    <div
      className="win-controls"
      style={{ '--wails-draggable': 'no-drag' } as React.CSSProperties}
      role="group"
      aria-label="窗口控制"
      // 双击按钮不冒泡到标题栏的 onDoubleClick(避免连击控件时误切换最大化)
      onDoubleClick={(e) => e.stopPropagation()}
    >
      <button
        type="button"
        className="win-control-btn"
        onClick={handleMinimise}
        title="最小化"
        aria-label="最小化"
      >
        <MinimiseIcon />
      </button>
      <button
        type="button"
        className="win-control-btn"
        onClick={handleToggleMaximise}
        title={maximised ? '向下还原' : '最大化'}
        aria-label={maximised ? '向下还原' : '最大化'}
      >
        {maximised ? <RestoreIcon /> : <MaximiseIcon />}
      </button>
      <button
        type="button"
        className="win-control-btn win-control-close"
        onClick={handleClose}
        title="关闭"
        aria-label="关闭"
      >
        <CloseIcon />
      </button>
    </div>
  )
}
