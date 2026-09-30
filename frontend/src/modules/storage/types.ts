export interface Usage {
  total: number
  used: number
  free: number
  percent: number
}

export interface MountPointInfo {
  path: string
  read_only: boolean
  browsable: boolean
}

export type SmartStatus =
  | 'passed'
  | 'warning'
  | 'failed'
  | 'standby'
  | 'disabled'
  | 'unsupported'
  | 'not_installed'
  | 'unknown'

export interface SmartSummary {
  status: SmartStatus
  temperature: number | null
  problems: string[]
  checked_at: number
}

export interface Device {
  name: string
  path: string
  type: string
  size: number
  model: string
  serial: string
  vendor: string
  transport: string
  rotational: boolean
  removable: boolean
  hotplug: boolean
  read_only: boolean
  fstype: string
  label: string
  uuid: string
  mountpoints: MountPointInfo[]
  usage: Usage | null
  swap: boolean
  in_use: boolean
  system: boolean
  system_reason: string
  manageable: boolean
  mountable: boolean
  persistent: boolean
  suggested_name: string
  smart: SmartSummary | null
  children: Device[]
}

export interface PersistentMount {
  uuid: string
  mountpoint: string
  fstype: string
  present: boolean
  device: string
}

export interface FormatOption {
  type: string
  max_label: number
  installed: boolean
}

export interface DisksResponse {
  devices: Device[]
  system_detection: boolean
  persistent_mounts: PersistentMount[]
  smart_installed: boolean
  filesystems: FormatOption[]
  temp_warning: number
  generated_at: number
}

export interface SmartAttribute {
  id: number
  name: string
  value: number
  worst: number
  threshold: number
  raw: number
  raw_string: string
  flags: string
  when_failed: string
  prefail: boolean
}

export interface SmartNVMe {
  critical_warning: number
  available_spare: number
  available_spare_threshold: number
  percentage_used: number
  media_errors: number
  unsafe_shutdowns: number
  data_units_read: number
  data_units_written: number
  error_log_entries: number
}

export interface SmartReport {
  device: string
  status: SmartStatus
  protocol: string
  model: string
  serial: string
  firmware: string
  exit_status: number
  passed: boolean | null
  temperature: number | null
  power_on_hours: number | null
  power_cycles: number | null
  reallocated_sectors: number | null
  pending_sectors: number | null
  uncorrectable_sectors: number | null
  nvme: SmartNVMe | null
  attributes: SmartAttribute[]
  problems: string[]
  messages: string[]
  checked_at: number
}

export interface MountResult {
  device: string
  mountpoint: string
  fstype: string
  options: string
  persistent: boolean
  warning: string
  browsable: boolean
}

export interface FormatContent {
  name: string
  type: string
  size: number
  fstype: string
  label: string
  mountpoints: string[]
}

export interface FormatPrepare {
  token: string
  expires_at: number
  device: string
  path: string
  type: string
  whole_disk: boolean
  size: number
  fstype: string
  label: string
  disk: {
    name: string
    model: string
    vendor: string
    serial: string
    size: number
    transport: string
    removable: boolean
  }
  contents: FormatContent[]
  filesystems: FormatOption[]
  sfdisk: boolean
}

export interface FormatResult {
  device: string
  partition: string
  fstype: string
  label: string
}
