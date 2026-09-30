import { useEffect, useMemo, useRef, useState } from 'react'
import { CheckCircle2, Loader2, XCircle } from 'lucide-react'
import { Alert, Button, Modal, ProgressBar } from '@/components/ui'
import { useEventSource } from '@/hooks/useStream'
import { cx, formatBytes } from '@/lib/format'
import { t } from './strings'
import type { Job, JobEvent } from './types'
import { jobKindLabel } from './util'

interface Step {
  seq: number
  message: string
}

interface Download {
  image: string
  current: number
  total: number
}

/** Follows a job over SSE. The job runs on the server, so closing this view
 *  does not stop it. */
export function JobProgress({ jobId, onFinished }: { jobId: string; onFinished?: (job: Job) => void }) {
  const [events, setEvents] = useState<JobEvent[]>([])
  const [result, setResult] = useState<Job | null>(null)
  const [attempt, setAttempt] = useState(0)
  const lastSeq = useRef(0)
  const finishedRef = useRef(onFinished)
  finishedRef.current = onFinished
  const logEnd = useRef<HTMLDivElement>(null)

  useEffect(() => {
    setEvents([])
    setResult(null)
    lastSeq.current = 0
  }, [jobId])

  const state = useEventSource(
    `/apps/jobs/${jobId}/stream`,
    (name, data) => {
      if (name === 'finished') {
        const job = data as Job
        setResult(job)
        finishedRef.current?.(job)
        return
      }
      const ev = data as JobEvent
      // A reopened stream replays from the start; keep only new events.
      if (typeof ev?.seq !== 'number' || ev.seq <= lastSeq.current) return
      lastSeq.current = ev.seq
      setEvents((prev) => [...prev, ev])
    },
    { events: ['progress', 'finished'], enabled: result === null, query: { attempt } },
  )

  const { steps, downloads, logs } = useMemo(() => {
    const steps: Step[] = []
    const byImage = new Map<string, Download>()
    const logs: JobEvent[] = []
    for (const ev of events) {
      if (ev.type === 'step') steps.push({ seq: ev.seq, message: ev.message })
      else if (ev.type === 'progress' && ev.image) byImage.set(ev.image, { image: ev.image, current: ev.current ?? 0, total: ev.total ?? 0 })
      else if (ev.type === 'log') logs.push(ev)
    }
    return { steps, downloads: Array.from(byImage.values()), logs: logs.slice(-200) }
  }, [events])

  useEffect(() => {
    logEnd.current?.scrollIntoView({ block: 'nearest' })
  }, [logs.length])

  const running = result === null
  const failed = result?.status === 'failed'

  return (
    <div className="flex flex-col gap-4">
      {running && <Alert tone="accent">{t('jobRunning')}</Alert>}
      {result?.status === 'success' && <Alert tone="success">{t('jobSuccess')}</Alert>}
      {failed && (
        <Alert tone="danger" title={t('jobFailed')}>
          {result.error}
        </Alert>
      )}
      {running && state === 'closed' && !document.hidden && (
        <Alert tone="warning">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span>{t('jobStreamLost')}</span>
            <Button size="sm" onClick={() => setAttempt((n) => n + 1)}>
              {t('jobRetry')}
            </Button>
          </div>
        </Alert>
      )}

      {steps.length === 0 && running ? (
        <p className="flex items-center gap-2 text-sm text-muted">
          <Loader2 className="ms-spin size-4" aria-hidden />
          {t('jobWaiting')}
        </p>
      ) : (
        <ol className="flex flex-col gap-2">
          {steps.map((step, i) => {
            const last = i === steps.length - 1
            const active = last && running
            const broken = last && failed
            return (
              <li key={step.seq} className="flex items-start gap-2.5 text-sm">
                {active ? (
                  <Loader2 className="ms-spin mt-0.5 size-4 shrink-0 text-accent" aria-hidden />
                ) : broken ? (
                  <XCircle className="mt-0.5 size-4 shrink-0 text-danger" aria-hidden />
                ) : (
                  <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-success" aria-hidden />
                )}
                <span className={cx('min-w-0 break-words', active ? 'font-medium text-fg' : 'text-muted')}>{step.message}</span>
              </li>
            )
          })}
        </ol>
      )}

      {downloads.length > 0 && (
        <div className="flex flex-col gap-3 rounded-xl border border-line bg-surface p-3">
          {downloads.map((d) => {
            const percent = d.total > 0 ? (d.current / d.total) * 100 : 0
            return (
              <div key={d.image} className="flex flex-col gap-1.5">
                <div className="flex items-baseline justify-between gap-3 text-xs">
                  <span className="min-w-0 truncate font-mono text-fg">{d.image}</span>
                  <span className="shrink-0 text-muted">
                    {t('downloadProgress', { current: formatBytes(d.current), total: formatBytes(d.total) })}
                  </span>
                </div>
                <ProgressBar value={percent} tone={percent >= 100 ? 'success' : 'accent'} label={d.image} />
              </div>
            )
          })}
        </div>
      )}

      {logs.length > 0 && (
        <details className="rounded-xl border border-line bg-surface" open={failed}>
          <summary className="cursor-pointer px-3 py-2 text-xs font-medium text-muted">{t('jobDetails')}</summary>
          <div className="max-h-48 overflow-auto border-t border-line px-3 py-2 font-mono text-[11px] leading-relaxed text-muted">
            {logs.map((l) => (
              <p key={l.seq} className="break-all whitespace-pre-wrap">
                {l.service ? `[${l.service}] ` : ''}
                {l.message}
              </p>
            ))}
            <div ref={logEnd} />
          </div>
        </details>
      )}
    </div>
  )
}

/** Progress of a job in its own dialog (updates, reopened installs). */
export function JobDialog({ job, onClose, onFinished }: { job: Pick<Job, 'id' | 'name' | 'kind'> | null; onClose: () => void; onFinished?: (job: Job) => void }) {
  return (
    <Modal
      open={job !== null}
      onClose={onClose}
      size="lg"
      title={job ? t('jobTitle', { name: job.name, kind: jobKindLabel(job.kind) }) : ''}
      footer={<Button onClick={onClose}>{t('close')}</Button>}
    >
      {job && <JobProgress jobId={job.id} onFinished={onFinished} />}
    </Modal>
  )
}
