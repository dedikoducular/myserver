import { useCallback, useState } from 'react'
import type { Tone } from '@/components/ui'
import { apiUrl } from '@/services/api'
import { useQuery } from '@/hooks/useApi'
import { useEventSource } from '@/hooks/useStream'
import { t } from './strings'
import type { AppState, CatalogResponse, InstalledApp, InstalledResponse, JobKind, ViewMode, WebUI } from './types'

export function iconUrl(icon: string): string | null {
  return /^[a-z0-9][a-z0-9_-]{0,62}\.(svg|png)$/.test(icon) ? apiUrl('/apps/icons/' + icon) : null
}

/** Address of an application's web interface on the host the panel is
 *  currently reached through. */
export function webUrl(ui: WebUI): string {
  const scheme = ui.scheme === 'https' ? 'https' : 'http'
  const path = ui.path.startsWith('/') ? ui.path : '/' + ui.path
  return `${scheme}://${window.location.hostname}:${ui.port}${path}`
}

export function openWebUI(ui: WebUI): void {
  window.open(webUrl(ui), '_blank', 'noopener,noreferrer')
}

export function stateInfo(state: AppState): { label: string; tone: Tone; pulse: boolean } {
  switch (state) {
    case 'running':
      return { label: t('stateRunning'), tone: 'success', pulse: false }
    case 'stopped':
      return { label: t('stateStopped'), tone: 'danger', pulse: false }
    case 'starting':
      return { label: t('stateStarting'), tone: 'warning', pulse: true }
    case 'unhealthy':
      return { label: t('stateUnhealthy'), tone: 'danger', pulse: false }
    case 'partial':
      return { label: t('statePartial'), tone: 'warning', pulse: false }
    case 'missing':
      return { label: t('stateMissing'), tone: 'danger', pulse: false }
    default:
      return { label: t('stateUnknown'), tone: 'neutral', pulse: false }
  }
}

export function operationLabel(op: string): string {
  switch (op) {
    case 'install':
      return t('opInstall')
    case 'update':
      return t('opUpdate')
    case 'settings':
      return t('opSettings')
    case 'restore':
      return t('opRestore')
    case 'start':
      return t('opStart')
    case 'stop':
      return t('opStop')
    case 'restart':
      return t('opRestart')
    case 'uninstall':
      return t('opUninstall')
    default:
      return t('opBusy')
  }
}

export function jobKindLabel(kind: JobKind): string {
  switch (kind) {
    case 'install':
      return t('jobInstall')
    case 'update':
      return t('jobUpdate')
    case 'settings':
      return t('jobSettings')
    default:
      return t('jobRestore')
  }
}

export function isUp(app: InstalledApp): boolean {
  return app.state === 'running' || app.state === 'starting' || app.state === 'unhealthy' || app.state === 'partial'
}

export function matches(app: { name: string; description: string; slug: string }, search: string): boolean {
  const q = search.trim().toLocaleLowerCase('tr')
  if (!q) return true
  return [app.name, app.description, app.slug].some((v) => v.toLocaleLowerCase('tr').includes(q))
}

const VIEW_KEY = 'myserver.apps.view'

/** Grid/list preference, remembered in this browser. */
export function useViewMode(): [ViewMode, (mode: ViewMode) => void] {
  const [mode, setMode] = useState<ViewMode>(() => {
    try {
      return localStorage.getItem(VIEW_KEY) === 'list' ? 'list' : 'grid'
    } catch {
      return 'grid'
    }
  })
  const update = useCallback((next: ViewMode) => {
    setMode(next)
    try {
      localStorage.setItem(VIEW_KEY, next)
    } catch {
      // Storage unavailable: the choice lasts for this page load.
    }
  }, [])
  return [mode, update]
}

/** Installed applications, refreshed when the server reports a change.
 *  `onChange` runs on every reported change, for data loaded elsewhere. */
export function useInstalledApps(onChange?: () => void) {
  const query = useQuery<InstalledResponse>('/apps/installed')
  const { reload } = query
  useEventSource(
    '/apps/events',
    () => {
      void reload()
      onChange?.()
    },
    { events: ['changed'] },
  )
  return query
}

export function useCatalog(enabled: boolean) {
  return useQuery<CatalogResponse>('/apps/catalog', { enabled })
}

/** "0.0.0.0:8096", "127.0.0.1:53/udp": where a port is published. */
export function portAddress(p: { host_ip: string; host: number; protocol: string }): string {
  return `${p.host_ip}:${p.host}` + (p.protocol === 'udp' ? '/udp' : '')
}
