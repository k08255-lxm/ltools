import { useEffect } from 'react'
import { useParams } from 'react-router-dom'
import * as ProcessManagerService from '../../../bindings/ltools/plugins/processmanager/processmanagerservice'
import * as SysInfoService from '../../../bindings/ltools/plugins/sysinfo/sysinfoservice'
import * as PluginService from '../../../bindings/ltools/internal/plugins/pluginservice'
import { useMainWindowVisible } from '../../hooks/useMainWindowVisible'
import type { PluginLifecycleHandler } from '../types'

/**
 * 插件生命周期处理映射
 * 每个插件可以注册自己的进入/离开处理函数
 */
const pluginEnterHandlers: Record<string, PluginLifecycleHandler> = {
  'processmanager.builtin': async () => {
    await ProcessManagerService.EnterView()
  },
  'sysinfo.builtin': async () => {
    await SysInfoService.EnterView()
  },
  // 未来可扩展其他插件
}

const pluginLeaveHandlers: Record<string, PluginLifecycleHandler> = {
  'processmanager.builtin': async () => {
    await ProcessManagerService.LeaveView()
  },
  'sysinfo.builtin': async () => {
    await SysInfoService.LeaveView()
  },
  // 未来可扩展其他插件
}

/**
 * 插件守卫组件
 * 统一管理插件的进入/离开生命周期
 */
export function PluginGuard({ children }: { children: React.ReactNode }) {
  const { pluginId } = useParams<{ pluginId: string }>()
  // 页面挂载 ≠ 窗口可见：主窗口 Close 到托盘后本组件仍挂载。
  // 生命周期由 (pluginId × 窗口可见性) 组合驱动 —— 窗口隐藏即 Leave
  // （暂停该插件的后台采集），重显即 Enter（恢复），避免对不可见窗口空转。
  const mainVisible = useMainWindowVisible()

  // 记录使用（fire-and-forget，仅插件切换时记录一次）
  useEffect(() => {
    if (pluginId) {
      PluginService.RecordUsage(pluginId).catch(err => {
        console.error(`Failed to record usage for ${pluginId}:`, err)
      })
    }
  }, [pluginId])

  // 进入/离开由 (pluginId, mainVisible) 组合驱动：
  // - 导航离开插件页（pluginId 变化/清除）→ cleanup Leave 旧插件
  // - 窗口隐藏 → cleanup Leave 当前插件
  // - 窗口重显 → Enter 当前插件
  // disposed/entered 串接：Enter 尚未完成即切换/卸载、Enter 失败、
  // StrictMode mount-cleanup-mount——cleanup 只释放本 effect 成功取得的
  // 一次订阅，不漏释放也不重复减计数（后端另有下限保护兜底）。
  useEffect(() => {
    if (!pluginId || !mainVisible) return
    const enter = pluginEnterHandlers[pluginId]
    const leave = pluginLeaveHandlers[pluginId]
    if (!enter || !leave) return

    let disposed = false
    let entered = false
    enter(pluginId)
      .then(() => {
        if (disposed) {
          leave(pluginId).catch(() => {})
        } else {
          entered = true
        }
      })
      .catch(() => {})

    return () => {
      disposed = true
      if (entered) {
        entered = false
        leave(pluginId).catch(() => {})
      }
    }
  }, [pluginId, mainVisible])

  return <>{children}</>
}

/**
 * 注册插件生命周期处理函数
 */
export function registerPluginLifecycle(
  pluginId: string,
  onEnter?: PluginLifecycleHandler,
  onLeave?: PluginLifecycleHandler
) {
  if (onEnter) {
    pluginEnterHandlers[pluginId] = onEnter
  }
  if (onLeave) {
    pluginLeaveHandlers[pluginId] = onLeave
  }
}
