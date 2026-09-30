export type ServiceState =
  | 'running'
  | 'active'
  | 'stopped'
  | 'failed'
  | 'disabled'
  | 'activating'
  | 'deactivating'
  | 'masked'
  | 'not_installed'

export type Verb = 'start' | 'stop' | 'restart' | 'reload' | 'enable' | 'disable'

export type RiskKind = '' | 'ssh' | 'panel' | 'network' | 'container' | 'core'

export interface Service {
  unit: string
  name: string
  description: string
  load_state: string
  active_state: string
  sub_state: string
  unit_file_state: string
  state: ServiceState
  installed: boolean
  main_pid: number | null
  memory_bytes: number | null
  active_since: number | null
  can_reload: boolean
  triggered_by: string[]
  risk: 'normal' | 'critical' | 'protected'
  risk_kind: RiskKind
  featured: boolean
}

export interface ServiceList {
  featured: Service[]
  services: Service[]
  updated_at: number
}

export interface ServiceLogs {
  unit: string
  lines: string[]
  requested: number
}

export const SETTING_FEATURED = 'services.featured'

export const DEFAULT_FEATURED = [
  'docker.service',
  'ssh.service',
  'chrony.service',
  'ufw.service',
  'nginx.service',
  'tailscaled.service',
  'smbd.service',
  'nfs-server.service',
  'myserver.service',
]

export const MAX_FEATURED = 24
