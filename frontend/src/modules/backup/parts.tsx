import { useCallback, useRef, useState } from 'react'
import { ChevronDown, ChevronUp, X } from 'lucide-react'
import { Alert, Badge, Button, ProgressBar, Status, type Tone } from '@/components/ui'
import { useAction } from '@/hooks/useApi'
import { useEventSource, type StreamState } from '@/hooks/useStream'
import { cx, formatBytes, formatShortDateTime, formatTime } from '@/lib/format'
import { api } from '@/services/api'
import { toast } from '@/stores/ui'
import { t } from './strings'
import type { BackupRecord, BackupStatus, Job, JobKind, Schedule, Trigger } from './types'

export function statusTone(s: BackupStatus): Tone {
  switch (s) {
    case 'success':
      return 'success'
    case 'failed':
      return 'danger'
    case 'running':
      return 'accent'
    default:
      return 'warning'
  }
}

export function statusLabel(s: BackupStatus): string {
  switch (s) {
    case 'success':
      return t('stSuccess')
    case 'failed':
      return t('stFailed')
    case 'running':
      return t('stRunning')
    default:
      return t('stCancelled')
  }
}

export function triggerLabel(tr: Trigger): string {
  switch (tr) {
    case 'scheduled':
      return t('trScheduled')
    case 'safety':
      return t('trSafety')
    case 'imported':
      return t('trImported')
    default:
      return t('trManual')
  }
}

export function jobLabel(k: JobKind): string {
  switch (k) {
    case 'restore':
      return t('jobRestore')
    case 'verify':
      return t('jobVerify')
    case 'import':
      return t('jobImport')
    default:
      return t('jobBackup')
  }
}

function pad(n: number): string {
  return String(n).padStart(2, '0')
}

const dayKeys = ['d0', 'd1', 'd2', 'd3', 'd4', 'd5', 'd6'] as const

export function dayName(i: number): string {
  return t(dayKeys[i] ?? 'd0')
}

export function scheduleSummary(s: Schedule | null): string {
  if (!s || !s.enabled) return t('scheduleOff')
  const time = `${pad(s.hour)}:${pad(s.minute)}`
  if (s.frequency === 'weekly') return t('sumWeekly', { day: dayName(s.weekday), time })
  if (s.frequency === 'monthly') return t('sumMonthly', { day: s.monthday, time })
  return t('sumDaily', { time })
}

/** Status of a record, taking a missing file into account. */
export function RecordStatus({ rec }: { rec: BackupRecord }) {
  if (rec.status === 'success' && rec.file_missing) return <Status tone="danger">{t('stMissing')}</Status>
  return (
    <Status tone={statusTone(rec.status)} pulse={rec.status === 'running'}>
      {statusLabel(rec.status)}
    </Status>
  )
}

export function RecordBadges({ rec }: { rec: BackupRecord }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      <Badge tone="neutral">{triggerLabel(rec.trigger)}</Badge>
      {rec.status === 'success' && (
        <>
          <Badge tone={rec.consistency === 'live' ? 'warning' : 'success'}>
            {rec.consistency === 'live' ? t('consLiveShort') : t('consStoppedShort')}
          </Badge>
          <Badge tone={rec.encrypted ? 'cyan' : 'neutral'}>{rec.encrypted ? t('encrypted') : t('unencrypted')}</Badge>
          {rec.warnings.length > 0 && <Badge tone="warning">{t('warningsCount', { count: rec.warnings.length })}</Badge>}
        </>
      )}
    </span>
  )
}

/** Anchor styled like a secondary button; used for downloads. */
export function LinkButton({ href, children, size = 'md', className }: { href: string; children: React.ReactNode; size?: 'sm' | 'md'; className?: string }) {
  return (
    <a
      href={href}
      download
      className={cx(
        'inline-flex select-none items-center justify-center border border-line bg-raised font-medium whitespace-nowrap text-fg transition-colors hover:border-line-strong',
        size === 'sm' ? 'h-8 gap-1.5 rounded-lg px-3 text-xs' : 'h-10 gap-2 rounded-xl px-4 text-sm',
        className,
      )}
    >
      {children}
    </a>
  )
}

/** Live list of jobs from the server-sent event stream. `onFinished` runs
 *  when a job that was running is reported as finished. */
