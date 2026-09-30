import { useEffect, useId, useState } from 'react'
import { FolderOpen, HardDrive, RotateCcw, ShieldCheck } from 'lucide-react'
import { Alert, Badge, Button, ConfirmDialog, EmptyState, Field, Input, Modal, Select } from '@/components/ui'
import { formatBytes, formatShortDateTime } from '@/lib/format'
import { api, errorMessage } from '@/services/api'
import { needsPassphrase, ToggleRow } from './dialogs'
import { JobCard, triggerLabel } from './parts'
import { t } from './strings'
import type { BackupRecord, Job, Preview, RestoreResult } from './types'

type Phase = 'pick' | 'verifying' | 'review' | 'restoring'

function isPreview(v: unknown): v is Preview {
  return typeof v === 'object' && v !== null && 'entries' in v
}

function isResult(v: unknown): v is RestoreResult {
  return typeof v === 'object' && v !== null && 'restored' in v
}

export function RestoreDialog({
  name,
  backups,
  initialId,
  jobs,
  onClose,
  onChanged,
}: {
  name: string
  /** Restorable backups of one application, newest first. */
  backups: BackupRecord[]
  initialId?: number
  jobs: Job[]
  onClose: () => void
  onChanged: () => void
}) {
  const [selected, setSelected] = useState<number | null>(initialId ?? backups[0]?.id ?? null)
  const [phase, setPhase] = useState<Phase>('pick')
  const [passphrase, setPassphrase] = useState('')
  const [askPass, setAskPass] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [jobId, setJobId] = useState<string | null>(null)
  const [preview, setPreview] = useState<Preview | null>(null)
  const [safety, setSafety] = useState(true)
  const [binds, setBinds] = useState(true)
  const [confirm, setConfirm] = useState(false)
  const [confirmError, setConfirmError] = useState<string | null>(null)
  const pickId = useId()
  const passId = useId()

  const rec = backups.find((b) => b.id === selected) ?? null
  const job = jobs.find((j) => j.id === jobId) ?? null

  // The verification job delivers the preview.
  useEffect(() => {
    if (phase !== 'verifying' || !job || job.status === 'running') return
    if (job.status === 'success' && isPreview(job.result)) {
      setPreview(job.result)
      setPhase('review')
      onChanged()
    } else {
      setError(job.status === 'cancelled' ? null : job.error || t('restoreVerifyFailed'))
      setPhase('pick')
      setJobId(null)
      onChanged()
    }
  }, [job, phase, onChanged])

  useEffect(() => {
    if (phase === 'restoring' && job && job.status !== 'running') onChanged()
  }, [job, phase, onChanged])

  const verify = async () => {
    if (!rec) return
    setError(null)
    setPending(true)
    try {
      const j = await api.post<Job>(`/backup/backups/${rec.id}/verify`, { passphrase })
      setJobId(j.id)
      setPhase('verifying')
    } catch (e) {
      if (needsPassphrase(e)) {
        setAskPass(true)
        // A passphrase that was refused is not kept in the field.
        setPassphrase('')
      }
      setError(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  const restore = async () => {
    if (!rec) return
    setConfirmError(null)
    try {
      const j = await api.post<Job>(`/backup/backups/${rec.id}/restore`, {
        confirm: rec.slug,
        passphrase,
        safety_backup: safety,
        restore_binds: binds,
      })
      setJobId(j.id)
      setPhase('restoring')
      setConfirm(false)
      // The passphrase has done its work; it is not kept in memory.
      setPassphrase('')
      onChanged()
    } catch (e) {
      setConfirmError(errorMessage(e))
    }
  }

  const hasBinds = preview?.entries.some((e) => e.kind === 'bind') ?? false
  const result = job && isResult(job.result) ? job.result : null
  const finished = phase === 'restoring' && job !== null && job.status !== 'running'

  const footer = (
    <>
      <Button variant="ghost" onClick={onClose}>
        {phase === 'restoring' ? t('close') : t('cancel')}
      </Button>
      {phase === 'pick' && (
        <Button variant="primary" icon={ShieldCheck} loading={pending} disabled={!rec || (askPass && !passphrase)} onClick={() => void verify()}>
          {t('restoreVerify')}
        </Button>
      )}
      {phase === 'review' && (
        <>
          <Button
            onClick={() => {
              setPhase('pick')
              setPreview(null)
              setJobId(null)
              setPassphrase('')
            }}
          >
            {t('back')}
          </Button>
          <Button variant="danger" icon={RotateCcw} disabled={!preview?.apps_available} onClick={() => setConfirm(true)}>
            {t('restoreGo')}
          </Button>
        </>
      )}
    </>
  )

  return (
    <>
      <Modal open onClose={onClose} size="lg" title={t('restoreTitle', { name })} footer={footer}>
        {backups.length === 0 ? (
          <EmptyState title={t('restoreNoBackups')} />
        ) : (
          <div className="flex flex-col gap-4">
            {phase === 'pick' && (
              <>
                <Field label={t('restorePick')} htmlFor={pickId}>
                  <Select
                    id={pickId}
                    value={selected ?? ''}
                    onChange={(e) => {
                      setSelected(Number(e.target.value))
                      setError(null)
                      setAskPass(false)
                      setPassphrase('')
                    }}
                  >
                    {backups.map((b) => (
                      <option key={b.id} value={b.id}>
                        {`${formatShortDateTime(b.created_at)} · ${formatBytes(b.size)} · ${triggerLabel(b.trigger)}`}
                      </option>
                    ))}
                  </Select>
                </Field>
                {rec && (
                  <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted">
                    <span className="break-all font-mono">{rec.file_name}</span>
                    <Badge tone={rec.consistency === 'live' ? 'warning' : 'success'}>{rec.consistency === 'live' ? t('consLive') : t('consStopped')}</Badge>
                    <Badge tone={rec.encrypted ? 'cyan' : 'neutral'}>{rec.encrypted ? t('encrypted') : t('unencrypted')}</Badge>
                  </div>
                )}
                {askPass && (
                  <Field label={t('passphraseLabel')} htmlFor={passId} hint={t('passphraseHint')}>
                    <Input id={passId} type="password" autoComplete="off" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} />
                  </Field>
                )}
                {error && <Alert tone="danger">{error}</Alert>}
              </>
            )}

            {phase === 'verifying' && (
              <>
                <p className="text-sm text-muted">{t('restoreVerifying')}</p>
                {job && <JobCard job={job} />}
              </>
            )}

            {phase === 'review' && preview && rec && (
              <>
                <Alert tone="success">{t('restoreVerified')}</Alert>
                {preview.consistency === 'live' && <Alert tone="warning">{t('restoreLiveWarn')}</Alert>}
                {!preview.apps_available && <Alert tone="danger">{t('appsUnavailable')}</Alert>}
                <p className="text-sm text-muted">{preview.app_installed ? t('installedInfo') : t('notInstalledInfo')}</p>

                <div>
                  <h3 className="mb-2 text-xs font-medium uppercase tracking-wide text-faint">{t('restoreWhat')}</h3>
                  <ul className="flex flex-col gap-2">
                    <li className="rounded-xl border border-line bg-surface p-3 text-sm text-fg">
                      <p>{t('restoreConfig', { count: preview.secret_count })}</p>
                      <p className="mt-1 text-xs text-muted">
                        {t('restoreVersion', { version: preview.app_version || '—' })}
                        {preview.app_installed ? ` · ${t('restoreInstalledVersion', { version: preview.installed_version || '—' })}` : ''}
                      </p>
                      <p className="mt-1 break-all text-xs text-muted">{t('restoreImages', { list: preview.images.join(', ') })}</p>
                    </li>
                    {preview.entries.map((e) => {
                      const skipped = e.kind === 'bind' && !binds
                      return (
                        <li key={e.kind + e.source} className="flex items-start gap-3 rounded-xl border border-line bg-surface p-3">
                          {e.kind === 'volume' ? (
                            <HardDrive className="mt-0.5 size-4 shrink-0 text-muted" aria-hidden />
                          ) : (
                            <FolderOpen className="mt-0.5 size-4 shrink-0 text-muted" aria-hidden />
                          )}
                          <div className="min-w-0 flex-1">
                            <p className="break-all font-mono text-xs text-fg">{e.source}</p>
                            <p className="mt-0.5 text-xs text-muted">
                              {e.kind === 'volume' ? t('kindVolume') : t('kindBind')} · {t('entrySize', { files: e.files, size: formatBytes(e.bytes) })}
                            </p>
                            <div className="mt-1.5">
                              {skipped ? (
                                <Badge tone="neutral">{t('excluded')}</Badge>
                              ) : e.exists ? (
                                <Badge tone="danger">{t('willOverwrite')}</Badge>
                              ) : (
                                <Badge tone="accent">{t('willCreate')}</Badge>
                              )}
                            </div>
                          </div>
                        </li>
                      )
                    })}
                  </ul>
                  {preview.untouched.length > 0 && <p className="mt-2 break-all text-xs text-muted">{t('untouched', { list: preview.untouched.join(', ') })}</p>}
                </div>

                {preview.warnings.length > 0 && (
                  <Alert tone="warning" title={t('warningsLabel')}>
                    <ul className="list-disc pl-4">
                      {preview.warnings.map((w) => (
                        <li key={w} className="break-words">
                          {w}
                        </li>
                      ))}
                    </ul>
                  </Alert>
                )}

                {preview.app_installed && (
                  <>
                    <ToggleRow label={t('safetyLabel')} hint={t('safetyHint')} checked={safety} onChange={setSafety} />
                    {!safety && <Alert tone="danger">{t('safetyOffWarn')}</Alert>}
                  </>
                )}
                {hasBinds && <ToggleRow label={t('restoreBindsLabel')} hint={t('restoreBindsHint')} checked={binds} onChange={setBinds} />}
                <Alert tone="warning">{t('confirmWarn')}</Alert>
              </>
            )}

            {phase === 'restoring' && (
              <>
                {job ? <JobCard job={job} /> : <p className="text-sm text-muted">{t('restoreRunning')}</p>}
                {!finished && <p className="text-xs text-faint">{t('jobSurvives')}</p>}
                {finished && job?.status === 'success' && <Alert tone="success">{t('restoreDone')}</Alert>}
                {finished && job?.status === 'cancelled' && <Alert tone="warning">{t('restoreCancelled')}</Alert>}
                {finished && result && (
                  <div className="flex flex-col gap-1 text-xs text-muted">
                    {result.restored.length > 0 && <p className="break-all">{t('restoredList', { list: result.restored.join(', ') })}</p>}
                    {result.skipped.length > 0 && <p className="break-all">{t('skippedList', { list: result.skipped.join(', ') })}</p>}
                    {result.safety_backup && <p className="break-all">{t('safetyFile', { file: result.safety_backup })}</p>}
                  </div>
                )}
                {finished && result && result.warnings.length > 0 && (
                  <Alert tone="warning" title={t('warningsLabel')}>
                    <ul className="list-disc pl-4">
                      {result.warnings.map((w) => (
                        <li key={w} className="break-words">
                          {w}
                        </li>
                      ))}
                    </ul>
                  </Alert>
                )}
              </>
            )}
          </div>
        )}
      </Modal>

      {rec && (
        <ConfirmDialog
          open={confirm}
          onClose={() => setConfirm(false)}
          onConfirm={restore}
          danger
          title={t('confirmTitle')}
          message={t('confirmMsg', { name, time: formatShortDateTime(rec.created_at) })}
          warning={safety || !preview?.app_installed ? t('confirmWarn') : `${t('safetyOffWarn')} ${t('confirmWarn')}`}
          confirmLabel={t('restore')}
          requireText={rec.slug}
          error={confirmError}
        />
      )}
    </>
  )
}
