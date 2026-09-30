// Shapes returned by /api/v1/system/*. Sizes are bytes, rates bytes/second,
// times Unix seconds.

export interface CpuMetrics {
  percent: number | null
  load1: number
  load5: number
  load15: number
  cores: number
  threads: number
  model: string
}

export interface MemoryMetrics {
  total: number
  used: number
  available: number
  percent: number
  swap_total: number
  swap_used: number
}

export interface DiskUsage {
  mount: string
  device: string
  fstype: string
  total: number
  used: number
  free: number
  percent: number
  system: boolean
}

export interface NvmeTemperature {
  name: string
  celsius: number
}

export interface TemperatureMetrics {
  cpu: number | null
  source: string
  nvme: NvmeTemperature[]
}

export interface InterfaceMetrics {
  name: string
  rx_bytes: number
  tx_bytes: number
  rx_rate: number
  tx_rate: number
  virtual: boolean
}

export interface NetworkMetrics {
  rx_rate: number
  tx_rate: number
  interfaces: InterfaceMetrics[]
}

export interface Snapshot {
  time: number
  uptime: number
  cpu: CpuMetrics
  memory: MemoryMetrics
  disks: DiskUsage[]
  root_disk: DiskUsage | null
  temperature: TemperatureMetrics
  network: NetworkMetrics
}

export interface FinePoint {
  t: number
  cpu: number
  memory: number
  disk: number | null
  temperature: number | null
  rx_rate: number
  tx_rate: number
}

export interface NetPoint {
  t: number
  rx_rate: number
  tx_rate: number
}

export interface MetricsPayload {
  snapshot: Snapshot | null
  interval: number
  history: FinePoint[]
}

export type HistoryRange = '5m' | '15m' | '30m' | '1h'

export interface NetworkHistory {
  range: HistoryRange
  interval: number
  points: NetPoint[]
}

export interface SystemInfo {
  os: string
  kernel: string
  cpu_model: string
  cpu_cores: number
  cpu_threads: number
  memory_total: number
  hostname: string
  ip: string
  interface: string
  uptime: number
  load: [number, number, number]
  time: number
  timezone: string
}

export type HealthStatus = 'HEALTHY' | 'WARNING' | 'CRITICAL'

export interface HealthReport {
  status: HealthStatus
  checks: Array<{ id: string; name: string; status: HealthStatus; message: string }>
  checked_at: number
}
