package sysinfo

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/wailsapp/wails/v3/pkg/application"
	"ltools/internal/plugins"
)

const (
	PluginID      = "sysinfo.builtin"
	PluginName    = "系统信息"
	PluginVersion = "1.0.0"
)

// 后台采集节奏（与原实现保持一致，不降低更新频率）。
// 以包级变量声明，测试可临时缩短周期验证生命周期（生产代码不修改）。
var (
	refreshInterval = 2 * time.Second // 系统信息快照刷新间隔
	cpuSampleTick   = 2 * time.Second // CPU 采样 tick 间隔
	cpuSampleWindow = 1 * time.Second // 单次 CPU 采样窗口
)

// SystemInfo contains system information
type SystemInfo struct {
	// OS Info
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Hostname    string `json:"hostname"`
	Platform    string `json:"platform"`        // e.g., "darwin", "ubuntu"
	PlatformVer string `json:"platformVersion"` // OS version
	KernelVer   string `json:"kernelVersion"`   // Kernel version
	KernelArch  string `json:"kernelArch"`      // Kernel architecture

	// CPU Info
	CPUs         int     `json:"cpus"`
	CPUModelName string  `json:"cpuModelName"`
	CPUUsage     float64 `json:"cpuUsage"` // CPU usage percentage

	// Memory Info (system-wide)
	MemoryUsed        string  `json:"memoryUsed"`
	MemoryTotal       string  `json:"memoryTotal"`
	MemoryFree        string  `json:"memoryFree"`
	MemoryUsedPercent float64 `json:"memoryUsedPercent"`

	// Swap Info
	SwapTotal       string  `json:"swapTotal"`
	SwapUsed        string  `json:"swapUsed"`
	SwapFree        string  `json:"swapFree"`
	SwapUsedPercent float64 `json:"swapUsedPercent"`

	// Load Average
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`

	// Disk Info
	DiskTotal       string  `json:"diskTotal"`
	DiskUsed        string  `json:"diskUsed"`
	DiskFree        string  `json:"diskFree"`
	DiskUsedPercent float64 `json:"diskUsedPercent"`

	// Network Info
	NetInterfaces []NetInterface `json:"netInterfaces"`

	// Go Runtime Info
	GoVersion  string `json:"goVersion"`
	GoMaxProcs int    `json:"goMaxProcs"`
	GoUptime   string `json:"goUptime"`

	// Host Info
	HostUptime string `json:"hostUptime"`
	BootTime   int64  `json:"bootTime"`

	// Process Info
	ProcCount int `json:"procCount"`

	Timestamp int64 `json:"timestamp"` // Unix timestamp in milliseconds
}

// NetInterface represents a network interface
type NetInterface struct {
	Name         string   `json:"name"`
	HardwareAddr string   `json:"hardwareAddr"`
	Flags        []string `json:"flags"`
	Addrs        []string `json:"addrs"`
	BytesSent    uint64   `json:"bytesSent"`
	BytesRecv    uint64   `json:"bytesRecv"`
	PacketsSent  uint64   `json:"packetsSent"`
	PacketsRecv  uint64   `json:"packetsRecv"`
}

// ProcessInfo contains information about a process
type ProcessInfo struct {
	PID       int       `json:"pid"`
	Name      string    `json:"name"`
	CPU       float64   `json:"cpu"`
	Memory    string    `json:"memory"`
	Status    string    `json:"status"`
	StartTime time.Time `json:"startTime"`
}

// SysInfoPlugin provides system information functionality
type SysInfoPlugin struct {
	*plugins.BasePlugin
	app           *application.App
	startTime     time.Time
	lastRefresh   time.Time
	cachedInfo    *SystemInfo
	cacheMu       sync.Mutex     // Guards cachedInfo/lastRefresh (written by concurrent snapshots)
	cpuUsage      float64        // Cached CPU usage percentage
	cpuUsageMutex sync.RWMutex   // Mutex for CPU usage
	viewMutex     sync.Mutex     // Serializes view enter/leave and enable toggles
	viewRefs      int            // Number of active consumers (sysinfo plugin page, Home dashboard card)
	viewStop      chan struct{}  // Non-nil while background sampling loops are running
	loopsWG       sync.WaitGroup // Tracks background sampling loop goroutines (test/shutdown observability)
}

// NewSysInfoPlugin creates a new system info plugin
func NewSysInfoPlugin() *SysInfoPlugin {
	metadata := &plugins.PluginMetadata{
		ID:          PluginID,
		Name:        PluginName,
		Version:     PluginVersion,
		Author:      "LTools Team",
		Description: "查看系统信息，包括操作系统、CPU、内存等",
		Icon:        "info",
		Type:        plugins.PluginTypeBuiltIn,
		State:       plugins.PluginStateInstalled,
		Permissions: []plugins.Permission{
			plugins.PermissionProcess,
		},
		Keywords: []string{"系统", "信息", "CPU", "内存", "system", "info", "cpu", "memory"},
	}

	base := plugins.NewBasePlugin(metadata)
	return &SysInfoPlugin{
		BasePlugin: base,
		startTime:  time.Now(),
	}
}

// Init initializes the plugin
func (p *SysInfoPlugin) Init(app *application.App) error {
	if err := p.BasePlugin.Init(app); err != nil {
		return err
	}
	p.app = app
	return nil
}

// ServiceStartup is called when the application starts
func (p *SysInfoPlugin) ServiceStartup(app *application.App) error {
	if err := p.BasePlugin.ServiceStartup(app); err != nil {
		return err
	}

	// 后台 CPU 采样与周期快照不再随服务启动常驻：
	// 改为引用计数驱动的按需采集（plugins.ViewLifecycle）——
	// 首个可见消费者（sysinfo 插件页 / Home 仪表盘卡）OnViewEnter 时启动，
	// 最后一个消费者 OnViewLeave 或插件停用 / 服务关闭时有界退出。
	return nil
}

// ServiceShutdown is called when the application shuts down
func (p *SysInfoPlugin) ServiceShutdown(app *application.App) error {
	// Stop background sampling loops if still running (bounded exit)
	p.pauseViewLoops()

	return p.BasePlugin.ServiceShutdown(app)
}

// Enabled returns true if the plugin is enabled
func (p *SysInfoPlugin) Enabled() bool {
	return p.BasePlugin.Enabled()
}

// SetEnabled enables or disables the plugin
func (p *SysInfoPlugin) SetEnabled(enabled bool) error {
	if err := p.BasePlugin.SetEnabled(enabled); err != nil {
		return err
	}
	if !enabled {
		// 停用插件时立即停止后台采集（保留已注册的消费者计数，
		// 重新启用且仍有可见消费者时由 ensureViewLoops 恢复）
		p.pauseViewLoops()
	} else {
		p.ensureViewLoops()
	}
	return nil
}

// OnViewEnter registers a visible sysinfo consumer (implements plugins.ViewLifecycle).
// Background sampling starts when the first consumer enters (ref count 0 → 1).
func (p *SysInfoPlugin) OnViewEnter(app *application.App) error {
	p.viewMutex.Lock()
	p.viewRefs++
	p.viewMutex.Unlock()

	p.ensureViewLoops()
	return nil
}

// OnViewLeave removes a previously registered consumer (implements plugins.ViewLifecycle).
// Background sampling stops when the last consumer leaves. Idempotent against underflow.
func (p *SysInfoPlugin) OnViewLeave(app *application.App) error {
	p.viewMutex.Lock()
	if p.viewRefs > 0 {
		p.viewRefs--
	}
	empty := p.viewRefs == 0
	p.viewMutex.Unlock()

	if empty {
		p.pauseViewLoops()
	}
	return nil
}

// ensureViewLoops starts the background sampling loops when consumers are
// registered and the loops are not already running. Serialized via viewMutex.
func (p *SysInfoPlugin) ensureViewLoops() {
	p.viewMutex.Lock()
	needStart := p.viewRefs > 0 && p.viewStop == nil
	var stop chan struct{}
	if needStart {
		stop = make(chan struct{})
		p.viewStop = stop
	}
	p.viewMutex.Unlock()

	if needStart {
		// CPU 采样（含约 1 秒的初始化窗口）在后台 goroutine 中进行，
		// 不阻塞 EnterView 的 UI 调用路径。
		p.loopsWG.Add(2)
		go func() {
			defer p.loopsWG.Done()
			p.sampleCPUPeriodically(stop)
		}()
		go func() {
			defer p.loopsWG.Done()
			p.refreshPeriodically(stop)
		}()
	}
}

// pauseViewLoops stops the background sampling loops if running.
// Does not forget registered consumers (used by OnViewLeave/SetEnabled(false)/shutdown).
func (p *SysInfoPlugin) pauseViewLoops() {
	p.viewMutex.Lock()
	stop := p.viewStop
	p.viewStop = nil
	p.viewMutex.Unlock()

	if stop != nil {
		close(stop)
	}
}

// publishSnapshot refreshes the cached system info and notifies consumers.
func (p *SysInfoPlugin) publishSnapshot() {
	info := p.GetSystemInfo()
	p.emitEvent("updated", fmt.Sprintf("%d", info.Timestamp))
}

// Helper method to emit events
func (p *SysInfoPlugin) emitEvent(eventName, data string) {
	if p.app != nil {
		p.app.Event.Emit("sysinfo:"+eventName, data)
	}
}

// sampleCPUPeriodically samples CPU usage in the background while sysinfo
// consumers are visible. Exits promptly (bounded) when stop is closed.
func (p *SysInfoPlugin) sampleCPUPeriodically(stop <-chan struct{}) {
	ticker := time.NewTicker(cpuSampleTick)
	defer ticker.Stop()

	// The first cpu.Percent call both initializes the calculation and spans a
	// real sample window, so warm the cache with its result: consumers get a
	// real CPU value ~1s after entering instead of waiting a full tick.
	if cpuPercent, err := cpu.Percent(cpuSampleWindow, false); err == nil && len(cpuPercent) > 0 {
		p.cpuUsageMutex.Lock()
		p.cpuUsage = cpuPercent[0]
		p.cpuUsageMutex.Unlock()
	}
	if p.Enabled() {
		p.publishSnapshot()
	}

	for {
		select {
		case <-ticker.C:
			if p.Enabled() {
				// Get CPU usage (this will block for ~1 second)
				if cpuPercent, err := cpu.Percent(cpuSampleWindow, false); err == nil && len(cpuPercent) > 0 {
					p.cpuUsageMutex.Lock()
					p.cpuUsage = cpuPercent[0]
					p.cpuUsageMutex.Unlock()
				}
			}
		case <-stop:
			return
		}
	}
}

// getCachedCPUUsage returns the cached CPU usage value
func (p *SysInfoPlugin) getCachedCPUUsage() float64 {
	p.cpuUsageMutex.RLock()
	defer p.cpuUsageMutex.RUnlock()
	return p.cpuUsage
}

// refreshPeriodically refreshes system info periodically while sysinfo
// consumers are visible. Exits promptly (bounded) when stop is closed.
//
// 每个周期只发布一个 "updated" 事件：前端唯一的刷新监听者
// （SystemInfoWidget / Home 卡片）都在收到该事件后自行全量拉取，
// 原先额外发布的 "cpu"/"uptime" 事件无独立消费者，只会造成重复请求。
func (p *SysInfoPlugin) refreshPeriodically(stop <-chan struct{}) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if p.Enabled() {
				p.publishSnapshot()
			}
		case <-stop:
			return
		}
	}
}

// GetSystemInfo returns current system information using gopsutil
func (p *SysInfoPlugin) GetSystemInfo() *SystemInfo {
	now := time.Now()
	info := &SystemInfo{
		Timestamp:  now.UnixMilli(), // Unix timestamp in milliseconds
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		GoVersion:  runtime.Version(),
		GoMaxProcs: runtime.GOMAXPROCS(0),
		GoUptime:   formatDuration(time.Since(p.startTime)),
	}

	// Get host info
	if hostInfo, err := host.Info(); err == nil {
		info.Hostname = hostInfo.Hostname
		info.Platform = hostInfo.Platform
		info.PlatformVer = hostInfo.PlatformVersion
		info.KernelVer = hostInfo.KernelVersion
		info.KernelArch = hostInfo.KernelArch
		info.BootTime = int64(hostInfo.BootTime)
		info.HostUptime = formatDuration(time.Duration(hostInfo.Uptime) * time.Second)
		info.ProcCount = int(hostInfo.Procs)
	}

	// Get CPU info
	if cpuInfo, err := cpu.Info(); err == nil && len(cpuInfo) > 0 {
		info.CPUs = runtime.NumCPU()
		info.CPUModelName = cpuInfo[0].ModelName
	}

	// Get CPU usage percentage from cache (non-blocking)
	info.CPUUsage = p.getCachedCPUUsage()

	// Get memory info
	if memInfo, err := mem.VirtualMemory(); err == nil {
		info.MemoryTotal = formatBytes(memInfo.Total)
		info.MemoryUsed = formatBytes(memInfo.Used)
		info.MemoryFree = formatBytes(memInfo.Free)
		info.MemoryUsedPercent = memInfo.UsedPercent
	}

	// Get swap info
	if swapInfo, err := mem.SwapMemory(); err == nil {
		info.SwapTotal = formatBytes(swapInfo.Total)
		info.SwapUsed = formatBytes(swapInfo.Used)
		info.SwapFree = formatBytes(swapInfo.Free)
		info.SwapUsedPercent = swapInfo.UsedPercent
	}

	// Get load average (Unix-like systems only)
	if loadInfo, err := load.Avg(); err == nil {
		info.Load1 = loadInfo.Load1
		info.Load5 = loadInfo.Load5
		info.Load15 = loadInfo.Load15
	}

	// Get disk info for root partition
	if diskInfo, err := disk.Usage("/"); err == nil {
		info.DiskTotal = formatBytes(diskInfo.Total)
		info.DiskUsed = formatBytes(diskInfo.Used)
		info.DiskFree = formatBytes(diskInfo.Free)
		info.DiskUsedPercent = diskInfo.UsedPercent
	}

	// Get network interfaces
	if netInterfaces, err := net.Interfaces(); err == nil {
		info.NetInterfaces = make([]NetInterface, 0, len(netInterfaces))
		for _, ni := range netInterfaces {
			iface := NetInterface{
				Name:         ni.Name,
				HardwareAddr: ni.HardwareAddr,
				Flags:        ni.Flags,
				Addrs:        make([]string, 0, len(ni.Addrs)),
			}
			for _, addr := range ni.Addrs {
				iface.Addrs = append(iface.Addrs, addr.Addr)
			}
			info.NetInterfaces = append(info.NetInterfaces, iface)
		}
	}

	// Get network I/O counters
	if netCounters, err := net.IOCounters(true); err == nil {
		counterMap := make(map[string]net.IOCountersStat)
		for _, counter := range netCounters {
			counterMap[counter.Name] = counter
		}
		for i := range info.NetInterfaces {
			if counter, ok := counterMap[info.NetInterfaces[i].Name]; ok {
				info.NetInterfaces[i].BytesSent = counter.BytesSent
				info.NetInterfaces[i].BytesRecv = counter.BytesRecv
				info.NetInterfaces[i].PacketsSent = counter.PacketsSent
				info.NetInterfaces[i].PacketsRecv = counter.PacketsRecv
			}
		}
	}

	p.cacheMu.Lock()
	p.cachedInfo = info
	p.lastRefresh = now
	p.cacheMu.Unlock()

	return info
}

// GetCPUInfo returns detailed CPU information using gopsutil
func (p *SysInfoPlugin) GetCPUInfo() map[string]interface{} {
	result := make(map[string]interface{})

	// Basic CPU info
	result["goMaxProcs"] = runtime.GOMAXPROCS(0)
	result["os"] = runtime.GOOS
	result["arch"] = runtime.GOARCH

	// Get detailed CPU info from gopsutil
	if cpuInfo, err := cpu.Info(); err == nil && len(cpuInfo) > 0 {
		result["count"] = len(cpuInfo)
		result["modelName"] = cpuInfo[0].ModelName
		result["vendor"] = cpuInfo[0].VendorID
		result["family"] = cpuInfo[0].Family
		result["model"] = cpuInfo[0].Model
		result["stepping"] = cpuInfo[0].Stepping
		result["mhz"] = cpuInfo[0].Mhz
		result["cacheSize"] = cpuInfo[0].CacheSize
		result["cores"] = cpuInfo[0].Cores
	}

	// Get CPU usage percentage
	if cpuPercent, err := cpu.Percent(time.Second, true); err == nil {
		result["usagePerCore"] = cpuPercent
	}

	// Get CPU times per CPU
	if cpuTimes, err := cpu.Times(true); err == nil {
		times := make([]map[string]interface{}, 0, len(cpuTimes))
		for _, t := range cpuTimes {
			times = append(times, map[string]interface{}{
				"cpu":       t.CPU,
				"user":      t.User,
				"system":    t.System,
				"idle":      t.Idle,
				"nice":      t.Nice,
				"iowait":    t.Iowait,
				"irq":       t.Irq,
				"softirq":   t.Softirq,
				"steal":     t.Steal,
				"guest":     t.Guest,
				"guestNice": t.GuestNice,
			})
		}
		result["timesPerCPU"] = times
	}

	return result
}

// GetMemoryInfo returns detailed memory information using gopsutil
func (p *SysInfoPlugin) GetMemoryInfo() map[string]interface{} {
	result := make(map[string]interface{})

	// Get virtual memory info
	if memInfo, err := mem.VirtualMemory(); err == nil {
		result["total"] = formatBytes(memInfo.Total)
		result["available"] = formatBytes(memInfo.Available)
		result["used"] = formatBytes(memInfo.Used)
		result["usedPercent"] = memInfo.UsedPercent
		result["free"] = formatBytes(memInfo.Free)
		result["active"] = formatBytes(memInfo.Active)
		result["inactive"] = formatBytes(memInfo.Inactive)
		result["wired"] = formatBytes(memInfo.Wired)
		result["buffers"] = formatBytes(memInfo.Buffers)
		result["cached"] = formatBytes(memInfo.Cached)
		result["shared"] = formatBytes(memInfo.Shared)
	}

	// Get swap memory info
	if swapInfo, err := mem.SwapMemory(); err == nil {
		result["swapTotal"] = formatBytes(swapInfo.Total)
		result["swapUsed"] = formatBytes(swapInfo.Used)
		result["swapFree"] = formatBytes(swapInfo.Free)
		result["swapUsedPercent"] = swapInfo.UsedPercent
	}

	// Get Go runtime memory stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	result["goAlloc"] = formatBytes(m.Alloc)
	result["goTotalAlloc"] = formatBytes(m.TotalAlloc)
	result["goSys"] = formatBytes(m.Sys)
	result["goLookups"] = m.Lookups
	result["goMallocs"] = m.Mallocs
	result["goFrees"] = m.Frees
	result["goHeapAlloc"] = formatBytes(m.HeapAlloc)
	result["goHeapSys"] = formatBytes(m.HeapSys)
	result["goHeapIdle"] = formatBytes(m.HeapIdle)
	result["goHeapInuse"] = formatBytes(m.HeapInuse)
	result["goHeapReleased"] = formatBytes(m.HeapReleased)
	result["goHeapObjects"] = m.HeapObjects
	result["goStackInuse"] = formatBytes(m.StackInuse)
	result["goStackSys"] = formatBytes(m.StackSys)
	result["goMSpanInuse"] = formatBytes(m.MSpanInuse)
	result["goMSpanSys"] = formatBytes(m.MSpanSys)
	result["goMCacheInuse"] = formatBytes(m.MCacheInuse)
	result["goMCacheSys"] = formatBytes(m.MCacheSys)
	result["goNextGC"] = m.NextGC
	result["goLastGC"] = m.LastGC
	result["goGCStats"] = map[string]interface{}{
		"numGC":        m.NumGC,
		"numForcedGC":  m.NumForcedGC,
		"pauseTotalNs": m.PauseTotalNs,
	}

	return result
}

// GetDiskInfo returns disk usage information for all mounted filesystems
func (p *SysInfoPlugin) GetDiskInfo() []map[string]interface{} {
	result := make([]map[string]interface{}, 0)

	// Get disk usage for common mount points
	paths := []string{"/"}
	if runtime.GOOS == "windows" {
		paths = []string{"C:", "D:", "E:"}
	}

	for _, path := range paths {
		if diskInfo, err := disk.Usage(path); err == nil {
			result = append(result, map[string]interface{}{
				"path":              diskInfo.Path,
				"fstype":            diskInfo.Fstype,
				"total":             formatBytes(diskInfo.Total),
				"free":              formatBytes(diskInfo.Free),
				"used":              formatBytes(diskInfo.Used),
				"usedPercent":       diskInfo.UsedPercent,
				"inodesTotal":       diskInfo.InodesTotal,
				"inodesUsed":        diskInfo.InodesUsed,
				"inodesFree":        diskInfo.InodesFree,
				"inodesUsedPercent": diskInfo.InodesUsedPercent,
			})
		}
	}

	// Get disk partition info
	if partitions, err := disk.Partitions(false); err == nil {
		for _, p := range partitions {
			// Skip if we already have this path
			found := false
			for _, existing := range result {
				if existing["path"] == p.Mountpoint {
					found = true
					break
				}
			}
			if !found {
				if diskInfo, err := disk.Usage(p.Mountpoint); err == nil {
					result = append(result, map[string]interface{}{
						"path":        diskInfo.Path,
						"fstype":      diskInfo.Fstype,
						"total":       formatBytes(diskInfo.Total),
						"free":        formatBytes(diskInfo.Free),
						"used":        formatBytes(diskInfo.Used),
						"usedPercent": diskInfo.UsedPercent,
						"device":      p.Device,
						"mountpoint":  p.Mountpoint,
						"opts":        p.Opts,
					})
				}
			}
		}
	}

	return result
}

// GetNetworkInfo returns network interface and statistics information
func (p *SysInfoPlugin) GetNetworkInfo() []map[string]interface{} {
	result := make([]map[string]interface{}, 0)

	// Get network interfaces
	if netInterfaces, err := net.Interfaces(); err == nil {
		// Get I/O counters for statistics
		counterMap := make(map[string]net.IOCountersStat)
		if netCounters, err := net.IOCounters(true); err == nil {
			for _, counter := range netCounters {
				counterMap[counter.Name] = counter
			}
		}

		for _, ni := range netInterfaces {
			ifaceInfo := map[string]interface{}{
				"name":         ni.Name,
				"hardwareAddr": ni.HardwareAddr,
				"flags":        ni.Flags,
				"addrs":        make([]string, 0, len(ni.Addrs)),
			}

			for _, addr := range ni.Addrs {
				ifaceInfo["addrs"] = append(ifaceInfo["addrs"].([]string), addr.Addr)
			}

			// Add statistics if available
			if counter, ok := counterMap[ni.Name]; ok {
				ifaceInfo["bytesSent"] = counter.BytesSent
				ifaceInfo["bytesRecv"] = counter.BytesRecv
				ifaceInfo["packetsSent"] = counter.PacketsSent
				ifaceInfo["packetsRecv"] = counter.PacketsRecv
				ifaceInfo["errin"] = counter.Errin
				ifaceInfo["errout"] = counter.Errout
				ifaceInfo["dropin"] = counter.Dropin
				ifaceInfo["dropout"] = counter.Dropout
			}

			result = append(result, ifaceInfo)
		}
	}

	// Get total network statistics
	if totalStats, err := net.IOCounters(false); err == nil && len(totalStats) > 0 {
		totalMap := map[string]interface{}{
			"name":        "total",
			"bytesSent":   totalStats[0].BytesSent,
			"bytesRecv":   totalStats[0].BytesRecv,
			"packetsSent": totalStats[0].PacketsSent,
			"packetsRecv": totalStats[0].PacketsRecv,
			"errin":       totalStats[0].Errin,
			"errout":      totalStats[0].Errout,
			"dropin":      totalStats[0].Dropin,
			"dropout":     totalStats[0].Dropout,
		}
		result = append([]map[string]interface{}{totalMap}, result...)
	}

	return result
}

// GetHostInfo returns host/platform information
func (p *SysInfoPlugin) GetHostInfo() map[string]interface{} {
	result := make(map[string]interface{})

	if hostInfo, err := host.Info(); err == nil {
		result["hostname"] = hostInfo.Hostname
		result["uptime"] = hostInfo.Uptime
		result["bootTime"] = hostInfo.BootTime
		result["procs"] = hostInfo.Procs
		result["os"] = hostInfo.OS
		result["platform"] = hostInfo.Platform
		result["platformFamily"] = hostInfo.PlatformFamily
		result["platformVersion"] = hostInfo.PlatformVersion
		result["kernelVersion"] = hostInfo.KernelVersion
		result["kernelArch"] = hostInfo.KernelArch
		result["virtualizationSystem"] = hostInfo.VirtualizationSystem
		result["virtualizationRole"] = hostInfo.VirtualizationRole
		result["hostId"] = hostInfo.HostID
	}

	return result
}

// GetLoadAverage returns load average information (Unix-like systems only)
func (p *SysInfoPlugin) GetLoadAverage() map[string]interface{} {
	result := make(map[string]interface{})

	if loadInfo, err := load.Avg(); err == nil {
		result["load1"] = loadInfo.Load1
		result["load5"] = loadInfo.Load5
		result["load15"] = loadInfo.Load15
	} else {
		result["error"] = "Load average not available on this platform"
	}

	return result
}

// GetTemperatureInfo returns temperature sensor information (if available)
// Note: Temperature sensor support varies by platform and may not be available on all systems
func (p *SysInfoPlugin) GetTemperatureInfo() []map[string]interface{} {
	result := make([]map[string]interface{}, 0)

	// Temperature sensors are platform-specific and may not be available
	// This is a placeholder for future implementation
	result = append(result, map[string]interface{}{
		"message":  "Temperature sensor support varies by platform",
		"platform": runtime.GOOS,
	})

	return result
}

// GetGoVersion returns the Go version
func (p *SysInfoPlugin) GetGoVersion() string {
	return runtime.Version()
}

// GetUptime returns the application uptime
func (p *SysInfoPlugin) GetUptime() string {
	return formatDuration(time.Since(p.startTime))
}

// GetStartTime returns the application start time
func (p *SysInfoPlugin) GetStartTime() time.Time {
	return p.startTime
}

// ForceGC forces garbage collection
func (p *SysInfoPlugin) ForceGC() {
	runtime.GC()
	p.emitEvent("gc", "forced")
}

// SetMaxProcs sets the maximum number of CPUs that can be executing
func (p *SysInfoPlugin) SetMaxProcs(n int) {
	runtime.GOMAXPROCS(n)
	p.emitEvent("maxprocs", fmt.Sprintf("%d", n))
}

// GetMaxProcs returns the maximum number of CPUs that can be executing
func (p *SysInfoPlugin) GetMaxProcs() int {
	return runtime.GOMAXPROCS(0)
}

// Helper function to format duration
func formatDuration(d time.Duration) string {
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

// Helper function to format bytes
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
