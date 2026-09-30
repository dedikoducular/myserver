import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Box, Download, Settings } from 'lucide-react'
import { Alert, Badge, Button, ConfirmDialog, EmptyState, ErrorState, KeyValueList, LoadingState } from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { formatDateTime } from '@/lib/format'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { AreaCard, checkedText } from './shared'
import { t } from './strings'
import { refreshSummary } from './summary'
import type { LastUpdate, SelfView } from './types'

function LastUpdateNote({ last }: { last: LastUpdate }) {
  const vars = { to: last.to_version || '—', from: last.from_version || '—' }
  const tone = last.state === 'success' ? 'success' : last.state === 'running' ? 'accent' : 'danger'
  const text =
    last.state === 'success'
      ? t('lastSuccess', vars)
      : last.state === 'running'
        ? t('lastRunning', vars)
        : last.state === 'rolled_back'
          ? t('lastRolledBack', vars)
          : t('lastFailed', vars)
  return (
    <Alert tone={tone} title={t('lastUpdateTitle')}>
      <p>{text}</p>
      {last.state !== 'success' && last.message && <p className="mt-1 break-words">{last.message}</p>}
      {last.time !== null && <p className="mt-1">{t('lastWhen', { time: formatDateTime(last.time) })}</p>}
    </Alert>
  )
}

export function SelfSection() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const self = useQuery<SelfView>('/updates/self')
  const [confirm, setConfirm] = useState(false)
  const [started, setStarted] = useState(false)

  const after = () => {
    void self.reload()
    refreshSummary()
  }
  const check = useAction(() => api.post<SelfView>('/updates/self/check'), {
    onSuccess: () => {
      toast.success(t('checkDone'))
      after()
    },
    onError: (m) => {
      toast.error(m)
      after()
    },
  })
  const apply = useAction((version: string) => api.post('/updates/self/apply', { version, confirm: true }), {
    onSuccess: () => {
      setConfirm(false)
      setStarted(true)
      toast.info(t('selfStarted'))
    },
  })

  const data = self.data
  const latest = data?.latest ?? null

  return (
    <AreaCard
      title={t('selfTitle')}
      icon={Box}
      subtitle={data ? (data.configured ? checkedText(data.checked_at, data.auto_check) : t('noSourceTitle')) : ''}
      badge={data?.update_available && latest ? <Badge tone="warning">{t('vAvailable', { version: latest.version })}</Badge> : undefined}
      canCheck={isAdmin && Boolean(data?.configured)}
      checking={check.pending || Boolean(data?.checking)}
      onCheck={() => void check.run()}
      error={data?.error}
    >
      {self.loading ? (
        <LoadingState />
      ) : self.error || !data ? (
        <ErrorState message={self.error ?? ''} onRetry={() => void self.reload()} />
      ) : (
        <div className="flex flex-col gap-4">
          <KeyValueList
            items={[
              { label: t('installedVersion'), value: <span className="font-mono">{data.installed_version}</span> },
              ...(data.configured
                ? [
                    { label: t('source'), value: <span className="break-all">{data.source_label}</span> },
                    {
                      label: t('latestVersion'),
                      value: latest ? <span className="font-mono">{latest.version}</span> : '—',
                    },
                  ]
                : []),
              ...(latest && data.update_available
                ? [
                    { label: t('published'), value: formatDateTime(latest.published_at) },
                    ...(data.asset
                      ? [{ label: t('archive', { arch: data.arch }), value: <span className="text-xs break-all">{data.asset.url}</span> }]
                      : []),
                    {
                      label: t('checksum'),
                      value: data.asset?.sha256 ? (
                        <span className="font-mono text-xs break-all">{data.asset.sha256}</span>
                      ) : (
                        <span className="text-muted">{t('checksumMissing')}</span>
                      ),
                    },
                  ]
                : []),
            ]}
          />

          {data.last_update && <LastUpdateNote last={data.last_update} />}

          {!data.configured ? (
            <EmptyState
              icon={Settings}
              title={t('noSourceTitle')}
              description={t('noSourceDesc')}
              action={
                isAdmin ? (
                  <Link to="/settings" className="inline-flex min-h-10 items-center text-sm font-medium text-accent hover:underline">
                    {t('settingsTitle')}
                  </Link>
                ) : undefined
              }
            />
          ) : data.checked_at === null ? (
            <p className="text-sm text-muted">{t('selfNotChecked')}</p>
          ) : data.update_available && latest ? (
            <>
              <Alert tone="accent" title={t('selfAvailable', { version: latest.version })} />
              <div>
                <p className="mb-1.5 text-xs font-medium text-muted">{t('releaseNotes')}</p>
                {/* Remote text: rendered as plain text only. */}
                <pre className="max-h-72 overflow-auto rounded-xl border border-line bg-surface px-3 py-2 font-sans text-xs leading-relaxed whitespace-pre-wrap break-words text-fg">
                  {latest.notes || t('noNotes')}
                </pre>
              </div>
              {started && <Alert tone="accent">{t('selfStarted')}</Alert>}
              {!data.installable && data.install_blocker && (
                <Alert tone="warning" title={t('notInstallableTitle')}>
                  {data.install_blocker}
                </Alert>
              )}
              {isAdmin && data.installable && (
                <div>
                  <Button
                    variant="primary"
                    icon={Download}
                    disabled={started || data.last_update?.state === 'running'}
                    onClick={() => {
                      apply.clearError()
                      setConfirm(true)
                    }}
                  >
                    {t('selfInstall')}
                  </Button>
                </div>
              )}
            </>
          ) : (
            !data.error && <p className="text-sm text-success">{t('selfCurrent')}</p>
          )}
        </div>
      )}

      <ConfirmDialog
        open={confirm}
        onClose={() => setConfirm(false)}
        onConfirm={async () => {
          if (latest) await apply.run(latest.version)
        }}
        title={t('selfConfirmTitle')}
        message={t('selfConfirmMessage', { from: data?.installed_version ?? '', to: latest?.version ?? '' })}
        confirmLabel={t('selfInstall')}
        cancelLabel={t('cancel')}
        pending={apply.pending}
        error={apply.error}
      />
    </AreaCard>
  )
}
