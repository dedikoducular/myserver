import { useCallback, useEffect, useId } from 'react'
import { create } from 'zustand'
import { useEventSource, type StreamState } from '@/hooks/useStream'
import { api, errorMessage } from '@/services/api'
import type { FinePoint, MetricsPayload, NetPoint, Snapshot, SystemInfo } from './types'

const FINE_LIMIT = 60
const NET_LIMIT = 240
const INFO_MAX_AGE_MS = 60_000
const STREAM_EVENTS = ['history', 'metrics', 'network']

interface SystemState {
  snapshot: Snapshot | null
  history: FinePoint[]
  /** Coarse network points received since the stream opened. */
  netLive: NetPoint[]
  stream: StreamState
  /** Hook instances using the stream; the first one owns the connection. */
  consumers: string[]
  retry: number

  info: SystemInfo | null
  infoError: string | null
  infoLoading: boolean
  infoAt: number
}

export const useSystemStore = create<SystemState>(() => ({
  snapshot: null,
  history: [],
  netLive: [],
  stream: 'connecting',
  consumers: [],
  retry: 0,
  info: null,
  infoError: null,
  infoLoading: false,
  infoAt: 0,
}))

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null
}

function toPoint(s: Snapshot): FinePoint {
  return {
    t: s.time,
    cpu: s.cpu.percent ?? 0,
    memory: s.memory.percent,
    disk: s.root_disk ? s.root_disk.percent : null,
    temperature: s.temperature.cpu,
    rx_rate: s.network.rx_rate,
    tx_rate: s.network.tx_rate,
  }
}

function handleEvent(name: string, data: unknown): void {
  if (!isObject(data)) return
  if (name === 'history') {
    const payload = data as unknown as MetricsPayload
    useSystemStore.setState({
      snapshot: payload.snapshot ?? null,
      history: Array.isArray(payload.history) ? payload.history.slice(-FINE_LIMIT) : [],
    })
  } else if (name === 'metrics') {
    const snapshot = data as unknown as Snapshot
    useSystemStore.setState((s) => ({
      snapshot,
      history: [...s.history, toPoint(snapshot)].slice(-FINE_LIMIT),
    }))
  } else if (name === 'network') {
    const point = data as unknown as NetPoint
    useSystemStore.setState((s) => ({ netLive: [...s.netLive, point].slice(-NET_LIMIT) }))
  }
}

function join(id: string): void {
  useSystemStore.setState((s) => (s.consumers.includes(id) ? s : { consumers: [...s.consumers, id] }))
}

function leave(id: string): void {
  useSystemStore.setState((s) => {
    const consumers = s.consumers.filter((c) => c !== id)
    return consumers.length === 0 ? { consumers, stream: 'closed' as StreamState } : { consumers }
  })
}

export interface SystemMetrics {
  snapshot: Snapshot | null
  history: FinePoint[]
  netLive: NetPoint[]
  stream: StreamState
  /** Reopens the stream after it was closed by an error. */
  reconnect: () => void
}

/** Live host metrics. Every component on the page shares one SSE
 *  connection: the first mounted consumer opens it, the others only read
 *  the store. */
export function useSystemMetrics(): SystemMetrics {
  const id = useId()
  const owner = useSystemStore((s) => s.consumers[0] === id)
  const retry = useSystemStore((s) => s.retry)

  useEffect(() => {
    join(id)
    return () => leave(id)
  }, [id])

  const state = useEventSource(owner ? '/system/stream' : null, handleEvent, {
    events: STREAM_EVENTS,
    query: retry > 0 ? { retry } : undefined,
  })

  useEffect(() => {
    if (owner) useSystemStore.setState({ stream: state })
  }, [owner, state])

  const snapshot = useSystemStore((s) => s.snapshot)
  const history = useSystemStore((s) => s.history)
  const netLive = useSystemStore((s) => s.netLive)
  const stream = useSystemStore((s) => s.stream)
  const reconnect = useCallback(() => useSystemStore.setState((s) => ({ retry: s.retry + 1, stream: 'connecting' })), [])
  return { snapshot, history, netLive, stream, reconnect }
}

let infoRequest: Promise<void> | null = null

function loadInfo(force = false): Promise<void> {
  const s = useSystemStore.getState()
  if (infoRequest) return infoRequest
  if (!force && s.info && Date.now() - s.infoAt < INFO_MAX_AGE_MS) return Promise.resolve()
  useSystemStore.setState({ infoLoading: true })
  infoRequest = api
    .get<SystemInfo>('/system/info')
    .then((info) => useSystemStore.setState({ info, infoError: null, infoAt: Date.now() }))
    .catch((e: unknown) => useSystemStore.setState({ infoError: errorMessage(e) }))
    .finally(() => {
      infoRequest = null
      useSystemStore.setState({ infoLoading: false })
    })
  return infoRequest
}

export interface SystemInfoState {
  info: SystemInfo | null
  error: string | null
  loading: boolean
  reload: () => void
}

/** Host description, fetched once and shared by every widget. */
export function useSystemInfo(): SystemInfoState {
  const info = useSystemStore((s) => s.info)
  const infoError = useSystemStore((s) => s.infoError)
  const infoLoading = useSystemStore((s) => s.infoLoading)
  useEffect(() => {
    void loadInfo()
  }, [])
  const reload = useCallback(() => void loadInfo(true), [])
  const error = info || infoLoading ? null : infoError
  return { info, error, loading: !info && !error, reload }
}
