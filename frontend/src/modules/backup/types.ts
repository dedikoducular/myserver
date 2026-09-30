export type BackupStatus = 'running' | 'success' | 'failed' | 'cancelled'
export type Consistency = 'stopped' | 'live'
export type Trigger = 'manual' | 'scheduled' | 'safety' | 'imported'
export type Frequency = 'daily' | 'weekly' | 'monthly'
export type JobKind = 'backup' | 'restore' | 'verify' | 'import'

export interface BackupRecord {
  id: number
  slug: string
  app_name: string
  app_version: string
  file_name: string
  size: number
  created_at: number
  duration: number
  status: BackupStatus
  consistency: Consistency
  encrypted: boolean
  includes_binds: boolean
  trigger: Trigger
  error: string
  warnings: string[]
  verified_at: number
  verify_ok: boolean
  file_missing: boolean
}

export interface Schedule {
  slug: string
  enabled: boolean
  frequency: Frequency
  hour: number
  minute: number
  weekday: number
  monthday: number
  keep_last: number
  prune_manual: boolean
  live: boolean
  include_binds: boolean
  last_run_at: number
  updated_at: number
  next_run_at: number
  exists: boolean
}

export interface AppVolume {
  service: string
  type: 'volume' | 'bind' | 'system'
  source: string
  target: string
  read_only: boolean
}

export interface AppSummary {
  slug: string
  name: string
  version: string
  volumes: AppVolume[]
  backup_count: number
  total_size: number
  last_backup: BackupRecord | null
  last_success: BackupRecord | null
  schedule: Schedule | null
}

export interface EncryptionInfo {
  enabled: boolean
  set_at: number
  cipher: string
  kdf: string
  key_file: string
  error: string
}

export interface LogLine {
  time: number
  level: 'info' | 'warning' | 'error'
  message: string
}

export interface PreviewEntry {
  kind: 'volume' | 'bind'
  source: string
  service: string
  target: string
  read_only: boolean
  files: number
  bytes: number
  exists: boolean
}

export interface Preview {
  slug: string
  app_name: string
  app_version: string
  created_at: number
  panel_version: string
  consistency: Consistency
  encrypted: boolean
  images: string[]
  entries: PreviewEntry[]
  untouched: string[]
  warnings: string[]
  apps_available: boolean
  app_installed: boolean
  app_running: boolean
  installed_version: string
  secret_count: number
}

export interface RestoreResult {
  restored: string[]
  skipped: string[]
  warnings: string[]
  safety_backup: string
  safety_backup_id: number
  was_installed: boolean
}

export interface Job {
  id: string
  kind: JobKind
  slug: string
  name: string
  trigger: Trigger
  backup_id: number
  status: BackupStatus
  step: string
  error: string
  bytes_done: number
  bytes_total: number
  cancellable: boolean
  cancel_requested: boolean
  started_at: number
  finished_at: number
  logs: LogLine[]
  result: Preview | RestoreResult | null
}

export interface Overview {
  apps_available: boolean
  apps_error: string
  apps: AppSummary[]
  orphans: AppSummary[]
  dir: string
  free_bytes: number | null
  total_bytes: number | null
  used_bytes: number
  encryption: EncryptionInfo
  jobs: Job[]
}

export interface UploadResult {
  upload_id: string
  size: number
  encrypted: boolean
  needs_passphrase: boolean
}
