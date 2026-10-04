import { useState, useEffect, useCallback } from 'react';
import { Events } from '@wailsio/runtime';
import { PluginService, PluginMetadata, Permission } from '../../bindings/ltools/internal/plugins';

// 定义插件状态变化事件名称
export const PLUGIN_STATE_CHANGED_EVENT = 'plugin:state-changed';

/**
 * 模块级共享插件列表缓存与在途请求。
 *
 * - 同一窗口内的多个 usePlugins() 实例共享同一份缓存与在途请求：
 *   并发挂载/并发刷新时在途去重，只产生一次 PluginService.List() IPC；
 *   已有缓存时新实例挂载直接复用，不再重复拉取；
 * - 每个窗口是独立的 JS 上下文，模块级变量天然按窗口隔离；
 * - generation 用于 enable/disable 等变更后的强制失效：
 *   旧的在途请求返回时不再回写缓存，避免写回过期列表。
 */
let pluginsCache: PluginMetadata[] | null = null;
let pluginsInflight: Promise<PluginMetadata[]> | null = null;
let pluginsGeneration = 0;

/**
 * 拉取插件列表（共享缓存 + 在途去重）
 * @param force true 时使共享缓存与在途请求失效（enable/disable 等变更后使用）
 */
function fetchPlugins(force: boolean): Promise<PluginMetadata[]> {
  if (force) {
    pluginsGeneration++;
    pluginsCache = null;
    pluginsInflight = null;
  }
  if (pluginsInflight) {
    return pluginsInflight;
  }
  const generation = pluginsGeneration;
  const inflight = PluginService.List()
    .then((list) => {
      // Filter out null values
      const filtered = (list ?? []).filter((p): p is PluginMetadata => p !== null);
      if (generation === pluginsGeneration) {
        pluginsCache = filtered;
      }
      return filtered;
    })
    .finally(() => {
      if (pluginsInflight === inflight) {
        pluginsInflight = null;
      }
    });
  pluginsInflight = inflight;
  return inflight;
}

/**
 * 插件管理 Hook
 * 提供插件列表、加载、启用/禁用等功能
 */
export function usePlugins() {
  // 挂载时若共享缓存已有数据则直接采用，避免重复 IPC 与列表闪屏
  const [plugins, setPlugins] = useState<PluginMetadata[]>(() => pluginsCache ?? []);
  const [loading, setLoading] = useState(() => pluginsCache === null);
  const [error, setError] = useState<Error | null>(null);

  // 加载插件列表（在途去重：同一窗口内并发调用只产生一次 List IPC）
  const loadPlugins = useCallback(async () => {
    try {
      setLoading(true);
      const pluginList = await fetchPlugins(false);
      setPlugins(pluginList);
    } catch (err) {
      setError(err as Error);
    } finally {
      setLoading(false);
    }
  }, []);

  // 启用插件
  const enablePlugin = useCallback(async (id: string) => {
    try {
      await PluginService.Enable(id);
      // 强制失效共享缓存后重新拉取，保证 enable 后列表及时更新
      const pluginList = await fetchPlugins(true);
      setPlugins(pluginList);
      // 发送状态变化事件
      Events.Emit(PLUGIN_STATE_CHANGED_EVENT, { action: 'enable', pluginId: id });
    } catch (err) {
      setError(err as Error);
      throw err; // 重新抛出错误以便调用者处理
    }
  }, []);

  // 禁用插件
  const disablePlugin = useCallback(async (id: string) => {
    try {
      await PluginService.Disable(id);
      // 强制失效共享缓存后重新拉取，保证 disable 后列表及时更新
      const pluginList = await fetchPlugins(true);
      setPlugins(pluginList);
      // 发送状态变化事件
      Events.Emit(PLUGIN_STATE_CHANGED_EVENT, { action: 'disable', pluginId: id });
    } catch (err) {
      setError(err as Error);
      throw err; // 重新抛出错误以便调用者处理
    }
  }, []);

  // 搜索插件
  const searchPlugins = async (keywords: string[]) => {
    try {
      const results = await PluginService.Search(...keywords);
      return results;
    } catch (err) {
      setError(err as Error);
      return [];
    }
  };

  // 获取插件详情
  const getPlugin = async (id: string) => {
    try {
      return await PluginService.Get(id);
    } catch (err) {
      setError(err as Error);
      return null;
    }
  };

  // 检查权限
  const checkPermission = async (pluginId: string, permission: Permission) => {
    try {
      return await PluginService.CheckPermission(pluginId, permission);
    } catch (err) {
      setError(err as Error);
      return false;
    }
  };

  // 请求权限
  const requestPermission = async (
    pluginId: string,
    permission: Permission,
    granted: boolean
  ) => {
    try {
      await PluginService.RequestPermission(pluginId, permission, granted);
    } catch (err) {
      setError(err as Error);
    }
  };

  // 初始化时加载插件
  useEffect(() => {
    if (pluginsCache !== null) {
      // 共享缓存命中：直接采用，不发 List IPC（由事件/变更驱动后续刷新）
      setPlugins(pluginsCache);
      setLoading(false);
    } else {
      loadPlugins();
    }

    // 监听插件状态变化事件，当其他组件修改插件状态时同步更新
    const unsubscribe = Events.On(PLUGIN_STATE_CHANGED_EVENT, () => {
      loadPlugins();
    });

    return () => {
      unsubscribe?.();
    };
  }, [loadPlugins]);

  return {
    plugins,
    loading,
    error,
    loadPlugins,
    enablePlugin,
    disablePlugin,
    searchPlugins,
    getPlugin,
    checkPermission,
    requestPermission,
  };
}

/**
 * 单个插件 Hook
 * @param id 插件ID
 */
export function usePlugin(id: string) {
  const [plugin, setPlugin] = useState<PluginMetadata | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<Error | null>(null);

  useEffect(() => {
    const loadPlugin = async () => {
      try {
        setLoading(true);
        const data = await PluginService.Get(id);
        setPlugin(data);
      } catch (err) {
        setError(err as Error);
      } finally {
        setLoading(false);
      }
    };

    loadPlugin();
  }, [id]);

  return { plugin, loading, error };
}


/**
 * 日期时间插件 Hook
 * 专门用于日期时间插件的功能
 */
export function useDateTime() {
  const [currentTime, setCurrentTime] = useState<string>('');
  const [currentDate, setCurrentDate] = useState<string>('');
  const [weekday, setWeekday] = useState<string>('');

  // 监听日期时间事件
  useEffect(() => {
    const unsubTime = Events.On('datetime:time', (ev: { data: string }) => {
      setCurrentTime(ev.data);
    });

    const unsubDate = Events.On('datetime:date', (ev: { data: string }) => {
      setCurrentDate(ev.data);
    });

    const unsubDay = Events.On('datetime:weekday', (ev: { data: string }) => {
      setWeekday(ev.data);
    });

    return () => {
      unsubTime?.();
      unsubDate?.();
      unsubDay?.();
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return {
    currentTime,
    currentDate,
    weekday,
  };
}
