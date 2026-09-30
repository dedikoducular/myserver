import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { Copy, Download, Eraser, RefreshCw } from 'lucide-react'
import { Alert, Button, Modal, Select, Status, Switch } from '@/components/ui'
import { useEventSource } from '@/hooks/useStream'
import { api, errorMessage } from '@/services/api'
import { toast } from '@/stores/ui'
import { cx } from '@/lib/format'
import { t } from './strings'
import { stripAnsi } from './util'
import type { ContainerDetail, LogLine } from './types'

const MAX_LINES = 5000
const TAILS = [100, 200, 500, 1000, 2000]
const EVENTS = ['start', 'log', 'end']

interface Row {
  key: number
  stream: LogLine['stream']
  text: string
  time: number | null
}

const clock = new Intl.DateTimeFormat('tr-TR', { hour: '2-digit', minute: '2-digit', second: '2-digit' })

function isLogLine(v: unknown): v is LogLine {
  return typeof v === 'object' && v !== null && typeof (v as LogLine).line === 'string'
}

function rowText(r: Row): string {
  return r.time != null ? `${new Date(r.time * 1000).toISOString()} ${r.text}` : r.text
}

export function LogsModal({ container, onClose }: { container: { id: string; name: string }; onClose: () => void }) {
  const [tail, setTail] = useState(200)
  const [timestamps, setTimestamps] = useState(false)
  const [autoScroll, setAutoScroll] = useState(true)
  const [rows, setRows] = useState<Row[]>([])
  const [ended, setEnded] = useState<'stopped' | 'error' | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)

  const buffer = useRef<Row[]>([])
  const nextKey = useRef(1)
  const frame = useRef<number | null>(null)
  const resetOnNext = useRef(false)
  const viewport = useRef<HTMLDivElement>(null)
  const tailId = useId()

  const flush = useCallback(() => {
    frame.current = null
    const incoming = buffer.current
    buffer.current = []
    const reset = resetOnNext.current
    resetOnNext.current = false
    if (incoming.length === 0 && !reset) return
    setRows((prev) => {
      const next = reset ? incoming : prev.concat(incoming)
      return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next
    })
  }, [])

  const schedule = useCallback(() => {
    if (frame.current === null) frame.current = window.requestAnimationFrame(flush)
  }, [flush])

  useEffect(
    () => () => {
      if (frame.current !== null) window.cancelAnimationFrame(frame.current)
    },
    [],
  )

  const state = useEventSource(
    `/docker/containers/${encodeURIComponent(container.id)}/logs/stream`,
    (event, data) => {
      if (event === 'log' && isLogLine(data)) {
        buffer.current.push({
          key: nextKey.current++,
          stream: data.stream === 'stderr' ? 'stderr' : 'stdout',
          text: stripAnsi(data.line),
          time: data.time,
        })
        schedule()
      } else if (event === 'start') {
        // Every connection replays the tail, so the view starts over.
        buffer.current = []
        resetOnNext.current = true
        schedule()
      } else if (event === 'end') {
        const reason = (data as { reason?: string } | null)?.reason
        setEnded(reason === 'error' ? 'error' : 'stopped')
      }
    },
    { query: { tail, timestamps, attempt }, events: EVENTS, enabled: ended === null && failure === null },
  )

  // EventSource cannot show why a connection was refused; ask the API.
  useEffect(() => {
    if (state !== 'closed' || ended !== null || failure !== null || document.hidden) return
    let cancelled = false
    api
      .get<ContainerDetail>(`/docker/containers/${encodeURIComponent(container.id)}`)
      .then(() => {
        if (!cancelled) setFailure(t('logsFailed'))
      })
      .catch((e: unknown) => {
        if (!cancelled) setFailure(errorMessage(e, t('logsFailed')))
      })
    return () => {
      cancelled = true
    }
  }, [state, ended, failure, container.id])

  useEffect(() => {
    if (autoScroll && viewport.current) viewport.current.scrollTop = viewport.current.scrollHeight
  }, [rows, autoScroll])

  const restart = () => {
    setEnded(null)
    setFailure(null)
    setAttempt((n) => n + 1)
  }

  const text = () => rows.map(rowText).join('\n') + '\n'

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text())
      toast.success(t('logsCopied'))
    } catch {
      toast.error(t('logsCopyFailed'))
    }
  }

  const download = () => {
    const url = URL.createObjectURL(new Blob([text()], { type: 'text/plain;charset=utf-8' }))
    const a = document.createElement('a')
    a.href = url
    a.download = `${container.name}-${new Date().toISOString().replace(/[:.]/g, '-')}.log`
    document.body.appendChild(a)
    a.click()
    a.remove()
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  const onScroll = () => {
    const el = viewport.current
    if (!el || !autoScroll) return
    // Scrolling up to read pauses following.
    if (el.scrollHeight - el.scrollTop - el.clientHeight > 80) setAutoScroll(false)
  }

  return (
    <Modal open onClose={onClose} title={t('logsTitle', { name: container.name })} size="xl">
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
          <div className="flex items-center gap-2">
            <label htmlFor={tailId} className="text-xs text-muted">
              {t('logsTail')}
            </label>
            <Select
              id={tailId}
              value={tail}
              onChange={(e) => {
                setTail(Number(e.target.value))
                setEnded(null)
                setFailure(null)
              }}
              className="w-40"
            >
              {TAILS.map((n) => (
                <option key={n} value={n}>
                  {t('logsTailOption', { n })}
                </option>
              ))}
            </Select>
          </div>
          <div className="flex items-center gap-2 text-xs text-muted">
            <Switch
              checked={timestamps}
              onChange={(v) => {
                setTimestamps(v)
                setEnded(null)
                setFailure(null)
              }}
              label={t('logsTimestamps')}
            />
            <span aria-hidden>{t('logsTimestamps')}</span>
          </div>
          <div className="flex items-center gap-2 text-xs text-muted">
            <Switch checked={autoScroll} onChange={setAutoScroll} label={t('logsAutoScroll')} />
            <span aria-hidden>{t('logsAutoScroll')}</span>
          </div>
          <div className="ml-auto flex flex-wrap items-center gap-2">
            <Button icon={Copy} onClick={() => void copy()} disabled={rows.length === 0}>
              {t('logsCopy')}
            </Button>
            <Button icon={Download} onClick={download} disabled={rows.length === 0}>
              {t('logsDownload')}
            </Button>
            <Button icon={Eraser} variant="ghost" onClick={() => setRows([])} disabled={rows.length === 0}>
              {t('logsClear')}
            </Button>
          </div>
        </div>

        {failure && <Alert tone="danger">{failure}</Alert>}
        {ended && <Alert tone="warning">{ended === 'error' ? t('logsEndedError') : t('logsEnded')}</Alert>}

        <div
          ref={viewport}
          onScroll={onScroll}
          role="log"
          aria-label={t('logsRegion')}
          aria-live="off"
          tabIndex={0}
          className="h-[55dvh] overflow-auto rounded-xl border border-line bg-bg p-3 font-mono text-xs leading-5"
        >
          {rows.length === 0 ? (
            <p className="text-faint">{state === 'connecting' ? t('logsConnecting') : t('logsEmpty')}</p>
          ) : (
            rows.map((r) => (
              <div key={r.key} className={cx('break-all whitespace-pre-wrap', r.stream === 'stderr' ? 'text-warning' : 'text-fg')}>
                {r.time != null && <span className="mr-2 text-faint select-none">{clock.format(new Date(r.time * 1000))}</span>}
                {r.text}
              </div>
            ))
          )}
        </div>

        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted">
          <span>
            {t('logsLines', { n: rows.length })}
            {rows.length >= MAX_LINES && ' · ' + t('logsTruncated', { n: MAX_LINES })}
          </span>
          {ended || failure ? (
            <Button size="md" icon={RefreshCw} onClick={restart}>
              {t('retry')}
            </Button>
          ) : state === 'open' ? (
            <Status tone="success" pulse>
              {t('logsLive')}
            </Status>
          ) : (
            <Status tone="neutral">{t('logsConnecting')}</Status>
          )}
        </div>
      </div>
    </Modal>
  )
}
