import type { Tone } from '@/components/ui'
import { formatBytes, formatPercent } from '@/lib/format'
import { t, type StringKey } from './strings'
import type { Container, ContainerStats, PortMapping } from './types'

export const CONTAINER_ID = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$/

/** Mirrors the backend's image reference pattern. */
const IMAGE_REF = new RegExp(
  '^[a-z0-9]+(?:[._-]+[a-z0-9]+)*(?::[0-9]{1,5})?' +
    '(?:/[a-z0-9]+(?:[._-]+[a-z0-9]+)*)*' +
    '(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?' +
    '(?:@sha256:[a-f0-9]{64})?$',
)

export function validImageRef(ref: string): boolean {
  return ref.length > 0 && ref.length <= 255 && IMAGE_REF.test(ref)
}

/** Exit codes that result from a deliberate stop (SIGINT, SIGKILL, SIGTERM). */
function signalExit(code: number): boolean {
  return code === 130 || code === 137 || code === 143
}

export function isActive(c: Pick<Container, 'state'>): boolean {
  return c.state === 'running' || c.state === 'paused' || c.state === 'restarting'
}

export function stateInfo(c: Pick<Container, 'state' | 'exit_code'>): { tone: Tone; label: string; pulse: boolean } {
  switch (c.state) {
    case 'running':
      return { tone: 'success', label: t('stateRunning'), pulse: false }
    case 'paused':
      return { tone: 'warning', label: t('statePaused'), pulse: false }
    case 'restarting':
      return { tone: 'warning', label: t('stateRestarting'), pulse: true }
    case 'created':
      return { tone: 'neutral', label: t('stateCreated'), pulse: false }
    case 'removing':
      return { tone: 'neutral', label: t('stateRemoving'), pulse: true }
    case 'dead':
      return { tone: 'danger', label: t('stateDead'), pulse: false }
    case 'exited':
      if (c.exit_code != null && c.exit_code !== 0 && !signalExit(c.exit_code)) {
        return { tone: 'danger', label: t('stateFailed', { code: c.exit_code }), pulse: false }
      }
      return { tone: 'danger', label: t('stateExited'), pulse: false }
    default:
      return { tone: 'neutral', label: c.state, pulse: false }
  }
}

/** Distinct port mappings as text, published ones first: "8096:8096", "53:53/udp". */
export function portLabels(ports: PortMapping[]): string[] {
  const published = new Set<string>()
  const exposed = new Set<string>()
  for (const p of ports) {
    const suffix = p.protocol && p.protocol !== 'tcp' ? '/' + p.protocol : ''
    if (p.host_port > 0) published.add(`${p.host_port}:${p.container_port}${suffix}`)
    else exposed.add(`${p.container_port}/${p.protocol || 'tcp'}`)
  }
  return published.size > 0 ? [...published] : [...exposed]
}

export function cpuText(s: ContainerStats | undefined): string {
  return s?.cpu_percent == null ? t('none') : formatPercent(s.cpu_percent, 1)
}

export function ramText(s: ContainerStats | undefined): string {
  return s ? formatBytes(s.memory_usage) : t('none')
}

export function ramDetail(s: ContainerStats | undefined): string {
  if (!s) return t('none')
  if (!s.memory_limit) return formatBytes(s.memory_usage)
  return `${formatBytes(s.memory_usage)} / ${formatBytes(s.memory_limit)}`
}

const layerStatus: Record<string, StringKey> = {
  Waiting: 'layerWaiting',
  'Pulling fs layer': 'layerPulling',
  Downloading: 'layerDownloading',
  'Verifying Checksum': 'layerVerifying',
  'Download complete': 'layerDownloaded',
  Extracting: 'layerExtracting',
  'Pull complete': 'layerComplete',
  'Already exists': 'layerExists',
}

/** Turkish text for one of Docker's layer status strings. */
export function layerStatusText(status: string): string {
  const key = layerStatus[status]
  return key ? t(key) : status
}

// eslint-disable-next-line no-control-regex
const ANSI = /\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]/g

/** Removes terminal escape sequences from a log line. */
export function stripAnsi(text: string): string {
  return text.includes('\x1b') ? text.replace(ANSI, '') : text
}

/** Reads a design token (CSS custom property) from the document root. */
export function token(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

export function matches(query: string, ...fields: Array<string | null | undefined>): boolean {
  const q = query.trim().toLocaleLowerCase('tr-TR')
  if (!q) return true
  return fields.some((f) => f?.toLocaleLowerCase('tr-TR').includes(q))
}
