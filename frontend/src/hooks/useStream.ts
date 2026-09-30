import { useEffect, useRef, useState } from 'react'
import { apiUrl, wsUrl, type RequestOptions } from '@/services/api'

export type StreamState = 'connecting' | 'open' | 'closed'

export interface EventSourceOptions {
  query?: RequestOptions['query']
  enabled?: boolean
  /** Named SSE events to listen for; defaults to the unnamed "message". */
  events?: string[]
}

/** Subscribes to a Server-Sent Events endpoint. The browser reconnects by
 *  itself; the stream is closed while the tab is hidden to save resources. */
export function useEventSource(
  path: string | null,
  onEvent: (event: string, data: unknown) => void,
  opts: EventSourceOptions = {},
): StreamState {
  const { enabled = true } = opts
  const [state, setState] = useState<StreamState>('connecting')
  const handler = useRef(onEvent)
  handler.current = onEvent
  const queryKey = opts.query ? JSON.stringify(opts.query) : ''
  const eventsKey = (opts.events ?? ['message']).join(',')

  useEffect(() => {
    if (!path || !enabled) {
      setState('closed')
      return
    }
    let source: EventSource | null = null

    const open = () => {
      if (source) return
      setState('connecting')
      const query = queryKey ? (JSON.parse(queryKey) as RequestOptions['query']) : undefined
      source = new EventSource(apiUrl(path, query), { withCredentials: true })
      source.onopen = () => setState('open')
      source.onerror = () => setState(source?.readyState === EventSource.CLOSED ? 'closed' : 'connecting')
      for (const name of eventsKey.split(',')) {
        source.addEventListener(name, (e) => {
          const raw = (e as MessageEvent<string>).data
          let data: unknown = raw
          try {
            data = JSON.parse(raw)
          } catch {
            // Plain-text events (log lines) are passed through as strings.
          }
          handler.current(name, data)
        })
      }
    }
    const close = () => {
      source?.close()
      source = null
    }
    const onVisibility = () => {
      if (document.hidden) {
        close()
        setState('closed')
      } else {
        open()
      }
    }

    if (!document.hidden) open()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      close()
    }
  }, [path, enabled, queryKey, eventsKey])

  return state
}

export interface SocketHandle {
  state: StreamState
  send: (data: string | Blob | BufferSource) => void
  close: () => void
}

export interface SocketOptions {
  query?: RequestOptions['query']
  enabled?: boolean
  binaryType?: BinaryType
  onMessage: (data: string | ArrayBuffer | Blob) => void
  onOpen?: () => void
  onClose?: (event: CloseEvent) => void
}

/** Opens a WebSocket to an API path. It does not reconnect: interactive
 *  sessions (terminal, exec) must be restarted deliberately by the user. */
export function useSocket(path: string | null, opts: SocketOptions): SocketHandle {
  const { enabled = true, binaryType = 'arraybuffer' } = opts
  const [state, setState] = useState<StreamState>('connecting')
  const socket = useRef<WebSocket | null>(null)
  const handlers = useRef(opts)
  handlers.current = opts
  const queryKey = opts.query ? JSON.stringify(opts.query) : ''

  useEffect(() => {
    if (!path || !enabled) {
      setState('closed')
      return
    }
    setState('connecting')
    const query = queryKey ? (JSON.parse(queryKey) as RequestOptions['query']) : undefined
    const ws = new WebSocket(wsUrl(path, query))
    ws.binaryType = binaryType
    socket.current = ws
    ws.onopen = () => {
      setState('open')
      handlers.current.onOpen?.()
    }
    ws.onmessage = (e) => handlers.current.onMessage(e.data as string | ArrayBuffer | Blob)
    ws.onclose = (e) => {
      setState('closed')
      handlers.current.onClose?.(e)
    }
    return () => {
      ws.onopen = ws.onmessage = ws.onclose = null
      ws.close()
      socket.current = null
    }
  }, [path, enabled, queryKey, binaryType])

  return {
    state,
    send: (data) => {
      if (socket.current?.readyState === WebSocket.OPEN) socket.current.send(data)
    },
    close: () => socket.current?.close(),
  }
}
