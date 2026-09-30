// Types of the docker module's API (backend/internal/docker).

export interface PortMapping {
  host_ip: string
  /** 0 when the port is exposed but not published. */
  host_port: number
  container_port: number
  protocol: string
}

export interface Address {
  network: string
  ip: string
}

export type ContainerState = 'created' | 'running' | 'paused' | 'restarting' | 'removing' | 'exited' | 'dead'

export interface Container {
  id: string
  short_id: string
  name: string
  image: string
  image_id: string
  state: ContainerState | string
  status: string
  exit_code: number | null
  created_at: number
  started_at: number | null
  uptime_seconds: number | null
  addresses: Address[]
  ports: PortMapping[]
  labels: Record<string, string>
  /** Slug of the MyServer app the container belongs to (label io.myserver.app). */
  app: string | null
}

export interface ContainerStats {
  id: string
  cpu_percent: number | null
  memory_usage: number
  memory_limit: number
  memory_percent: number
  pids: number
}

export interface StatsSnapshot {
  at: number
  available: boolean
  items: ContainerStats[]
}

export type ChangeKind = 'container' | 'image' | 'volume' | 'network'

export interface ChangeEvent {
  kind: ChangeKind
  action: string
  id: string
}

export interface EnvVar {
  name: string
  value: string
  masked: boolean
}

export interface Mount {
  type: string
  name: string
  source: string
  destination: string
  read_only: boolean
}

export interface NetworkAttachment {
  network: string
  network_id: string
  ip_address: string
  ipv6_address: string
  gateway: string
  mac_address: string
  aliases: string[]
}

export interface ContainerDetail {
  id: string
  name: string
  image: string
  image_id: string
  created_at: number | null
  state: string
  running: boolean
  paused: boolean
  restarting: boolean
  oom_killed: boolean
  exit_code: number
  started_at: number | null
  finished_at: number | null
  health: string | null
  restart_count: number
  restart_policy: string
  platform: string
  driver: string
  hostname: string
  user: string
  working_dir: string
  entrypoint: string[]
  command: string[]
  tty: boolean
  privileged: boolean
  network_mode: string
  memory_limit: number
  nano_cpus: number
  env: EnvVar[]
  labels: Record<string, string>
  app: string | null
  mounts: Mount[]
  networks: NetworkAttachment[]
  ports: PortMapping[]
}

export interface LogLine {
  stream: 'stdout' | 'stderr'
  line: string
  time: number | null
}

export interface DockerImage {
  id: string
  short_id: string
  tags: string[]
  digests: string[]
  size: number
  created_at: number
  dangling: boolean
  in_use: boolean
  containers: string[]
}

export interface PullLayer {
  id: string
  status: string
  current: number
  total: number
}

export interface PullState {
  job_id: string
  image: string
  status: string
  layers: PullLayer[]
  percent: number
  done: boolean
  success: boolean
  message: string
}

export interface Ref {
  id: string
  name: string
  ip?: string
}

export interface DockerVolume {
  name: string
  driver: string
  mountpoint: string
  scope: string
  created_at: number | null
  labels: Record<string, string>
  app: string | null
  anonymous: boolean
  in_use: boolean
  containers: Ref[]
}

export interface VolumePruneResult {
  removed: string[]
  failed: Array<{ name: string; message: string }>
}

export interface Subnet {
  subnet: string
  gateway: string
}

export interface DockerNetwork {
  id: string
  short_id: string
  name: string
  driver: string
  scope: string
  internal: boolean
  created_at: number | null
  subnets: Subnet[]
  labels: Record<string, string>
  app: string | null
  builtin: boolean
  in_use: boolean
  containers: Ref[]
}

export interface DockerInfo {
  version: string
  api_version: string
  storage_driver: string
  root_dir: string
  logging_driver: string
  cgroup_driver: string
  cgroup_version: string
  operating_system: string
  kernel_version: string
  architecture: string
  cpus: number
  memory_total: number
  containers: number
  containers_running: number
  containers_paused: number
  containers_stopped: number
  images: number
}

/** Control frames of the container terminal WebSocket (text, JSON). */
export type ExecControl =
  | { type: 'ready'; shell: string }
  | { type: 'exit'; code?: number }
  | { type: 'error'; message: string }
