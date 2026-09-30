import { useEffect, useId, useRef, useState } from 'react'
import { Play, Save, Upload } from 'lucide-react'
import { Alert, Button, ErrorState, Field, Input, LoadingState, Modal, ProgressBar, Select, Switch } from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { formatBytes, formatPercent } from '@/lib/format'
import { api, ApiError, errorMessage } from '@/services/api'
import { toast } from '@/stores/ui'
import { dayName, JobCard } from './parts'
import { t } from './strings'
import type { AppSummary, EncryptionInfo, Job, Schedule, UploadResult } from './types'

function ToggleRow({ label, hint, checked, onChange, disabled }: { label: string; hint?: string; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-3">
      <div className="min-w-0">
        <p className="text-sm text-fg">{label}</p>
        {hint && <p className="mt-0.5 text-xs text-muted">{hint}</p>}
      </div>
      <Switch checked={checked} onChange={onChange} label={label} disabled={disabled} />
    </div>
  )
}

export { ToggleRow }

/* ---------- Backup now ---------- */

export function BackupDialog({
  app,
  encryption,
  defaults,
  onClose,
  onStarted,
}: {
  app: AppSummary
  encryption: EncryptionInfo
  defaults: { live: boolean; binds: boolean }
  onClose: () => void
  onStarted: () => void
}) {
  const [live, setLive] = useState(defaults.live)
  const [binds, setBinds] = useState(defaults.binds)
  const bindList = app.volumes.filter((v) => v.type === 'bind')
  const start = useAction(() => api.post<Job>('/backup/backups', { slug: app.slug, live, include_binds: binds }), {
    onSuccess: () => {
      toast.success(t('backupStarted', { name: app.name }))
      onStarted()
      onClose()
    },
  })

  return (
    <Modal
      open
      onClose={onClose}
      busy={start.pending}
      title={t('backupTitle', { name: app.name })}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={start.pending}>
            {t('cancel')}
          </Button>
          <Button variant="primary" icon={Play} loading={start.pending} onClick={() => void start.run()}>
            {t('start')}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {!live && <p className="text-sm text-muted">{t('backupStopInfo')}</p>}
        <ToggleRow label={t('liveLabel')} hint={t('liveHint')} checked={live} onChange={setLive} />
        {live && (
          <Alert tone="warning" title={t('liveWarnTitle')}>
            {t('liveWarn')}
          </Alert>
        )}
        {bindList.length > 0 && (
          <div className="flex flex-col gap-1.5">
            <ToggleRow label={t('bindsLabel')} hint={t('bindsHint')} checked={binds} onChange={setBinds} />
            {binds && <p className="break-words font-mono text-xs text-faint">{t('bindsList', { list: bindList.map((v) => v.source).join(', ') })}</p>}
          </div>
        )}
        <Alert tone={encryption.enabled ? 'cyan' : 'warning'}>{encryption.enabled ? t('willEncrypt') : t('wontEncrypt')}</Alert>
        {start.error && <Alert tone="danger">{start.error}</Alert>}
      </div>
    </Modal>
  )
}

/* ---------- Schedule ---------- */

interface ScheduleForm {
  enabled: boolean
  frequency: Schedule['frequency']
  time: string
  weekday: string
  monthday: string
  keep: string
  pruneManual: boolean
  live: boolean
  binds: boolean
}

function toForm(s: Schedule): ScheduleForm {
  return {
    enabled: s.enabled,
    frequency: s.frequency,
    time: `${String(s.hour).padStart(2, '0')}:${String(s.minute).padStart(2, '0')}`,
    weekday: String(s.weekday),
    monthday: String(s.monthday),
    keep: String(s.keep_last),
    pruneManual: s.prune_manual,
    live: s.live,
    binds: s.include_binds,
  }
}

function intIn(v: string, min: number, max: number): number | null {
  if (!/^\d+$/.test(v.trim())) return null
  const n = Number(v)
  return n >= min && n <= max ? n : null
}

