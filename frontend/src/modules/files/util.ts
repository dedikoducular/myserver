import {
  File,
  FileArchive,
  FileAudio,
  FileCode,
  FileImage,
  FileQuestion,
  FileText,
  FileVideo,
  Folder,
  FolderSymlink,
  Link2Off,
  type LucideIcon,
} from 'lucide-react'
import type { Tone } from '@/components/ui'
import { apiUrl } from '@/services/api'
import { t } from './strings'
import type { FileEntry, RootInfo } from './types'

export function isDirLike(e: FileEntry): boolean {
  return e.type === 'directory' || (e.type === 'symlink' && e.link_kind === 'directory')
}

export function isFileLike(e: FileEntry): boolean {
  return e.type === 'file' || (e.type === 'symlink' && e.link_kind === 'file')
}

function ext(name: string): string {
  const i = name.lastIndexOf('.')
  return i > 0 ? name.slice(i + 1).toLowerCase() : ''
}

const IMAGE = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'avif', 'ico'])
const VIDEO = new Set(['mp4', 'm4v', 'webm'])
const AUDIO = new Set(['mp3', 'wav', 'ogg', 'flac', 'm4a', 'opus'])
const TEXT = new Set([
  'txt', 'log', 'md', 'csv', 'json', 'xml', 'yaml', 'yml', 'toml', 'ini', 'conf', 'cfg', 'env', 'html', 'htm', 'css',
  'js', 'ts', 'tsx', 'jsx', 'sh', 'py', 'go', 'c', 'h', 'cpp', 'java', 'rs', 'php', 'sql', 'service', 'list', 'svg',
])

export type PreviewKind = 'image' | 'video' | 'audio' | 'text'

/** How a file can be opened inside the panel, or null when it can only be
 *  downloaded. SVG and HTML are shown as text, never rendered. */
export function previewKind(e: FileEntry, textMax: number): PreviewKind | null {
  if (!isFileLike(e)) return null
  const x = ext(e.name)
  if (IMAGE.has(x)) return 'image'
  if (VIDEO.has(x)) return 'video'
  if (AUDIO.has(x)) return 'audio'
  if (e.size <= textMax && (TEXT.has(x) || e.mime.startsWith('text/') || (x === '' && e.size > 0))) return 'text'
  return null
}

export function isArchive(e: FileEntry): boolean {
  if (!isFileLike(e)) return false
  const n = e.name.toLowerCase()
  return n.endsWith('.zip') || n.endsWith('.tar') || n.endsWith('.tar.gz') || n.endsWith('.tgz')
}

export function entryIcon(e: FileEntry): { icon: LucideIcon; tone: Tone } {
  if (e.type === 'symlink') {
    if (e.link_kind === 'directory') return { icon: FolderSymlink, tone: 'cyan' }
    if (e.link_kind === 'unreachable') return { icon: Link2Off, tone: 'warning' }
  }
  if (e.type === 'directory') return { icon: Folder, tone: 'accent' }
  if (e.type === 'other' || e.link_kind === 'other') return { icon: FileQuestion, tone: 'neutral' }
  const x = ext(e.name)
  if (IMAGE.has(x) || e.mime.startsWith('image/')) return { icon: FileImage, tone: 'purple' }
  if (e.mime.startsWith('video/')) return { icon: FileVideo, tone: 'danger' }
  if (e.mime.startsWith('audio/')) return { icon: FileAudio, tone: 'success' }
  if (/zip|tar|gzip|bzip|x-xz|7z|rar|zstd/.test(e.mime)) return { icon: FileArchive, tone: 'warning' }
  if (['json', 'xml', 'yaml', 'yml', 'js', 'ts', 'sh', 'py', 'go', 'c', 'cpp', 'java', 'rs', 'php', 'sql', 'html', 'css'].includes(x))
    return { icon: FileCode, tone: 'cyan' }
  if (e.mime.startsWith('text/') || e.mime === 'application/pdf') return { icon: FileText, tone: 'neutral' }
  return { icon: File, tone: 'neutral' }
}

const toneClass: Record<Tone, string> = {
  neutral: 'text-muted',
  accent: 'text-accent',
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
  purple: 'text-purple',
  cyan: 'text-cyan',
}

export function iconClass(tone: Tone): string {
  return toneClass[tone]
}

export function typeLabel(e: FileEntry): string {
  switch (e.type) {
    case 'directory':
      return t('typeFolder')
    case 'symlink':
      return t('typeSymlink')
    case 'file':
      return t('typeFile')
    default:
      return t('typeOther')
  }
}

export interface Crumb {
  label: string
  path: string
}

/** Breadcrumb from the allowed root down to path. */
export function crumbs(path: string, root: string): Crumb[] {
  const out: Crumb[] = [{ label: root, path: root }]
  if (path === root || !path.startsWith(root + '/')) return out
  let current = root
  for (const part of path.slice(root.length + 1).split('/')) {
    if (!part) continue
    current += '/' + part
    out.push({ label: part, path: current })
  }
  return out
}

/** The allowed root containing path (longest match), if any. */
export function rootOf(path: string, roots: RootInfo[]): RootInfo | undefined {
  let best: RootInfo | undefined
  for (const r of roots) {
    if ((path === r.path || path.startsWith(r.path + '/')) && (!best || r.path.length > best.path.length)) best = r
  }
  return best
}

export function parentOf(path: string): string {
  const i = path.lastIndexOf('/')
  return i <= 0 ? '/' : path.slice(0, i)
}

/** Client-side check mirroring the server's name rules. */
export function nameError(name: string): string | null {
  const n = name.trim()
  if (!n) return t('nameRequired')
  if (n === '.' || n === '..' || /[/\\\u0000-\u001f\u007f]/.test(n)) return t('nameInvalid')
  return null
}

function multiPathUrl(endpoint: string, paths: string[]): string {
  const params = new URLSearchParams()
  for (const p of paths) params.append('path', p)
  return `${endpoint}?${params.toString()}`
}

/** API path (without the /api/v1 prefix) carrying several `path` values. */
export function multiPath(endpoint: string, paths: string[]): string {
  return multiPathUrl(endpoint, paths)
}

export function downloadUrl(paths: string[]): string {
  return apiUrl(multiPathUrl('/files/download', paths))
}

export function previewUrl(path: string): string {
  return apiUrl('/files/preview', { path })
}

/** Starts a browser download without leaving the page. */
export function startDownload(paths: string[]): void {
  const a = document.createElement('a')
  a.href = downloadUrl(paths)
  a.rel = 'noopener'
  a.download = ''
  document.body.appendChild(a)
  a.click()
  a.remove()
}
