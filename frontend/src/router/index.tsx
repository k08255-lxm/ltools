import { lazy, Suspense } from 'react'
import { isWindowPath } from './routes/windowRoutes'

/**
 * 主应用与独立窗口的路由入口分离
 *
 * 两个路由根都是懒加载模块：
 * - 独立窗口路径（/search、/screenshot2-overlay、/pin-window、/sticky-window、
 *   /localtranslate-window、/music-player）只加载 windowRoutes 依赖树，
 *   不再静态引入 mainRoutes → MainLayout / Sidebar / ToastContext 等主应用依赖；
 * - 主窗口路径只加载 mainRoutes 依赖树，窗口组件 chunk 不会进入主窗口。
 *
 * 各 router 实例在对应模块（MainApp/WindowApp）内以模块级变量创建一次，
 * 组件重渲染不会重建 React Router 实例。
 */
const MainApp = lazy(() => import('./MainApp'))
const WindowApp = lazy(() => import('./WindowApp'))

/**
 * 路由组件
 * 根据当前路径决定加载主应用路由还是独立窗口路由
 * （独立窗口的 URL 在其生命周期内固定，无需响应式跟踪）
 */
export function AppRouter() {
  const App = isWindowPath(window.location.pathname) ? WindowApp : MainApp
  return (
    <Suspense fallback={<div className="h-screen w-screen bg-surface-0" />}>
      <App />
    </Suspense>
  )
}

// 导出类型和工具
export type { RouteConfig, PluginRouteConfig, NavItem, IconName } from './types'
export { registerPluginLifecycle } from './guards/pluginGuard'
