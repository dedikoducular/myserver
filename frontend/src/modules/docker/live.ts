import { useCallback, useEffect, useRef, useState } from 'react'
import { useEventSource, type StreamState } from '@/hooks/useStream'
import type { ChangeEvent, ChangeKind, ContainerStats, StatsSnapshot } from './types'

export interface DockerLive {
  /** Latest usage per container ID. */
  stats: ReadonlyMap<string, ContainerStats>
  /** false while the backend cannot reach Docker; null until the first sample. */
  available: boolean | null
  /** Counters that increase when objects of a kind change; use as reload triggers. */
  revision: Record<ChangeKind, number>
  stream: StreamState
}

const EVENTS = ['stats', 'changed']
const CHANGE_DELAY = 400

const emptyStats: ReadonlyMap<string, ContainerStats> = new Map()
const zeroRevision: Record<ChangeKind, number> = { container: 0, image: 0, volume: 0, network: 0 }

function isSnapshot(v: unknown): v is StatsSnapshot {
  return typeof v === 'object' && v !== null && Array.isArray((v as StatsSnapshot).items)
}

function isChange(v: unknown): v is ChangeEvent {
  return typeof v === 'object' && v !== null && typeof (v as ChangeEvent).kind === 'string'
}

/** Subscribes to the docker live stream: resource usage of running
 *  containers and change notifications that tell lists to reload. */
export function useDockerLive(enabled = true): DockerLive {
  const [stats, setStats] = useState(emptyStats)
  const [available, setAvailable] = useState<boolean | null>(null)
  const [revision, setRevision] = useState(zeroRevision)
  const pending = useRef(new Set<ChangeKind>())
  const timer = useRef<number | null>(null)
  const lastAvailable = useRef<boolean | null>(null)

  const bump = useCallback((kinds: ChangeKind[]) => {
    for (const k of kinds) pending.current.add(k)
    if (timer.current !== null) return
    // A single operation produces a burst of events; reload once.
    timer.current = window.setTimeout(() => {
      timer.current = null
      const kinds = [...pending.current]
      pending.current.clear()
      setRevision((r) => {
        const next = { ...r }
        for (const k of kinds) next[k] = r[k] + 1
        return next
      })
    }, CHANGE_DELAY)
  }, [])

  useEffect(
    () => () => {
      if (timer.current !== null) window.clearTimeout(timer.current)
    },
    [],
  )

  const stream = useEventSource(
    enabled ? '/docker/stats/stream' : null,
    (event, data) => {
      if (event === 'stats' && isSnapshot(data)) {
        setStats(new Map(data.items.map((s) => [s.id, s])))
        // Docker came back or went away: every list is out of date.
        if (lastAvailable.current !== null && lastAvailable.current !== data.available) {
          bump(['container', 'image', 'volume', 'network'])
        }
        lastAvailable.current = data.available
        setAvailable(data.available)
      } else if (event === 'changed' && isChange(data)) {
        if (data.kind in zeroRevision) bump([data.kind])
      }
    },
    { events: EVENTS },
  )

  // After the stream reconnects (tab shown again, network back) events may
  // have been missed.
  const wasOpen = useRef(false)
  useEffect(() => {
    if (stream === 'open') {
      if (wasOpen.current) bump(['container', 'image', 'volume', 'network'])
      wasOpen.current = true
    }
  }, [stream, bump])

  return { stats, available, revision, stream }
}

/** Current Unix time, refreshed by a local clock so durations keep counting
 *  without asking the server. */
export function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000))
  useEffect(() => {
    const id = window.setInterval(() => {
      if (!document.hidden) setNow(Math.floor(Date.now() / 1000))
    }, intervalMs)
    return () => window.clearInterval(id)
  }, [intervalMs])
  return now
}

/** Calls `reload` whenever `revision` increases. */
export function useReloadOn(revision: number, reload: () => Promise<void>): void {
  const seen = useRef(revision)
  const fn = useRef(reload)
  fn.current = reload
  useEffect(() => {
    if (revision !== seen.current) {
      seen.current = revision
      void fn.current()
    }
  }, [revision])
}