export function useJobs(enabled: boolean, initial: Job[] | undefined, onFinished: (job: Job) => void): { jobs: Job[]; stream: StreamState } {
  const [jobs, setJobs] = useState<Job[] | null>(null)
  const running = useRef<Set<string>>(new Set())
  const cb = useRef(onFinished)
  cb.current = onFinished

  const onEvent = useCallback((_: string, data: unknown) => {
    if (!Array.isArray(data)) return
    const list = data as Job[]
    for (const j of list) {
      if (j.status === 'running') running.current.add(j.id)
      else if (running.current.delete(j.id)) cb.current(j)
    }
    setJobs(list)
  }, [])
  const stream = useEventSource(enabled ? '/backup/stream' : null, onEvent, { events: ['jobs'] })
  return { jobs: jobs ?? initial ?? [], stream }
}

export function JobCard({ job, compact = false }: { job: Job; compact?: boolean }) {
  const [open, setOpen] = useState(false)
  const cancel = useAction(() => api.post<Job>(`/backup/jobs/${job.id}/cancel`), {
    onSuccess: () => toast.info(t('jobCancelled')),
    onError: (m) => toast.error(m),
  })
  const active = job.status === 'running'
  const percent = job.bytes_total > 0 ? Math.min(100, (job.bytes_done / job.bytes_total) * 100) : null
  const logs = open ? job.logs : job.logs.slice(-3)

  return (
    <div className="rounded-xl border border-line bg-surface p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="truncate text-sm font-medium text-fg">
            {jobLabel(job.kind)}
            {job.name ? ` · ${job.name}` : ''}
          </p>
          <p className="text-xs text-muted">
            {formatShortDateTime(job.started_at)}
            {job.kind === 'backup' ? ` · ${triggerLabel(job.trigger)}` : ''}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Status tone={statusTone(job.status)} pulse={active}>
            {statusLabel(job.status)}
          </Status>
          {active && job.cancellable && (
            <Button size="sm" variant="danger" icon={X} loading={cancel.pending} disabled={job.cancel_requested} onClick={() => void cancel.run()}>
              {job.cancel_requested ? t('jobCancelRequested') : t('jobCancel')}
            </Button>
          )}
        </div>
      </div>

      {active && (
        <div className="mt-3 flex flex-col gap-1.5">
          <p className="break-words text-xs text-fg">{job.step}</p>
          {percent !== null ? (
            <ProgressBar value={percent} tone="accent" label={job.step} />
          ) : (
            <div className="h-1.5 w-full overflow-hidden rounded-full bg-raised" aria-hidden>
              <div className="h-full w-1/3 animate-pulse rounded-full bg-accent" />
            </div>
          )}
          <p className="text-xs text-muted">
            {job.bytes_total > 0
              ? t('jobProcessedOf', { done: formatBytes(job.bytes_done), total: formatBytes(job.bytes_total) })
              : t('jobProcessed', { done: formatBytes(job.bytes_done) })}
          </p>
        </div>
      )}
      {active && !job.cancellable && job.kind === 'restore' && (
        <Alert tone="warning" className="mt-3">
          {t('jobNotCancellable')}
        </Alert>
      )}
      {job.status === 'failed' && job.error && (
        <Alert tone="danger" className="mt-3">
          {job.error}
        </Alert>
      )}

      {!compact && job.logs.length > 0 && (
        <div className="mt-3">
          <ul className="flex flex-col gap-0.5 font-mono text-[11px] leading-relaxed">
            {logs.map((l, i) => (
              <li
                key={`${l.time}-${i}`}
                className={cx('break-words', l.level === 'error' ? 'text-danger' : l.level === 'warning' ? 'text-warning' : 'text-muted')}
              >
                <span className="text-faint">{formatTime(l.time)}</span> {l.message}
              </li>
            ))}
          </ul>
          {job.logs.length > 3 && (
            <button
              type="button"
              onClick={() => setOpen((v) => !v)}
              aria-expanded={open}
              className="mt-1 inline-flex min-h-8 items-center gap-1 text-xs text-accent hover:underline"
            >
              {open ? <ChevronUp className="size-3.5" aria-hidden /> : <ChevronDown className="size-3.5" aria-hidden />}
              {t('jobLogs')}
            </button>
          )}
        </div>
      )}
    </div>
  )
}
