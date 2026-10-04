import { createBrowserRouter, RouterProvider } from 'react-router-dom'
import { windowRoutes } from './routes/windowRoutes'

/**
 * 独立窗口路由根
 *
 * router 实例为模块级单例：只在模块首次加载时创建一次，
 * 不会因组件重渲染（含 StrictMode 双渲染）而重建。
 * 本模块只依赖 windowRoutes（窗口组件本身仍是懒加载 chunk），
 * 主应用的 MainLayout / Sidebar 等依赖不会进入独立窗口的加载链。
 */
const router = createBrowserRouter(windowRoutes)

export default function WindowApp() {
  return <RouterProvider router={router} />
}
