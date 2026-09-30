import { useEffect } from 'react'
import { create } from 'zustand'
import { api, errorMessage } from '@/services/api'
import type { Summary } from './types'

// One shared copy of the cached update summary, used by the dashboard
// widget, the sidebar badge and the page. Loading it only reads the
// server's cached state; it never starts a check.

const FRESH_MS = 60_000

interface SummaryStore {
  data: Summary | undefined
  error: string | null
  loading: boolean
  loadedAt: number
  load: (force?: boolean) => Promise<void>
}

let inFlight: Promise<void> | null = null

export const useSummaryStore = create<SummaryStore>((set, get) => ({
  data: undefined,
  error: null,
  loading: false,
  loadedAt: 0,
  load: (force = false) => {
    if (inFlight) return inFlight
    if (!force && get().loadedAt > 0 && Date.now() - get().loadedAt < FRESH_MS) return Promise.resolve()
    set({ loading: true })
    inFlight = api
      .get<Summary>('/updates/summary')
      .then((data) => set({ data, error: null, loading: false, loadedAt: Date.now() }))
      .catch((e: unknown) => set({ error: errorMessage(e), loading: false, loadedAt: Date.now() }))
      .finally(() => {
        inFlight = null
      })
    return inFlight
  },
}))

export function useSummary() {
  const data = useSummaryStore((s) => s.data)
  const error = useSummaryStore((s) => s.error)
  const loading = useSummaryStore((s) => s.loading)
  const load = useSummaryStore((s) => s.load)
  useEffect(() => {
    void load()
  }, [load])
  return { data, error, loading: loading && !data, reload: () => load(true) }
}

/** Total number of available updates, from cached data. Never triggers a check. */
export function useUpdateCount(): number {
  const total = useSummaryStore((s) => s.data?.total ?? 0)
  const load = useSummaryStore((s) => s.load)
  useEffect(() => {
    void load()
  }, [load])
  return total
}

export function refreshSummary(): void {
  void useSummaryStore.getState().load(true)
}
