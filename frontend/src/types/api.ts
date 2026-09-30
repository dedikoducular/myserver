// Types for the core API (auth, notifications, health, audit, logs).
// Feature modules keep their own types next to their code.

export type Role = 'admin' | 'user'

export interface User {
  id: number
  username: string
  role: Role
  disabled: boolean
  created_at: number
  last_login_at: number | null
}

export interface AuthStatus {
  setup_complete: boolean
  authenticated: boolean
  user: User | null
  csrf_token: string
  version: string
  language: string
}

export interface SetupCheck {
  ok: boolean
  message: string
  detail?: string
}

export interface SetupChecks {
  hostname: string
  timezone: string
  storage: SetupCheck
  docker: SetupCheck
  helper: SetupCheck
}

export interface SetupResult extends AuthStatus {
  warnings: string[]
}

export type Severity = 'INFO' | 'SUCCESS' | 'WARNING' | 'ERROR' | 'CRITICAL'

export interface Notification {
  id: number
  created_at: number
  severity: Severity
  source: string
  title: string
  message: string
  read: boolean
}

export interface NotificationList {
  items: Notification[]
  unread: number
}

export type HealthStatus = 'HEALTHY' | 'WARNING' | 'CRITICAL'

export interface HealthCheck {
  id: string
  name: string
  status: HealthStatus
  message: string
}

export interface HealthReport {
  status: HealthStatus
  checks: HealthCheck[]
  checked_at: number
}

export interface AuditEntry {
  id: number
  created_at: number
  username: string
  ip: string
  action: string
  target: string
  detail: string
  success: boolean
}

export interface LogEntry {
  time: string
  level: 'INFO' | 'WARN' | 'ERROR'
  message: string
  attrs?: Record<string, string>
}

export interface SessionInfo {
  ip: string
  user_agent: string
  created_at: number
  last_seen_at: number
  expires_at: number
  current: boolean
}

export interface ModuleState {
  name: string
  loaded: boolean
  error?: string
}

export type SettingsMap = Record<string, string>
