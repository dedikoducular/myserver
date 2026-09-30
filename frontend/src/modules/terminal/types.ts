export interface TerminalStatus {
  supported: boolean
  enabled: boolean
  allowed: boolean
  user: string
  user_valid: boolean
  user_error: string | null
  helper_available: boolean | null
  active_sessions: number
  own_sessions: number
  max_sessions: number
  max_sessions_per_user: number
  idle_timeout_minutes: number
  max_session_seconds: number
}

export interface SystemUser {
  username: string
  uid: number
  full_name: string
  home: string
  shell: string
}

/** Control messages sent by the server as text frames. */
export type ServerMessage =
  | { type: 'exit'; code: number }
  | { type: 'error'; reason?: string; message: string }
  | { type: 'idle_warning'; seconds: number }
  | { type: 'pong' }

export const SETTING_ENABLED = 'terminal.enabled'
export const SETTING_USER = 'terminal.user'
export const SETTING_TIMEOUT = 'terminal.idle_timeout_minutes'
