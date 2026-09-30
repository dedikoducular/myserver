import { useCallback, useEffect, useRef, useState } from 'react'
import { api, errorMessage, type RequestOptions } from '@/services/api'

export interface QueryState<T> {
  data: T | undefined
  error: string | null
  /** True until the first response (or error) arrives. */
  loading: boolean
  /** True while any request is in flight, including reloads. */
  fetching: boolean
  reload: () => Promise<void>
}

export interface QueryOptions {
  query?: RequestOptions['query']
  /** Skip fetching while false. */
  enabled?: boolean
  /** Refetch period in ms. Use only for slow-changing data; live metrics
   *  must come from a stream (useEventSource / useSocket). Paused while the
   *  tab is hidden. */
  refetchInterval?: number
}

/** Fetches GET `path`, refetching when path or query changes. */
export function useQuery<T>(path: string | null, opts: QueryOptions = {}): QueryState<T> {
  const { enabled = true, refetchInterval } = opts
  const queryKey = opts.query ? JSON.stringify(opts.query) : ''
  const [data, setData] = useState<T>()
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [fetching, setFetching] = useState(false)
  const seq = useRef(0)
  const queryRef = useRef(opts.query)
  queryRef.current = opts.query

  const run = useCallback(
    async (signal?: AbortSignal) => {
      if (!path || !enabled) return
      const id = ++seq.current
      setFetching(true)
      try {
        const res = await api.get<T>(path, { query: queryRef.current, signal })
        if (id !== seq.current) return
        setData(res)
        setError(null)
      } catch (e) {
        if (e instanceof DOMException && e.name === 'AbortError') return
        if (id !== seq.current) return
        setError(errorMessage(e))
      } finally {
        if (id === seq.current) {
          setLoading(false)
          setFetching(false)
        }
      }
    },
    // queryKey stands in for the query object's contents.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [path, enabled, queryKey],
  )

  useEffect(() => {
    if (!path || !enabled) {
      setLoading(false)
      return
    }
    setLoading(true)
    const ctrl = new AbortController()
    void run(ctrl.signal)
    return () => ctrl.abort()
  }, [run, path, enabled])

  useEffect(() => {
    if (!refetchInterval || !path || !enabled) return
    const timer = window.setInterval(() => {
      if (!document.hidden) void run()
    }, refetchInterval)
    return () => window.clearInterval(timer)
  }, [refetchInterval, run, path, enabled])

  const reload = useCallback(() => run(), [run])
  return { data, error, loading, fetching, reload }
}

export interface ActionState<A extends unknown[], R> {
  run: (...args: A) => Promise<R | undefined>
  pending: boolean
  error: string | null
  clearError: () => void
}

/** Wraps a mutating call with pending/error state. `run` resolves to
 *  undefined when the call failed; the error is in `error`. */
export function useAction<A extends unknown[], R>(
  fn: (...args: A) => Promise<R>,
  handlers: { onSuccess?: (result: R) => void; onError?: (message: string) => void } = {},
): ActionState<A, R> {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const fnRef = useRef(fn)
  const handlersRef = useRef(handlers)
  fnRef.current = fn
  handlersRef.current = handlers

  const run = useCallback(async (...args: A) => {
    setPending(true)
    setError(null)
    try {
      const result = await fnRef.current(...args)
      handlersRef.current.onSuccess?.(result)
      return result
    } catch (e) {
      const message = errorMessage(e)
      setError(message)
      handlersRef.current.onError?.(message)
      return undefined
    } finally {
      setPending(false)
    }
  }, [])

  const clearError = useCallback(() => setError(null), [])
  return { run, pending, error, clearError }
}
