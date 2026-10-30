// Package proto описывает обмен данными между агентом и сервером.
package proto

// Inventory — опись устройства, которую агент отправляет при регистрации
// и периодически обновляет.
type Inventory struct {
	Hostname  string   `json:"hostname"`
	MachineID string   `json:"machine_id"`
	OS        string   `json:"os"`        // linux / windows
	OSName    string   `json:"os_name"`   // Ubuntu 24.04.1 LTS
	Kernel    string   `json:"kernel"`    // 6.8.0-45-generic
	Arch      string   `json:"arch"`      // amd64
	CPUCores  int      `json:"cpu_cores"` // логические ядра
	CPUModel  string   `json:"cpu_model"`
	MemTotal  uint64   `json:"mem_total"` // байты
	IPs       []string `json:"ips"`
	AgentVer  string   `json:"agent_version"`
	BootTime  int64    `json:"boot_time"` // unix
}

type RegisterRequest struct {
	Token     string    `json:"token"`
	Inventory Inventory `json:"inventory"`
}

type RegisterResponse struct {
	HostID string `json:"host_id"`
	Key    string `json:"key"`
}

// Sample — одно значение метрики. Labels дополняют обязательную метку host.
type Sample struct {
	Name   string            `json:"n"`
	Labels map[string]string `json:"l,omitempty"`
	Value  float64           `json:"v"`
}

type MetricsBatch struct {
	Timestamp int64      `json:"ts"` // unix ms
	Samples   []Sample   `json:"samples"`
	Inventory *Inventory `json:"inventory,omitempty"` // раз в N циклов
}

// Имена метрик (без префикса sentra_, сервер добавит его сам).
const (
	MCPUUsage    = "cpu_usage_percent"
	MCPUIOWait   = "cpu_iowait_percent"
	MLoad1       = "load1"
	MLoad5       = "load5"
	MLoad15      = "load15"
	MMemUsedPct  = "mem_used_percent"
	MMemUsed     = "mem_used_bytes"
	MMemTotal    = "mem_total_bytes"
	MSwapUsedPct = "swap_used_percent"
	MFsUsedPct   = "fs_used_percent"
	MFsFree      = "fs_free_bytes"
	MFsTotal     = "fs_total_bytes"
	MNetRx       = "net_rx_bytes_per_sec"
	MNetTx       = "net_tx_bytes_per_sec"
	MNetErrors   = "net_errors_per_sec"
	MUptime      = "uptime_seconds"
	MProcs       = "processes"
)
