import { useId, useState } from 'react'
import { Download, Package, PackageCheck, Power, RotateCcw } from 'lucide-react'
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Field,
  LoadingState,
  Select,
  Switch,
  TableWrap,
  tableClass,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { formatRelative } from '@/lib/format'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { AreaCard, JobOutput, checkedText } from './shared'
import { t } from './strings'
import { refreshSummary } from './summary'
import type { AptView, JobMeta } from './types'

type Mode = 'upgrade' | 'full'

export function AptSection() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const apt = useQuery<AptView>('/updates/apt')
  const [mode, setMode] = useState<Mode>('upgrade')
  const [kernel, setKernel] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const [rebootOpen, setRebootOpen] = useState(false)
  const [job, setJob] = useState<JobMeta | null>(null)
  const [showLast, setShowLast] = useState(false)
  const modeId = useId()

  const after = () => {
    void apt.reload()
    refreshSummary()
  }

  const check = useAction(() => api.post<AptView>('/updates/apt/check'), {
    onSuccess: () => {
      toast.success(t('checkDone'))
      after()
    },
    onError: (m) => {
      toast.error(m)
      after()
    },
  })
  const upgrade = useAction(
    () => api.post<JobMeta>('/updates/apt/upgrade', { mode, include_kernel: kernel, confirm: true }),
    {
      onSuccess: (j) => {
        setJob(j)
        setConfirm(false)
        toast.info(t('upgradeStarted'))
        refreshSummary()
      },
    },
  )
  const reboot = useAction((hostname: string) => api.post('/updates/reboot', { hostname }), {
    onSuccess: () => {
      setRebootOpen(false)
      toast.warning(t('rebootStarted'))
    },
  })

  const data = apt.data
  const activeJob = job ?? (data?.job?.status === 'running' ? data.job : null)
  const running = activeJob?.status === 'running'
  const lastJob = !activeJob && data?.job ? data.job : null
  const installable = data ? data.packages.filter((p) => kernel || !p.kernel).length : 0

  return (
    <AreaCard
      title={t('aptTitle')}
      icon={Package}
      subtitle={data ? checkedText(data.checked_at, data.auto_check) : ''}
      badge={data && data.count > 0 ? <Badge tone="warning">{t('updatesAvailable', { count: data.count })}</Badge> : undefined}
      canCheck={isAdmin && !running}
      checking={check.pending || Boolean(data?.checking)}
      onCheck={() => void check.run()}
      error={data?.error}
    >
      {apt.loading ? (
        <LoadingState />
      ) : apt.error || !data ? (
        <ErrorState message={apt.error ?? ''} onRetry={() => void apt.reload()} />
      ) : (
        <div className="flex flex-col gap-4">
          {data.reboot_required && (
            <Alert tone="warning" title={t('rebootTitle')}>
              <p>{t('rebootDesc')}</p>
              {data.reboot_packages.length > 0 && (
                <p className="mt-1 break-words">{t('rebootPackages', { list: data.reboot_packages.join(', ') })}</p>
              )}
              {isAdmin && (
                <Button
                  variant="danger"
                  size="md"
                  icon={Power}
                  className="mt-2"
                  disabled={running || !data.hostname}
                  onClick={() => {
                    reboot.clearError()
                    setRebootOpen(true)
                  }}
                >
                  {t('reboot')}
                </Button>
              )}
            </Alert>
          )}

          {activeJob && (
            <JobOutput
              job={activeJob}
              onDone={(m) => {
                setJob(m)
                if (m.status === 'success') toast.success(t('upgradeOk'))
                else toast.error(m.message || t('upgradeFailed'))
                after()
              }}
            />
          )}

          {data.checked_at === null && data.count === 0 ? (
            <EmptyState icon={Package} title={t('aptNeverTitle')} description={t('aptNeverDesc')} />
          ) : data.count === 0 ? (
            <EmptyState icon={PackageCheck} title={t('aptEmptyTitle')} description={t('aptEmptyDesc')} />
          ) : (
            <>
              <div className="flex flex-wrap items-center gap-2 text-xs text-muted">
                {data.security_count > 0 && <Badge tone="danger">{t('securityCount', { count: data.security_count })}</Badge>}
                {data.kernel_count > 0 && <Badge tone="purple">{t('kernelCount', { count: data.kernel_count })}</Badge>}
                {data.lists_refreshed_at !== null && (
                  <span>{t('aptListsRefreshed', { time: formatRelative(data.lists_refreshed_at) })}</span>
                )}
                {data.error && <span>{t('aptStale')}</span>}
              </div>
              <TableWrap className="max-h-96 overflow-y-auto">
                <table className={tableClass.table}>
                  <thead>
                    <tr>
                      <th className={tableClass.th}>{t('colPackage')}</th>
                      <th className={tableClass.th}>{t('colCurrent')}</th>
                      <th className={tableClass.th}>{t('colCandidate')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.packages.map((p) => (
                      <tr key={p.name + ':' + p.arch} className={tableClass.row}>
                        <td className={tableClass.td}>
                          <div className="flex flex-wrap items-center gap-1.5">
                            <span className="font-medium break-all">{p.name}</span>
                            {p.security && <Badge tone="danger">{t('badgeSecurity')}</Badge>}
                            {p.kernel && <Badge tone="purple">{t('badgeKernel')}</Badge>}
                            {p.new && <Badge tone="cyan">{t('badgeNew')}</Badge>}
                          </div>
                        </td>
                        <td className={tableClass.td + ' font-mono text-xs text-muted'}>{p.current_version || '—'}</td>
                        <td className={tableClass.td + ' font-mono text-xs'}>{p.candidate_version}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </TableWrap>

              {isAdmin && (
                <div className="flex flex-col gap-4 rounded-xl border border-line bg-surface p-4">
                  <p className="text-sm font-medium text-fg">{t('optionsTitle')}</p>
                  <Field
                    label={t('modeLabel')}
                    htmlFor={modeId}
                    hint={mode === 'full' ? t('modeFullHint') : t('modeUpgradeHint')}
                    className="max-w-md"
                  >
                    <Select id={modeId} value={mode} disabled={running} onChange={(e) => setMode(e.target.value as Mode)}>
                      <option value="upgrade">{t('modeUpgrade')}</option>
                      <option value="full">{t('modeFull')}</option>
                    </Select>
                  </Field>
                  <div className="flex items-start gap-3">
                    <Switch checked={kernel} onChange={setKernel} label={t('kernelOption')} disabled={running} />
                    <div className="min-w-0">
                      <p className="text-sm text-fg">{t('kernelOption')}</p>
                      <p className="mt-0.5 text-xs text-faint">{t('kernelHint')}</p>
                    </div>
                  </div>
                  <div>
                    <Button
                      variant="primary"
                      icon={Download}
                      disabled={running || installable === 0 || check.pending || data.checking}
                      onClick={() => {
                        upgrade.clearError()
                        setConfirm(true)
                      }}
                    >
                      {running ? t('upgradeRunning') : t('install')}
                    </Button>
                  </div>
                </div>
              )}
            </>
          )}

          {lastJob && (
            <div className="flex flex-col gap-2">
              <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted">
                <span>
                  {t('lastJob')}: {lastJob.detail} · {formatRelative(lastJob.started_at)}
                </span>
                {isAdmin && (
                  <Button size="sm" variant="ghost" icon={RotateCcw} onClick={() => setShowLast((v) => !v)}>
                    {showLast ? t('hideOutput') : t('showOutput')}
                  </Button>
                )}
              </div>
              {showLast && isAdmin && <JobOutput job={lastJob} />}
            </div>
          )}
        </div>
      )}

      <ConfirmDialog
        open={confirm}
        onClose={() => setConfirm(false)}
        onConfirm={async () => {
          await upgrade.run()
        }}
        title={t('confirmTitle')}
        message={t('confirmMessage')}
        warning={
          <>
            <p>{kernel ? t('confirmKernelOn') : t('confirmKernelOff')}</p>
            {mode === 'full' && <p className="mt-1">{t('confirmFull')}</p>}
          </>
        }
        danger={mode === 'full'}
        confirmLabel={t('confirmInstall')}
        cancelLabel={t('cancel')}
        pending={upgrade.pending}
        error={upgrade.error}
      />
      <ConfirmDialog
        open={rebootOpen}
        onClose={() => setRebootOpen(false)}
        onConfirm={async () => {
          if (data?.hostname) await reboot.run(data.hostname)
        }}
        title={t('rebootConfirmTitle')}
        message={t('rebootConfirmMessage')}
        danger
        requireText={data?.hostname || undefined}
        confirmLabel={t('reboot')}
        cancelLabel={t('cancel')}
        pending={reboot.pending}
        error={reboot.error}
      />
    </AreaCard>
  )
}
