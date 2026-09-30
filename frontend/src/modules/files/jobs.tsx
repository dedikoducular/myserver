import { useCallback, useEffect, useRef, useState } from 'react'
import { CheckCircle2, Loader2, X, XCircle } from 'lucide-react'
import { Card, CardHeader, IconButton, ProgressBar } from '@/components/ui'
import { useEventSource } from '@/hooks/useStream'
import { formatBytes } from '@/lib/format'
import { api, errorMessage } from '@/services/api'
import { toast } from '@/stores/ui'
import { t } from './strings'
import type { Job, JobKind } from './types'

export function jobKindLabel(kind: JobKind): string {
  switch (kind) {
    case 'copy':
      return t('jobCopy')
    case 'move':
      return t('jobMove')
    case 'delete':
      return t('jobDelete')
    case 'zip':
      return t('jobZip')
    case 'extract':
      return t('jobExtract')
    default:
      return t('jobSize')
  }
}

export interface JobsHandle {
  jobs: Job[]
  /** Call after starting a job so its progress is streamed. */
  watch: (job?: Job) => void
  cancel: (id: string) => void
  dismiss: (id: string) => void
}

/** Follows file jobs over SSE. The stream is only kept open while a job is
 *  running; `onFinished` fires once per job that ends while being watched. */
export function useJobs(onFinished: (job: Job) => void): JobsHandle {
  const [jobs, setJobs] = useState<Job[]>([])
  const [streaming, setStreaming] = useState(true)
  const [dismissed, setDismissed] = useState<ReadonlySet<string>>(new Set())
  const running = useRef(new Set<string>())
  const finished = useRef(onFinished)
  finished.current = onFinished

  const receive = useCallback((list: Job[]) => {
    for (const job of list) {
      if (job.status === 'running') {
        running.current.add(job.id)
      } else if (running.current.delete(job.id)) {
        finished.current(job)
      }
    }
    setJobs(list)
    if (!list.some((j) => j.status === 'running')) setStreaming(false)
  }, [])

  useEventSource(
    '/files/jobs/stream',
    (_event, data) => {
      if (Array.isArray(data)) receive(data as Job[])
    },
    { enabled: streaming, events: ['jobs'] },
  )

  const watch = useCallback((job?: Job) => {
    if (job) {
      if (job.status === 'running') running.current.add(job.id)
      setJobs((prev) => (prev.some((j) => j.id === job.id) ? prev : [job, ...prev]))
    }
    setStreaming(true)
  }, [])

  const cancel = useCallback((id: string) => {
    api.post<Job>(`/files/jobs/${encodeURIComponent(id)}/cancel`).catch((e: unknown) => toast.error(errorMessage(e)))
  }, [])

  const dismiss = useCallback((id: string) => {
    setDismissed((prev) => new Set(prev).add(id))
  }, [])

  // Jobs that were already finished when the page opened are history, not news.
  const initial = useRef(true)
  useEffect(() => {
    if (!initial.current || jobs.length === 0) return
    initial.current = false
    const old = jobs.filter((j) => j.status !== 'running').map((j) => j.id)
    if (old.length > 0) setDismissed((prev) => new Set([...prev, ...old]))
  }, [jobs])

  return { jobs: jobs.filter((j) => !dismissed.has(j.id)), watch, cancel, dismiss }
}

function percent(job: Job): number {
  if (job.status === 'done') return 100
  if (job.bytes_total > 0) return (job.bytes_done / job.bytes_total) * 100
  if (job.items_total > 0) return (job.items_done / job.items_total) * 100
  return 0
}

function statusLine(job: Job): string {
  switch (job.status) {
    case 'done':
      return job.skipped > 0 ? t('jobDoneSkipped', { count: job.skipped }) : t('jobDone')
    case 'failed':
      return job.message || t('jobFailed')
    case 'cancelled':
      return t('jobCancelled')
  }
  if (job.phase === 'scanning') return t('jobScanning')
  if (job.phase === 'preparing') return t('jobPreparing')
  if (job.bytes_total > 0) {
    return t('jobProgressBytes', { done: formatBytes(job.bytes_done), total: formatBytes(job.bytes_total) })
  }
  return t('jobProgress', { done: job.items_done, total: Math.max(job.items_total, job.items_done) })
}

export function JobsPanel({ handle }: { handle: JobsHandle }) {
  const visible = handle.jobs.filter((j) => j.kind !== 'size')
  if (visible.length === 0) return null
  return (
    <Card className="mb-4">
      <CardHeader title={t('jobsTitle')} />
      <ul className="flex flex-col gap-3">
        {visible.map((job) => (
          <li key={job.id} className="flex items-start gap-3">
            <span className="mt-0.5 shrink-0">
              {job.status === 'running' && <Loader2 className="ms-spin size-4 text-accent" aria-hidden />}
              {job.status === 'done' && <CheckCircle2 className="size-4 text-success" aria-hidden />}
              {(job.status === 'failed' || job.status === 'cancelled') && <XCircle className="size-4 text-danger" aria-hidden />}
            </span>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm text-fg">
                <span className="font-medium">{jobKindLabel(job.kind)}</span>
                <span className="text-muted"> · {job.title}</span>
              </p>
              <p className={job.status === 'failed' ? 'break-words text-xs text-danger' : 'truncate text-xs text-muted'}>{statusLine(job)}</p>
              {job.status === 'running' && (
                <>
                  <ProgressBar value={percent(job)} tone="accent" label={jobKindLabel(job.kind)} className="mt-1.5" />
                  {job.current && <p className="mt-1 truncate text-[11px] text-faint">{job.current}</p>}
                </>
              )}
            </div>
            {job.status === 'running' ? (
              <IconButton icon={X} label={t('jobCancel')} tone="danger" onClick={() => handle.cancel(job.id)} />
            ) : (
              <IconButton icon={X} label={t('jobDismiss')} onClick={() => handle.dismiss(job.id)} />
            )}
          </li>
        ))}
      </ul>
    </Card>
  )
}
