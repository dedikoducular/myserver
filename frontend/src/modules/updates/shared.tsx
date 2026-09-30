import { useEffect, useRef, useState, type ReactNode } from 'react'
import { RefreshCw, type LucideIcon } from 'lucide-react'
import { Alert, Badge, Button, Card, CardHeader, Spinner, type Tone } from '@/components/ui'
import { useEventSource } from '@/hooks/useStream'
import { formatRelative, formatShortDateTime } from '@/lib/format'
import { t } from './strings'
import type { JobMeta, JobStatus, StateError } from './types'

export function checkedText(checkedAt: number | null, autoCheck: boolean): string {
  const base = checkedAt ? t('lastCheck', { time: formatRelative(checkedAt) }) : t('neverChecked')
  return autoCheck ? base : `${base} · ${t('autoOff')}`
}

/** Card frame shared by the three areas: title, last check, "Denetle". */
export function AreaCard({
  title,
  icon,
  subtitle,
  badge,
  canCheck,
  checking,
  onCheck,
  error,
  children,
}: {
  title: string
  icon: LucideIcon
  subtitle: string
  badge?: ReactNode
  canCheck: boolean
  checking: boolean
  onCheck: () => void
  error?: StateError | null
  children: ReactNode
}) {
  return (
    <Card>
      <CardHeader
        title={
          <span className="inline-flex items-center gap-2">
            {title}
            {badge}
          </span>
        }
        icon={icon}
        subtitle={subtitle}
        actions={
          <Button
            size="md"
            icon={RefreshCw}
            loading={checking}
            disabled={!canCheck}
            title={canCheck ? undefined : t('adminOnly')}
            onClick={onCheck}
          >
            {checking ? t('checking') : t('check')}
          </Button>
        }
      />
      {error && (
        <Alert tone="warning" className="mb-4">
          {error.message}
        </Alert>
      )}
      {children}
    </Card>
  )
}

const statusTone: Record<JobStatus, Tone> = { running: 'accent', success: 'success', failed: 'danger' }

export function JobBadge({ status }: { status: JobStatus }) {
  const label = status === 'running' ? t('jobRunning') : status === 'success' ? t('jobSuccess') : t('jobFailed')
  return <Badge tone={statusTone[status]}>{label}</Badge>
}

interface StartEvent {
  job: JobMeta
  from: number
}
interface LinesEvent {
  from: number
  lines: string[]
}

/** Follows a job's output over SSE. Works for running and finished jobs. */
export function JobOutput({ job, onDone }: { job: JobMeta; onDone?: (job: JobMeta) => void }) {
  const [lines, setLines] = useState<string[]>([])
  const [meta, setMeta] = useState<JobMeta>(job)
  const [done, setDone] = useState(false)
  const box = useRef<HTMLPreElement>(null)
  const stick = useRef(true)
  const doneRef = useRef(onDone)
  doneRef.current = onDone

  useEffect(() => {
    setLines([])
    setMeta(job)
    setDone(false)
    // Only the job's identity restarts the stream.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [job.id])

  const state = useEventSource(
    `/updates/jobs/${job.id}/stream`,
    (event, data) => {
      if (event === 'start') {
        const e = data as StartEvent
        setMeta(e.job)
        if (e.from === 0) setLines([])
      } else if (event === 'lines') {
        const e = data as LinesEvent
        setLines((prev) => prev.slice(0, e.from).concat(e.lines))
      } else if (event === 'done') {
        const m = data as JobMeta
        setMeta(m)
        setDone(true)
        doneRef.current?.(m)
      }
    },
    { events: ['start', 'lines', 'done'], enabled: !done },
  )

  useEffect(() => {
    const el = box.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [lines])

  return (
    <div className="rounded-xl border border-line bg-surface">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-line px-3 py-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2 text-xs text-muted">
          <span className="font-medium text-fg">{t('outputTitle')}</span>
          <JobBadge status={meta.status} />
          <span className="truncate">{meta.detail}</span>
        </div>
        <div className="flex items-center gap-2 text-xs text-faint">
          {meta.status === 'running' && state === 'open' && (
            <span className="inline-flex items-center gap-1.5 text-accent">
              <Spinner className="size-3.5 text-accent" label={t('outputLive')} />
              {t('outputLive')}
            </span>
          )}
          <span>{t('jobStartedBy', { user: meta.username || '—', time: formatShortDateTime(meta.started_at) })}</span>
        </div>
      </div>
      <pre
        ref={box}
        role="log"
        aria-live="off"
        tabIndex={0}
        onScroll={(e) => {
          const el = e.currentTarget
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
        }}
        className="max-h-80 min-h-24 overflow-auto px-3 py-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words text-muted"
      >
        {lines.length === 0 ? t('outputWaiting') : lines.join('\n')}
      </pre>
      {meta.status === 'failed' && meta.message && (
        <div className="border-t border-line p-3">
          <Alert tone="danger">{meta.message}</Alert>
        </div>
      )}
    </div>
  )
}
