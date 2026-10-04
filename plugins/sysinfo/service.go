package sysinfo

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// SysInfoService exposes SystemInfo functionality to the frontend
type SysInfoService struct {
	app    *application.App
	plugin *SysInfoPlugin
}

// NewSysInfoService creates a new system info service
func NewSysInfoService(plugin *SysInfoPlugin, app *application.App) *SysInfoService {
	return &SysInfoService{
		app:    app,
		plugin: plugin,
	}
}

// ServiceStartup is called when the application starts
func (s *SysInfoService) ServiceStartup(app *application.App) error {
	return s.plugin.ServiceStartup(app)
}

// ServiceShutdown is called when the application shuts down
func (s *SysInfoService) ServiceShutdown(app *application.App) error {
	return s.plugin.ServiceShutdown(app)
}

// GetSystemInfo returns current system information
func (s *SysInfoService) GetSystemInfo() *SystemInfo {
	return s.plugin.GetSystemInfo()
}

// GetCPUInfo returns CPU information
func (s *SysInfoService) GetCPUInfo() map[string]interface{} {
	return s.plugin.GetCPUInfo()
}

// GetMemoryInfo returns memory information
func (s *SysInfoService) GetMemoryInfo() map[string]interface{} {
	return s.plugin.GetMemoryInfo()
}

// GetDiskInfo returns disk usage information
func (s *SysInfoService) GetDiskInfo() []map[string]interface{} {
	return s.plugin.GetDiskInfo()
}

// GetNetworkInfo returns network interface information
func (s *SysInfoService) GetNetworkInfo() []map[string]interface{} {
	return s.plugin.GetNetworkInfo()
}

// GetHostInfo returns host/platform information
func (s *SysInfoService) GetHostInfo() map[string]interface{} {
	return s.plugin.GetHostInfo()
}

// GetLoadAverage returns load average information
func (s *SysInfoService) GetLoadAverage() map[string]interface{} {
	return s.plugin.GetLoadAverage()
}

// GetTemperatureInfo returns temperature sensor information
func (s *SysInfoService) GetTemperatureInfo() []map[string]interface{} {
	return s.plugin.GetTemperatureInfo()
}

// GetGoVersion returns the Go version
func (s *SysInfoService) GetGoVersion() string {
	return s.plugin.GetGoVersion()
}

// GetUptime returns the application uptime
func (s *SysInfoService) GetUptime() string {
	return s.plugin.GetUptime()
}

// GetStartTime returns the application start time
func (s *SysInfoService) GetStartTime() int64 {
	return s.plugin.GetStartTime().Unix()
}

// ForceGC forces garbage collection
func (s *SysInfoService) ForceGC() {
	s.plugin.ForceGC()
}

// SetMaxProcs sets the maximum number of CPUs that can be executing
func (s *SysInfoService) SetMaxProcs(n int) {
	s.plugin.SetMaxProcs(n)
}

// GetMaxProcs returns the maximum number of CPUs that can be executing
func (s *SysInfoService) GetMaxProcs() int {
	return s.plugin.GetMaxProcs()
}

// EnterView notifies the plugin that a sysinfo consumer (plugin page or Home
// dashboard card) became visible. Background sampling starts with the first
// consumer and is reference counted across concurrent consumers/windows.
func (s *SysInfoService) EnterView() error {
	return s.plugin.OnViewEnter(s.app)
}

// LeaveView notifies the plugin that a sysinfo consumer is no longer visible.
// Background sampling stops when the last consumer leaves.
func (s *SysInfoService) LeaveView() error {
	return s.plugin.OnViewLeave(s.app)
}