export function ScheduleDialog({ app, encryption, onClose, onSaved }: { app: AppSummary; encryption: EncryptionInfo; onClose: () => void; onSaved: () => void }) {
  const q = useQuery<Schedule>(`/backup/schedules/${app.slug}`)
  const [form, setForm] = useState<ScheduleForm | null>(null)
  const [problem, setProblem] = useState<string | null>(null)
  const ids = { freq: useId(), time: useId(), wd: useId(), md: useId(), keep: useId() }
  const hasBinds = app.volumes.some((v) => v.type === 'bind')

  useEffect(() => {
    if (q.data) setForm(toForm(q.data))
  }, [q.data])

  const save = useAction((body: Record<string, unknown>) => api.put<Schedule>(`/backup/schedules/${app.slug}`, body), {
    onSuccess: () => {
      toast.success(t('scheduleSaved'))
      onSaved()
      onClose()
    },
  })

  const set = <K extends keyof ScheduleForm>(k: K, v: ScheduleForm[K]) => {
    setProblem(null)
    setForm((f) => (f ? { ...f, [k]: v } : f))
  }

  const submit = () => {
    if (!form) return
    const m = /^(\d{1,2}):(\d{2})$/.exec(form.time)
    const hour = m ? intIn(m[1]!, 0, 23) : null
    const minute = m ? intIn(m[2]!, 0, 59) : null
    const monthday = intIn(form.monthday, 1, 31)
    const keep = intIn(form.keep, 1, 365)
    if (hour === null || minute === null || monthday === null || keep === null) {
      setProblem(t('invalidNumber'))
      return
    }
    void save.run({
      enabled: form.enabled,
      frequency: form.frequency,
      hour,
      minute,
      weekday: Number(form.weekday),
      monthday,
      keep_last: keep,
      prune_manual: form.pruneManual,
      live: form.live,
      include_binds: form.binds,
    })
  }

  return (
    <Modal
      open
      onClose={onClose}
      busy={save.pending}
      title={t('scheduleTitle', { name: app.name })}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={save.pending}>
            {t('cancel')}
          </Button>
          <Button variant="primary" icon={Save} loading={save.pending} disabled={!form} onClick={submit}>
            {t('save')}
          </Button>
        </>
      }
    >
      {q.loading ? (
        <LoadingState />
      ) : q.error || !form ? (
        <ErrorState message={q.error ?? ''} onRetry={() => void q.reload()} />
      ) : (
        <div className="flex flex-col gap-4">
          <ToggleRow label={t('scheduleEnabled')} hint={t('missedInfo')} checked={form.enabled} onChange={(v) => set('enabled', v)} />
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t('frequency')} htmlFor={ids.freq}>
              <Select id={ids.freq} value={form.frequency} onChange={(e) => set('frequency', e.target.value as Schedule['frequency'])}>
                <option value="daily">{t('freqDaily')}</option>
                <option value="weekly">{t('freqWeekly')}</option>
                <option value="monthly">{t('freqMonthly')}</option>
              </Select>
            </Field>
            <Field label={t('time')} htmlFor={ids.time} hint={t('timeHint')}>
              <Input id={ids.time} type="time" value={form.time} onChange={(e) => set('time', e.target.value)} />
            </Field>
            {form.frequency === 'weekly' && (
              <Field label={t('weekday')} htmlFor={ids.wd}>
                <Select id={ids.wd} value={form.weekday} onChange={(e) => set('weekday', e.target.value)}>
                  {[1, 2, 3, 4, 5, 6, 0].map((d) => (
                    <option key={d} value={d}>
                      {dayName(d)}
                    </option>
                  ))}
                </Select>
              </Field>
            )}
            {form.frequency === 'monthly' && (
              <Field label={t('monthday')} htmlFor={ids.md} hint={t('monthdayHint')}>
                <Input id={ids.md} type="number" inputMode="numeric" min={1} max={31} value={form.monthday} onChange={(e) => set('monthday', e.target.value)} />
              </Field>
            )}
            <Field label={t('keepLast')} htmlFor={ids.keep} hint={t('keepLastHint')}>
              <Input id={ids.keep} type="number" inputMode="numeric" min={1} max={365} value={form.keep} onChange={(e) => set('keep', e.target.value)} />
            </Field>
          </div>
          <ToggleRow label={t('pruneManual')} hint={t('pruneManualHint')} checked={form.pruneManual} onChange={(v) => set('pruneManual', v)} />
          <ToggleRow label={t('liveLabel')} hint={t('liveHint')} checked={form.live} onChange={(v) => set('live', v)} />
          {form.live && (
            <Alert tone="warning" title={t('liveWarnTitle')}>
              {t('liveWarn')}
            </Alert>
          )}
          {hasBinds && <ToggleRow label={t('bindsLabel')} hint={t('bindsHint')} checked={form.binds} onChange={(v) => set('binds', v)} />}
          <Alert tone={encryption.enabled ? 'cyan' : 'warning'}>{encryption.enabled ? t('scheduleEncWarn') : t('wontEncrypt')}</Alert>
          {(problem || save.error) && <Alert tone="danger">{problem ?? save.error}</Alert>}
        </div>
      )}
    </Modal>
  )
}

