// Shapes returned by the /apps API (see backend/internal/apps/handlers.go).

export type AppState = 'running' | 'stopped' | 'starting' | 'unhealthy' | 'partial' | 'missing' | 'unknown'

export interface Category {
  id: string
  label: string
}

export interface ServiceState {
  name: string
  container_name: string
  container_id: string
  image: string
  state: AppState
  status: string
}

export type BindAddress = 'all' | 'loopback'

export interface WebUI {
  port: number
  scheme: 'http' | 'https'
  path: string
  /** Published on 127.0.0.1: cannot be opened from another device. */
  local_only: boolean
}

export interface PortView {
  service: string
  /** "0.0.0.0" (all interfaces) or "127.0.0.1" (this server only). */
  host_ip: string
  /** The service uses the server's network directly. */
  host_network: boolean
  host: number
  container: number
  protocol: 'tcp' | 'udp'
  label: string
}

export interface InstalledApp {
  slug: string
  name: string
  description: string
  category: string
  category_label: string
  icon: string
  version: string
  available_version: string
  manifest_available: boolean
  installed_at: number
  updated_at: number
  state: AppState
  /** Operation currently running on the application, or "". */
  operation: string
  job_id: string
  services: ServiceState[]
  web_ui: WebUI | null
  ports: PortView[]
  bind_address: BindAddress
}

export interface InstalledResponse {
  apps: InstalledApp[]
  categories: Category[]
  docker_available: boolean
}

export interface CatalogApp {
  slug: string
  name: string
  description: string
  category: string
  category_label: string
  icon: string
  version: string
  website: string
  images: string[]
  warning_level: '' | 'warning' | 'danger'
  /** Architectures declared by the manifest; empty when not declared. */
  architectures: string[]
  /** False when the server's CPU architecture is not supported. */
  installable: boolean
  unsupported_reason: string
  installed: boolean
  operation: string
  job_id: string
}

export interface InvalidManifest {
  file: string
  error: string
}

export interface CatalogResponse {
  apps: CatalogApp[]
  categories: Category[]
  invalid: InvalidManifest[]
  loaded_at: number
  load_error: string
}

export interface ReloadResponse {
  valid: number
  invalid: InvalidManifest[]
  load_error: string
}

export interface Warning {
  level: 'warning' | 'danger'
  code: string
  title: string
  message: string
}

export interface PortField {
  key: string
  service: string
  label: string
  container: number
  protocol: 'tcp' | 'udp'
  default: number
  value: number
  fixed: boolean
  web_ui: boolean
  /** Published only while this option is enabled; "" when always. */
  option: string
}

export interface OptionField {
  key: string
  service: string
  label: string
  description: string
  default: boolean
  value: boolean
  cap_add: string[]
}

export interface EnvField {
  key: string
  name: string
  label: string
  description: string
  default: string
  value: string
  required: boolean
  secret: boolean
  generated: boolean
  has_value: boolean
}

export interface PathField {
  key: string
  service: string
  label: string
  description: string
  target: string
  default: string
  value: string
  required: boolean
  read_only: boolean
}

export interface AppDetail extends CatalogApp {
  long_description: string
  notes: string
  warnings: Warning[]
  requires_risk_acceptance: boolean
  allowed_roots: string[]
  bind_address: BindAddress
  publishes_ports: boolean
  host_network: boolean
  fields: { ports: PortField[]; env: EnvField[]; paths: PathField[]; options: OptionField[] }
  services: Array<{ name: string; image: string; depends_on: string[] }>
  volumes: Array<{ service: string; type: string; source: string; target: string; label: string; read_only: boolean }>
  installed_app: InstalledApp | null
}

export type JobKind = 'install' | 'update' | 'settings' | 'restore'

export interface Job {
  id: string
  slug: string
  name: string
  kind: JobKind
  status: 'running' | 'success' | 'failed'
  error: string
  started_at: number
  finished_at: number
  last_step: string
}

export interface JobEvent {
  seq: number
  time: number
  type: 'step' | 'progress' | 'log' | 'done' | 'error'
  message: string
  service?: string
  image?: string
  current?: number
  total?: number
}

export interface InputsBody {
  ports: Record<string, number>
  env: Record<string, string>
  paths: Record<string, string>
  options: Record<string, boolean>
  bind_address: BindAddress
  accept_risks: boolean
}

export interface UninstallResult {
  removed_volumes: string[]
  kept_volumes: string[]
  failed_volumes: string[]
  kept_paths: string[]
}

export interface LogLine {
  service: string
  stream: 'stdout' | 'stderr'
  time: string
  line: string
}

export type ViewMode = 'grid' | 'list'
