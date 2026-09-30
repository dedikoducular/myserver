export type EntryType = 'file' | 'directory' | 'symlink' | 'other'
export type LinkKind = 'file' | 'directory' | 'other' | 'unreachable'

export interface FileEntry {
  name: string
  path: string
  type: EntryType
  link_target: string | null
  link_kind: LinkKind | null
  size: number
  mode: string
  mode_octal: string
  uid: number | null
  gid: number | null
  owner: string | null
  group: string | null
  modified_at: number
  mime: string
}

export interface ListResponse {
  path: string
  root: string
  parent: string | null
  directory: FileEntry
  entries: FileEntry[]
  total: number
  offset: number
  limit: number
  truncated: boolean
}

export interface RootInfo {
  path: string
  name: string
  exists: boolean
  total_bytes: number | null
  free_bytes: number | null
}

export interface RootsResponse {
  roots: RootInfo[]
  max_upload_bytes: number
  text_max_bytes: number
}

export interface StatResponse {
  entry: FileEntry
  is_root: boolean
  root: string
}

export interface TreeTotals {
  items: number
  bytes: number
  truncated: boolean
}

export type JobKind = 'copy' | 'move' | 'delete' | 'zip' | 'extract' | 'size'
export type JobStatus = 'running' | 'done' | 'failed' | 'cancelled'

export interface Job {
  id: string
  kind: JobKind
  title: string
  owner: string
  status: JobStatus
  phase: string
  items_total: number
  items_done: number
  bytes_total: number
  bytes_done: number
  skipped: number
  current: string
  message: string
  result: unknown
  started_at: number
  finished_at: number | null
}

export interface TextFile {
  path: string
  content: string
  size: number
  modified_at: number
}

export interface UploadResult {
  name: string
  path: string
  size: number
}

export type ConflictPolicy = 'rename' | 'overwrite' | 'skip'
export type SortKey = 'name' | 'size' | 'mode' | 'modified'
export type SortOrder = 'asc' | 'desc'
