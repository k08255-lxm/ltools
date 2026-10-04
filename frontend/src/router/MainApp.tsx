import { createBrowserRouter, RouterProvider } from 'react-router-dom'
import { mainRoutes } from './routes/mainRoutes'

/**
 * 主应用路由根
 *
 * router 实例为模块级单例：只在模块首次加载时创建一次，
 * 不会因组件重渲染（含 StrictMode 双渲染）而重建。
 * MainLayout 及其依赖树（Sidebar、ToastContext、全局快捷键等）
 * 仅在本模块被加载时才会进入打包/加载链，独立窗口不会加载它们。
 */
const router = createBrowserRouter(mainRoutes)

export default function MainApp() {
  return <RouterProvider router={router} />
}