/* ---------- Passphrase prompt ---------- */

export function PassphraseDialog({ error, pending, onClose, onSubmit }: { error?: string | null; pending?: boolean; onClose: () => void; onSubmit: (passphrase: string) => void }) {
  const [value, setValue] = useState('')
  const id = useId()
  // The passphrase leaves the field as soon as it is sent, whether it turns
  // out to be right or wrong.
  const submit = () => {
    if (!value) return
    const v = value
    setValue('')
    onSubmit(v)
  }
  return (
    <Modal
      open
      onClose={onClose}
      size="sm"
      busy={pending}
      title={t('passphraseTitle')}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={pending}>
            {t('cancel')}
          </Button>
          <Button variant="primary" loading={pending} disabled={!value} onClick={submit}>
            {t('continue')}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        <Field label={t('passphraseLabel')} htmlFor={id} hint={t('passphraseHint')}>
          <Input id={id} type="password" autoComplete="off" value={value} onChange={(e) => setValue(e.target.value)} data-autofocus />
        </Field>
        {error && <Alert tone="danger">{error}</Alert>}
      </form>
    </Modal>
  )
}

/** True when the error asks for (another) passphrase. */
export function needsPassphrase(e: unknown): boolean {
  return e instanceof ApiError && (e.code === 'passphrase_required' || e.code === 'wrong_passphrase')
}

/* ---------- Upload ---------- */

type UploadPhase = 'pick' | 'uploading' | 'passphrase' | 'validating' | 'done' | 'failed'

