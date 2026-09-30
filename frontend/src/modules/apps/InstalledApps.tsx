import { useCallback, useState } from 'react'
import { ConfirmDialog } from '@/components/ui'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { AppDialog } from './AppDialog'
import { JobDialog } from './JobProgress'
import { LogsDialog } from './LogsDialog'
import { InstalledCard, gridClass, type AppAction } from './parts'
import { t } from './strings'
import type { InstalledApp, Job, JobKind, ViewMode } from './types'
import { UninstallDialog } from './UninstallDialog'
import { openWebUI } from './util'

type Dialog =
  | { kind: 'stop' | 'update' | 'logs' | 'settings' | 'uninstall'; app: InstalledApp }
  | { kind: 'job'; job: Pick<Job, 'id' | 'name' | 'kind'> }
  | null

const JOB_KINDS: JobKind[] = ['install', 'update', 'settings', 'restore']

/** The cards of installed applications with every action of the "…" menu.
 *  Shared by the page and the dashboard widget. */
export function InstalledApps({ apps, view, onChanged }: { apps: InstalledApp[]; view: ViewMode; onChanged: () => void }) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [busy, setBusy] = useState<Record<string, string>>({})
  const [error, setError] = useState<string | null>(null)

  const lifecycle = useCallback(
    async (app: InstalledApp, action: 'start' | 'stop' | 'restart') => {
      setBusy((b) => ({ ...b, [app.slug]: action }))
      try {
        await api.post(`/apps/installed/${app.slug}/${action}`)
        toast.success(t(action === 'start' ? 'started' : action === 'stop' ? 'stopped' : 'restarted', { name: app.name }))
        return true
      } catch (e) {
        toast.error(errorMessage(e))
        return false
      } finally {
        setBusy((b) => {
          const next = { ...b }
          delete next[app.slug]
          return next
        })
        onChanged()
      }
    },
    [onChanged],
  )

  const onAction = (app: InstalledApp, action: AppAction) => {
    setError(null)
    switch (action) {
      case 'open':
        if (app.web_ui?.local_only) toast.info(t('localOnlyHint'))
        else if (app.web_ui) openWebUI(app.web_ui)
        return
      case 'start':
      case 'restart':
        void lifecycle(app, action)
        return
      case 'progress':
        if (app.job_id) {
          const kind = JOB_KINDS.find((k) => k === app.operation) ?? 'update'
          setDialog({ kind: 'job', job: { id: app.job_id, name: app.name, kind } })
        }
        return
      default:
        setDialog({ kind: action, app })
    }
  }

  const close = () => {
    setDialog(null)
    setError(null)
  }

  const startUpdate = async (app: InstalledApp) => {
    setError(null)
    try {
      const job = await api.post<Job>(`/apps/installed/${app.slug}/update`)
      onChanged()
      setDialog({ kind: 'job', job })
    } catch (e) {
      setError(errorMessage(e))
    }
  }

  const target = dialog && dialog.kind !== 'job' ? dialog.app : null

  return (
    <>
      <div className={gridClass(view)}>
        {apps.map((app) => (
          <InstalledCard key={app.slug} app={app} view={view} isAdmin={isAdmin} busy={busy[app.slug]} onAction={onAction} />
        ))}
      </div>

      <ConfirmDialog
        open={dialog?.kind === 'stop'}
        onClose={close}
        onConfirm={async () => {
          if (target && (await lifecycle(target, 'stop'))) close()
          else close()
        }}
        title={t('stopTitle', { name: target?.name ?? '' })}
        message={t('stopMessage')}
        confirmLabel={t('stop')}
        cancelLabel={t('cancel')}
      />
      <ConfirmDialog
        open={dialog?.kind === 'update'}
        onClose={close}
        onConfirm={async () => {
          if (target) await startUpdate(target)
        }}
        title={t('updateTitle', { name: target?.name ?? '' })}
        message={t('updateMessage')}
        warning={t('updateWarning')}
        confirmLabel={t('updateConfirm')}
        cancelLabel={t('cancel')}
        error={error}
      />
      <LogsDialog app={dialog?.kind === 'logs' ? dialog.app : null} onClose={close} />
      <AppDialog slug={dialog?.kind === 'settings' ? dialog.app.slug : null} mode="settings" onClose={close} onChanged={onChanged} />
      <UninstallDialog app={dialog?.kind === 'uninstall' ? dialog.app : null} onClose={close} onDone={onChanged} />
      <JobDialog job={dialog?.kind === 'job' ? dialog.job : null} onClose={close} onFinished={onChanged} />
    </>
  )
}
