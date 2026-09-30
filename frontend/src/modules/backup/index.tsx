import { useCallback, useId, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Archive,
  CalendarClock,
  Download,
  Info,
  Lock,
  LockOpen,
  MoreHorizontal,
  Play,
  RotateCcw,
  ShieldCheck,
  Trash2,
  Upload,
} from 'lucide-react'
import {
  Alert,
  Badge,
  Button,
  Card,
  CardHeader,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  IconTile,
  KeyValueList,
  LoadingState,
  Menu,
  Modal,
  PageHeader,
  Select,
  Status,
  TableWrap,
  tableClass,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { formatBytes, formatDateTime, formatDuration, formatRelative, formatShortDateTime } from '@/lib/format'
import { api, apiUrl, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { BackupDialog, needsPassphrase, PassphraseDialog, ScheduleDialog, UploadDialog } from './dialogs'
import { JobCard, LinkButton, RecordBadges, RecordStatus, scheduleSummary, triggerLabel, useJobs } from './parts'
import { RestoreDialog } from './RestoreDialog'
import { t } from './strings'
import type { AppSummary, BackupRecord, Job, Overview } from './types'

export { BackupSettingsSection } from './settings'

type Dialog =
  | { kind: 'backup'; app: AppSummary }
  | { kind: 'schedule'; app: AppSummary }
  | { kind: 'restore'; slug: string; name: string; id?: number }
  | { kind: 'delete'; rec: BackupRecord }
  | { kind: 'details'; rec: BackupRecord }
  | { kind: 'verify'; rec: BackupRecord; error?: string }
  | { kind: 'upload' }
  | null

function downloadUrl(rec: BackupRecord): string {
  return apiUrl(`/backup/backups/${rec.id}/download`)
}

function restorable(rec: BackupRecord): boolean {
  return rec.status === 'success' && !rec.file_missing
}

function About({ ov }: { ov: Overview }) {
  const enc = ov.encryption
  return (
    <Card>
      <CardHeader title={t('aboutTitle')} icon={Info} />
      <div className="grid gap-4 lg:grid-cols-2">
        <ul className="flex list-disc flex-col gap-1.5 pl-5 text-sm text-muted">
          <li>{t('aboutConfig')}</li>
          <li>{t('aboutVolumes')}</li>
          <li>{t('aboutBinds')}</li>
          <li>{t('aboutNot')}</li>
        </ul>
        <div className="flex flex-col gap-2">
          <Alert tone="warning" title={t('secretsTitle')}>
            {t('secretsBody')}
          </Alert>
          {enc.error ? (
            <Alert tone="danger" title={t('encErrorTitle')}>
              {enc.error}
            </Alert>
          ) : enc.enabled ? (
            <Alert tone="success" title={t('encOnTitle')}>
              {t('encOnBody', { cipher: enc.cipher, kdf: enc.kdf })}
            </Alert>
          ) : (
            <Alert tone="warning" title={t('encOffTitle')}>
              {t('encOffBody')}
            </Alert>
          )}
          <Link to="/settings" className="inline-flex min-h-10 items-center text-xs font-medium text-accent hover:underline">
            {t('openSettings')}
          </Link>
        </div>
      </div>
    </Card>
  )
}

function AppCard({ app, busy, installed, open }: { app: AppSummary; busy: boolean; installed: boolean; open: (d: Dialog) => void }) {
  const last = app.last_success
  const attempt = app.last_backup
  const volumes = app.volumes.filter((v) => v.type === 'volume').length
  const binds = app.volumes.filter((v) => v.type === 'bind').length
  return (
    <Card className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <IconTile icon={Archive} tone={last ? 'accent' : 'neutral'} />
          <div className="min-w-0">
            <p className="truncate text-sm font-semibold text-fg">{app.name}</p>
            <p className="truncate text-xs text-muted">
              {installed ? t('dataLocations', { volumes, binds }) : t('notInstalled')}
              {app.version ? ` · ${app.version}` : ''}
            </p>
          </div>
        </div>
        {busy ? (
          <Status tone="accent" pulse>
            {t('running')}
          </Status>
        ) : (
          last && (
            <Menu
              label={t('more')}
              trigger={<MoreHorizontal className="size-5" aria-hidden />}
              items={[
                { label: t('downloadLast'), icon: Download, onSelect: () => window.location.assign(downloadUrl(last)) },
                { label: t('verify'), icon: ShieldCheck, onSelect: () => open({ kind: 'verify', rec: last }) },
                { label: t('deleteLast'), icon: Trash2, danger: true, separated: true, onSelect: () => open({ kind: 'delete', rec: last }) },
              ]}
            />
          )
        )}
      </div>

      <div className="flex flex-col gap-1 text-xs">
        {last ? (
          <>
            <p className="text-fg">
              {t('lastBackup')}: {formatRelative(last.created_at)} · {formatBytes(last.size)}
            </p>
            <div className="flex flex-wrap items-center gap-1.5">
              <RecordStatus rec={last} />
              <RecordBadges rec={last} />
            </div>
            <p className="text-muted">
              {t('backupCount', { count: app.backup_count })} · {formatBytes(app.total_size)}
            </p>
          </>
        ) : (
          <p className="text-muted">{t('never')}</p>
        )}
        {attempt && attempt.id !== last?.id && attempt.status === 'failed' && (
          <p className="break-words text-danger">
            {t('lastFailed')} ({formatRelative(attempt.created_at)}): {attempt.error}
          </p>
        )}
        {attempt && attempt.id !== last?.id && attempt.status === 'cancelled' && <p className="text-warning">{t('lastCancelled')}</p>}
        {installed && (
          <p className="flex items-center gap-1.5 text-muted">
            <CalendarClock className="size-3.5 shrink-0" aria-hidden />
            <span className="min-w-0 break-words">
              {scheduleSummary(app.schedule)}
              {app.schedule?.enabled && app.schedule.next_run_at > 0 ? ` · ${t('nextRun', { time: formatShortDateTime(app.schedule.next_run_at) })}` : ''}
            </span>
          </p>
        )}
      </div>

      <div className="mt-auto grid grid-cols-2 gap-2 sm:flex sm:flex-wrap">
        {installed && (
          <Button variant="primary" icon={Play} disabled={busy} onClick={() => open({ kind: 'backup', app })}>
            {t('backupNow')}
          </Button>
        )}
        <Button icon={RotateCcw} disabled={busy || app.backup_count === 0} onClick={() => open({ kind: 'restore', slug: app.slug, name: app.name })}>
          {t('restore')}
        </Button>
        {installed && (
          <Button icon={CalendarClock} onClick={() => open({ kind: 'schedule', app })}>
            {t('schedule')}
          </Button>
        )}
        {last && (
          <LinkButton href={downloadUrl(last)}>
            <Download className="size-4 shrink-0" aria-hidden />
            {t('download')}
          </LinkButton>
        )}
        {last && (
          <Button variant="danger" icon={Trash2} disabled={busy} onClick={() => open({ kind: 'delete', rec: last })}>
            {t('remove')}
          </Button>
        )}
      </div>
    </Card>
  )
}

function BackupList({ records, busySlugs, open }: { records: BackupRecord[]; busySlugs: Set<string>; open: (d: Dialog) => void }) {
  const [slug, setSlug] = useState('')
  const [status, setStatus] = useState('')
  const [trigger, setTrigger] = useState('')
  const ids = { app: useId(), status: useId(), trigger: useId() }

  const names = useMemo(() => {
    const m = new Map<string, string>()
    for (const r of records) if (!m.has(r.slug)) m.set(r.slug, r.app_name || r.slug)
    return [...m.entries()].sort((a, b) => a[1].localeCompare(b[1], 'tr'))
  }, [records])
  const rows = records.filter((r) => (!slug || r.slug === slug) && (!status || r.status === status) && (!trigger || r.trigger === trigger))

  return (
    <Card>
      <CardHeader title={t('listTitle')} icon={Archive} />
      <div className="mb-4 grid gap-3 sm:grid-cols-3">
        <Field label={t('filterApp')} htmlFor={ids.app}>
          <Select id={ids.app} value={slug} onChange={(e) => setSlug(e.target.value)}>
            <option value="">{t('all')}</option>
            {names.map(([s, n]) => (
              <option key={s} value={s}>
                {n}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t('filterStatus')} htmlFor={ids.status}>
          <Select id={ids.status} value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">{t('all')}</option>
            <option value="success">{t('stSuccess')}</option>
            <option value="failed">{t('stFailed')}</option>
            <option value="cancelled">{t('stCancelled')}</option>
            <option value="running">{t('stRunning')}</option>
          </Select>
        </Field>
        <Field label={t('filterTrigger')} htmlFor={ids.trigger}>
          <Select id={ids.trigger} value={trigger} onChange={(e) => setTrigger(e.target.value)}>
            <option value="">{t('all')}</option>
            <option value="manual">{t('trManual')}</option>
            <option value="scheduled">{t('trScheduled')}</option>
            <option value="safety">{t('trSafety')}</option>
            <option value="imported">{t('trImported')}</option>
          </Select>
        </Field>
      </div>
      {rows.length === 0 ? (
        <EmptyState icon={Archive} title={t('listEmptyTitle')} description={t('listEmptyDesc')} />
      ) : (
        <TableWrap>
          <table className={tableClass.table}>
            <thead>
              <tr>
                <th className={tableClass.th}>{t('colApp')}</th>
                <th className={tableClass.th}>{t('colDate')}</th>
                <th className={tableClass.th}>{t('colSize')}</th>
                <th className={tableClass.th}>{t('colStatus')}</th>
                <th className={tableClass.th}>{t('colType')}</th>
                <th className={tableClass.th}>
                  <span className="sr-only">{t('colActions')}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const ok = restorable(r)
                const busy = busySlugs.has(r.slug)
                return (
                  <tr key={r.id} className={tableClass.row}>
                    <td className={tableClass.td}>
                      <span className="font-medium">{r.app_name || r.slug}</span>
                    </td>
                    <td className={`${tableClass.td} whitespace-nowrap`}>{formatShortDateTime(r.created_at)}</td>
                    <td className={`${tableClass.td} whitespace-nowrap`}>{r.status === 'success' ? formatBytes(r.size) : '—'}</td>
                    <td className={tableClass.td}>
                      <RecordStatus rec={r} />
                    </td>
                    <td className={tableClass.td}>
                      <RecordBadges rec={r} />
                    </td>
                    <td className={`${tableClass.td} whitespace-nowrap text-right`}>
                      <IconButton icon={Info} label={t('details')} onClick={() => open({ kind: 'details', rec: r })} />
                      {ok && (
                        <>
                          <IconButton
                            icon={RotateCcw}
                            label={t('restore')}
                            disabled={busy}
                            onClick={() => open({ kind: 'restore', slug: r.slug, name: r.app_name || r.slug, id: r.id })}
                          />
                          <IconButton icon={ShieldCheck} label={t('verify')} disabled={busy} onClick={() => open({ kind: 'verify', rec: r })} />
                          <a
                            href={downloadUrl(r)}
                            download
                            aria-label={t('download')}
                            title={t('download')}
                            className="inline-flex size-10 shrink-0 items-center justify-center rounded-lg align-middle text-muted transition-colors hover:bg-raised hover:text-fg"
                          >
                            <Download className="size-[18px]" aria-hidden />
                          </a>
                        </>
                      )}
                      {r.status !== 'running' && (
                        <IconButton icon={Trash2} label={t('remove')} tone="danger" disabled={busy} onClick={() => open({ kind: 'delete', rec: r })} />
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </TableWrap>
      )}
    </Card>
  )
}

function DetailsDialog({ rec, onClose }: { rec: BackupRecord; onClose: () => void }) {
  return (
    <Modal open onClose={onClose} title={t('detailsTitle')} footer={<Button onClick={onClose}>{t('close')}</Button>}>
      <div className="flex flex-col gap-4">
        <KeyValueList
          items={[
            { label: t('colApp'), value: `${rec.app_name || rec.slug}${rec.app_version ? ` (${rec.app_version})` : ''}` },
            { label: t('colDate'), value: formatDateTime(rec.created_at) },
            { label: t('colStatus'), value: <RecordStatus rec={rec} /> },
            { label: t('colType'), value: triggerLabel(rec.trigger) },
            { label: t('fileName'), value: <span className="break-all font-mono text-xs">{rec.file_name || '—'}</span> },
            { label: t('colSize'), value: rec.status === 'success' ? formatBytes(rec.size) : '—' },
            { label: t('colDuration'), value: formatDuration(rec.duration) },
            { label: t('consStoppedShort'), value: rec.consistency === 'live' ? t('consLive') : t('consStopped') },
            {
              label: t('encrypted'),
              value: (
                <span className="inline-flex items-center gap-1.5">
                  {rec.encrypted ? <Lock className="size-3.5" aria-hidden /> : <LockOpen className="size-3.5" aria-hidden />}
                  {rec.encrypted ? t('yes') : t('no')}
                </span>
              ),
            },
            { label: t('includesBinds'), value: rec.includes_binds ? t('included') : t('excluded') },
            {
              label: t('verify'),
              value:
                rec.verified_at > 0 ? (
                  <Badge tone={rec.verify_ok ? 'success' : 'danger'}>
                    {t(rec.verify_ok ? 'verifiedOk' : 'verifiedBad', { time: formatShortDateTime(rec.verified_at) })}
                  </Badge>
                ) : (
                  t('notVerified')
                ),
            },
          ]}
        />
        {rec.error && (
          <Alert tone="danger" title={t('errorLabel')}>
            {rec.error}
          </Alert>
        )}
        {rec.warnings.length > 0 && (
          <Alert tone="warning" title={t('warningsLabel')}>
            <ul className="list-disc pl-4">
              {rec.warnings.map((w) => (
                <li key={w} className="break-words">
                  {w}
                </li>
              ))}
            </ul>
          </Alert>
        )}
      </div>
    </Modal>
  )
}

export default function BackupPage() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const overview = useQuery<Overview>('/backup/overview', { enabled: isAdmin })
  const list = useQuery<BackupRecord[]>('/backup/backups', { enabled: isAdmin })
  const settings = useQuery<Record<string, string>>('/settings', { enabled: isAdmin })
  const [dialog, setDialog] = useState<Dialog>(null)
  const [verifyPending, setVerifyPending] = useState(false)

  const reloadOverview = overview.reload
  const reloadList = list.reload
  const refresh = useCallback(() => {
    void reloadOverview()
    void reloadList()
  }, [reloadOverview, reloadList])

  const { jobs, stream } = useJobs(isAdmin, overview.data?.jobs, (job: Job) => {
    refresh()
    const label = job.name || job.slug
    if (job.status === 'success') toast.success(`${label}: ${job.logs[job.logs.length - 1]?.message ?? ''}`.trim())
    else if (job.status === 'failed') toast.error(`${label}: ${job.error}`)
  })

  const busySlugs = useMemo(() => new Set(jobs.filter((j) => j.status === 'running' && j.slug).map((j) => j.slug)), [jobs])

  const del = useAction((id: number) => api.del<unknown>(`/backup/backups/${id}`), {
    onSuccess: () => {
      toast.success(t('deleted'))
      setDialog(null)
      refresh()
    },
  })

  const startVerify = async (rec: BackupRecord, passphrase: string) => {
    setVerifyPending(true)
    try {
      await api.post<Job>(`/backup/backups/${rec.id}/verify`, { passphrase })
      toast.info(t('verifyStarted'))
      setDialog(null)
    } catch (e) {
      if (needsPassphrase(e)) setDialog({ kind: 'verify', rec, error: passphrase ? errorMessage(e) : undefined })
      else {
        toast.error(errorMessage(e))
        setDialog(null)
      }
    } finally {
      setVerifyPending(false)
    }
  }

  const open = (d: Dialog) => {
    del.clearError()
    if (d?.kind === 'verify' && d.error === undefined) {
      // Try with the key stored on the server first; ask only if needed.
      void startVerify(d.rec, '')
      return
    }
    setDialog(d)
  }

  if (!isAdmin) {
    return (
      <>
        <PageHeader title={t('pageTitle')} icon={Archive} />
        <Card>
          <EmptyState icon={Lock} title={t('adminOnlyTitle')} description={t('adminOnlyDesc')} />
        </Card>
      </>
    )
  }

  const ov = overview.data
  const records = list.data ?? []
  const visibleJobs = jobs.filter((j) => j.status === 'running' || Date.now() / 1000 - j.finished_at < 3600).slice(0, 8)
  const restoreBackups = dialog?.kind === 'restore' ? records.filter((r) => r.slug === dialog.slug && restorable(r)) : []

  return (
    <>
      <PageHeader
        title={t('pageTitle')}
        description={t('pageDescription')}
        icon={Archive}
        actions={
          <Button icon={Upload} onClick={() => open({ kind: 'upload' })}>
            {t('upload')}
          </Button>
        }
      />

      {overview.loading ? (
        <Card>
          <LoadingState />
        </Card>
      ) : overview.error || !ov ? (
        <Card>
          <ErrorState message={overview.error ?? ''} onRetry={refresh} />
        </Card>
      ) : (
        <div className="flex flex-col gap-5">
          <About ov={ov} />

          {visibleJobs.length > 0 && (
            <Card>
              <CardHeader title={t('jobsTitle')} subtitle={t('jobSurvives')} />
              {stream === 'closed' && (
                <Alert tone="warning" className="mb-3">
                  {t('streamLost')}
                </Alert>
              )}
              <div className="grid gap-3 lg:grid-cols-2">
                {visibleJobs.map((j) => (
                  <JobCard key={j.id} job={j} />
                ))}
              </div>
            </Card>
          )}

          <section aria-label={t('appsTitle')} className="flex flex-col gap-3">
            <h2 className="text-[15px] font-semibold text-fg">{t('appsTitle')}</h2>
            {!ov.apps_available ? (
              <Card>
                <ErrorState title={t('appsUnavailable')} message={ov.apps_error} onRetry={refresh} />
              </Card>
            ) : ov.apps.length === 0 ? (
              <Card>
                <EmptyState
                  icon={Archive}
                  title={t('noAppsTitle')}
                  description={t('noAppsDesc')}
                  action={
                    <Link
                      to="/apps"
                      className="inline-flex h-10 items-center justify-center rounded-xl border border-transparent bg-accent-strong px-4 text-sm font-medium text-accent-fg transition-colors hover:bg-accent"
                    >
                      {t('goApps')}
                    </Link>
                  }
                />
              </Card>
            ) : (
              <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
                {ov.apps.map((a) => (
                  <AppCard key={a.slug} app={a} installed busy={busySlugs.has(a.slug)} open={open} />
                ))}
              </div>
            )}
          </section>

          {ov.orphans.length > 0 && (
            <section aria-label={t('orphansTitle')} className="flex flex-col gap-3">
              <div>
                <h2 className="text-[15px] font-semibold text-fg">{t('orphansTitle')}</h2>
                <p className="mt-0.5 text-xs text-muted">{t('orphansDesc')}</p>
              </div>
              <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
                {ov.orphans.map((a) => (
                  <AppCard key={a.slug} app={a} installed={false} busy={busySlugs.has(a.slug)} open={open} />
                ))}
              </div>
            </section>
          )}

          {list.loading ? (
            <Card>
              <LoadingState />
            </Card>
          ) : list.error ? (
            <Card>
              <ErrorState message={list.error} onRetry={() => void list.reload()} />
            </Card>
          ) : (
            <BackupList records={records} busySlugs={busySlugs} open={open} />
          )}
        </div>
      )}

      {dialog?.kind === 'backup' && ov && (
        <BackupDialog
          app={dialog.app}
          encryption={ov.encryption}
          defaults={{
            live: settings.data?.['backup.default_consistency'] === 'live',
            binds: settings.data?.['backup.default_include_binds'] !== 'false',
          }}
          onClose={() => setDialog(null)}
          onStarted={refresh}
        />
      )}
      {dialog?.kind === 'schedule' && ov && (
        <ScheduleDialog app={dialog.app} encryption={ov.encryption} onClose={() => setDialog(null)} onSaved={refresh} />
      )}
      {dialog?.kind === 'restore' && (
        <RestoreDialog name={dialog.name} backups={restoreBackups} initialId={dialog.id} jobs={jobs} onClose={() => setDialog(null)} onChanged={refresh} />
      )}
      {dialog?.kind === 'upload' && <UploadDialog jobs={jobs} onClose={() => setDialog(null)} onImported={refresh} />}
      {dialog?.kind === 'details' && <DetailsDialog rec={dialog.rec} onClose={() => setDialog(null)} />}
      {dialog?.kind === 'verify' && (
        <PassphraseDialog
          error={dialog.error}
          pending={verifyPending}
          onClose={() => setDialog(null)}
          onSubmit={(p) => void startVerify(dialog.rec, p)}
        />
      )}
      {dialog?.kind === 'delete' && (
        <ConfirmDialog
          open
          onClose={() => setDialog(null)}
          onConfirm={async () => {
            await del.run(dialog.rec.id)
          }}
          danger
          title={t('deleteTitle')}
          message={t(dialog.rec.status === 'success' ? 'deleteMsg' : 'deleteRecordMsg', {
            name: dialog.rec.app_name || dialog.rec.slug,
            time: formatShortDateTime(dialog.rec.created_at),
          })}
          warning={dialog.rec.status === 'success' ? t('deleteWarn') : undefined}
          confirmLabel={t('remove')}
          error={del.error}
        />
      )}
    </>
  )
}