export function UploadDialog({ jobs, onClose, onImported }: { jobs: Job[]; onClose: () => void; onImported: () => void }) {
  const [file, setFile] = useState<File | null>(null)
  const [phase, setPhase] = useState<UploadPhase>('pick')
  const [percent, setPercent] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [uploaded, setUploaded] = useState<UploadResult | null>(null)
  const [passphrase, setPassphrase] = useState('')
  const [jobId, setJobId] = useState<string | null>(null)
  const abort = useRef<AbortController | null>(null)
  const notified = useRef(false)
  const fileId = useId()
  const passId = useId()
  const job = jobs.find((j) => j.id === jobId) ?? null

  useEffect(() => () => abort.current?.abort(), [])

  useEffect(() => {
    if (phase !== 'validating' || !job || job.status === 'running') return
    if (job.status === 'success') {
      setPhase('done')
      if (!notified.current) {
        notified.current = true
        onImported()
      }
    } else {
      setError(job.error || t('uploadFailed'))
      setPhase('failed')
    }
  }, [job, phase, onImported])

  const startImport = async (res: UploadResult, pass: string) => {
    setError(null)
    try {
      const j = await api.post<Job>('/backup/imports', { upload_id: res.upload_id, passphrase: pass })
      setJobId(j.id)
      setPhase('validating')
    } catch (e) {
      setError(errorMessage(e))
      setPhase(needsPassphrase(e) ? 'passphrase' : 'failed')
    } finally {
      // Used or refused: the passphrase is not kept in the field.
      setPassphrase('')
    }
  }

  const startUpload = async () => {
    if (!file) return
    setError(null)
    setPhase('uploading')
    setPercent(0)
    const form = new FormData()
    form.append('file', file)
    abort.current = new AbortController()
    try {
      const res = await api.upload<UploadResult>('/backup/upload', form, {
        signal: abort.current.signal,
        onProgress: (loaded, total) => setPercent(total > 0 ? (loaded / total) * 100 : 0),
      })
      setUploaded(res)
      if (res.needs_passphrase) setPhase('passphrase')
      else await startImport(res, '')
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return
      setError(errorMessage(e))
      setPhase('failed')
    }
  }

  const busy = phase === 'uploading' || (phase === 'validating' && job?.status === 'running')

  return (
    <Modal
      open
      onClose={() => {
        abort.current?.abort()
        onClose()
      }}
      title={t('uploadTitle')}
      description={t('uploadDesc')}
      footer={
        <>
          <Button
            variant="ghost"
            onClick={() => {
              abort.current?.abort()
              onClose()
            }}
          >
            {phase === 'done' || phase === 'failed' ? t('close') : t('cancel')}
          </Button>
          {phase === 'pick' && (
            <Button variant="primary" icon={Upload} disabled={!file} onClick={() => void startUpload()}>
              {t('uploadStart')}
            </Button>
          )}
          {phase === 'passphrase' && uploaded && (
            <Button variant="primary" disabled={!passphrase} onClick={() => void startImport(uploaded, passphrase)}>
              {t('continue')}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {phase === 'pick' && (
          <>
            <Field label={t('uploadFile')} htmlFor={fileId} hint={file ? formatBytes(file.size) : undefined}>
              <input
                id={fileId}
                type="file"
                accept=".gz,.enc,.tar.gz,application/gzip,application/octet-stream"
                onChange={(e) => setFile(e.target.files?.[0] ?? null)}
                className="block w-full text-sm text-fg file:mr-3 file:h-10 file:rounded-xl file:border file:border-line file:bg-raised file:px-4 file:text-sm file:text-fg"
              />
            </Field>
            <Alert tone="warning">{t('uploadUntrusted')}</Alert>
          </>
        )}
        {phase === 'uploading' && (
          <div className="flex flex-col gap-2">
            <p className="text-sm text-fg">{t('uploading', { percent: formatPercent(percent) })}</p>
            <ProgressBar value={percent} tone="accent" label={t('uploadTitle')} />
          </div>
        )}
        {phase === 'passphrase' && (
          <Field label={t('passphraseLabel')} htmlFor={passId} hint={t('passphraseHint')}>
            <Input id={passId} type="password" autoComplete="off" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} />
          </Field>
        )}
        {phase === 'validating' && (job ? <JobCard job={job} /> : <LoadingState label={t('uploadValidating')} />)}
        {phase === 'done' && job && <Alert tone="success">{t('uploadDone', { name: job.name })}</Alert>}
        {error && (phase === 'failed' || phase === 'passphrase') && (
          <Alert tone="danger" title={phase === 'failed' ? t('uploadFailed') : undefined}>
            {error}
          </Alert>
        )}
        {busy && <p className="text-xs text-faint">{phase === 'validating' ? t('jobSurvives') : ''}</p>}
      </div>
    </Modal>
  )
}
