export interface StateError {
  code: string
  message: string
}

export type JobStatus = 'running' | 'success' | 'failed'

export interface JobMeta {
  id: string
  kind: 'apt_upgrade' | 'docker_pull'
  title: string
  detail: string
  username: string
  started_at: number
  finished_at: number | null
  status: JobStatus
  message: string
}

export interface AptPackage {
  name: string
  arch: string
  current_version: string
  candidate_version: string
  origin: string
  security: boolean
  kernel: boolean
  new: boolean
}

export interface AptView {
  packages: AptPackage[]
  count: number
  security_count: number
  kernel_count: number
  checked_at: number | null
  lists_refreshed_at: number | null
  error: StateError | null
  checking: boolean
  auto_check: boolean
  reboot_required: boolean
  reboot_packages: string[]
  hostname: string
  job: JobMeta | null
}

export type ImageState = 'current' | 'update_available' | 'unchecked'

export interface ImageContainer {
  id: string
  name: string
  state: string
  app: string
}

export interface ImageStatus {
  ref: string
  name: string
  tag: string
  status: ImageState
  reason_code: string
  reason: string
  app: string
  containers: ImageContainer[]
  local_digest: string
  remote_digest: string
  pulled: boolean
}

export interface DockerView {
  images: ImageStatus[]
  count: number
  unchecked: number
  checked_at: number | null
  error: StateError | null
  checking: boolean
  auto_check: boolean
  job: JobMeta | null
}

export interface Release {
  version: string
  notes: string
  published_at: number | null
  url: string
  assets: Record<string, ReleaseAsset>
}

export interface ReleaseAsset {
  url: string
  sha256: string
}

export interface LastUpdate {
  state: 'running' | 'success' | 'failed' | 'rolled_back'
  from_version: string
  to_version: string
  time: number | null
  message: string
}

export interface SelfView {
  arch: string
  asset: ReleaseAsset | null
  installable: boolean
  install_blocker: string
  last_update: LastUpdate | null
  installed_version: string
  source: 'none' | 'github' | 'url'
  source_label: string
  configured: boolean
  latest: Release | null
  update_available: boolean
  checked_at: number | null
  error: StateError | null
  checking: boolean
  auto_check: boolean
}

interface SummaryArea {
  count: number
  checked_at: number | null
  checking: boolean
  error: StateError | null
}

export interface Summary {
  total: number
  apt: SummaryArea & {
    security_count: number
    kernel_count: number
    reboot_required: boolean
    running: boolean
  }
  docker: SummaryArea & { images: number; unchecked: number }
  self: SummaryArea & {
    installed_version: string
    latest_version: string
    configured: boolean
  }
}
