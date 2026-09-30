import { useEffect, useState } from 'react'
import { Alert, Button, ConfirmDialog, Modal } from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { api } from '@/services/api'
import { toast } from '@/stores/ui'
import { cx } from '@/lib/format'
import { t } from './strings'
import type { AppDetail, InstalledApp, UninstallResult } from './types'

function Choice({
  checked,
  onSelect,
  title,
  text,
  danger,
  name,
}: {
  checked: boolean
  onSelect: () => void
  title: string
  text: string
  danger?: boolean
  name: string
}) {
  return (
    <label
      className={cx(
        'flex cursor-pointer items-start gap-3 rounded-xl border px-3 py-3 transition-colors',
        checked ? (danger ? 'border-danger/50 bg-danger/10' : 'border-accent/50 bg-accent/10') : 'border-line hover:border-line-strong',
      )}
    >
      <input type="radio" name={name} checked={checked} onChange={onSelect} className="mt-1 size-4 accent-accent" />
      <span className="min-w-0">
        <span className={cx('block text-sm font-medium', danger ? 'text-danger' : 'text-fg')}>{title}</span>
        <span className="mt-0.5 block text-xs text-muted">{text}</span>
      </span>
    </label>
  )
}

/** Uninstall in two steps: first the choice about the data (kept by
 *  default), then a separate confirmation. Deleting the data additionally
 *  requires typing the application's id. */
export function UninstallDialog({ app, onClose, onDone }: { app: InstalledApp | null; onClose: () => void; onDone: () => void }) {
  const [deleteData, setDeleteData] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const detail = useQuery<AppDetail>(app && app.manifest_available ? `/apps/catalog/${app.slug}` : null)

  useEffect(() => {
    setDeleteData(false)
    setConfirming(false)
  }, [app?.slug])

  const remove = useAction(
    (slug: string, withData: boolean) =>
      api.post<UninstallResult>(`/apps/installed/${slug}/uninstall`, { delete_data: withData, confirm: withData ? slug : '' }),
    {
      onSuccess: (res) => {
        if (!app) return
        if (res.failed_volumes.length > 0) {
          toast.warning(t('uninstalledPartial', { name: app.name, volumes: res.failed_volumes.join(', ') }))
        } else if (res.removed_volumes.length > 0 || deleteData) {
          toast.success(t('uninstalledDeleted', { name: app.name }))
        } else {
          toast.success(t('uninstalled', { name: app.name }))
        }
        onDone()
        onClose()
      },
    },
  )

  if (!app) return null
  const volumes = detail.data?.slug === app.slug ? detail.data.volumes.filter((v) => v.type === 'volume') : []
  const volumeNames = Array.from(new Set(volumes.map((v) => v.source)))

  return (
    <>
      <Modal
        open={!confirming}
        onClose={onClose}
        size="md"
        title={t('uninstallTitle', { name: app.name })}
        footer={
          <>
            <Button variant="ghost" onClick={onClose} data-autofocus>
              {t('cancel')}
            </Button>
            <Button variant="danger" onClick={() => setConfirming(true)}>
              {t('continue')}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-3">
          <p className="text-sm text-fg">{t('uninstallMessage')}</p>
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-2 text-xs font-medium text-muted">{t('uninstallChoice')}</legend>
            <Choice name="apps-uninstall-data" checked={!deleteData} onSelect={() => setDeleteData(false)} title={t('keepData')} text={t('keepDataText')} />
            <Choice
              name="apps-uninstall-data"
              checked={deleteData}
              onSelect={() => setDeleteData(true)}
              title={t('deleteData')}
              text={t('deleteDataText')}
              danger
            />
          </fieldset>
          {deleteData && volumeNames.length > 0 && (
            <Alert tone="danger" title={t('volumesToDelete')}>
              <ul className="font-mono">
                {volumeNames.map((v) => (
                  <li key={v} className="break-all">
                    {v}
                  </li>
                ))}
              </ul>
            </Alert>
          )}
          <p className="text-xs text-faint">{t('pathsKept')}.</p>
        </div>
      </Modal>
      <ConfirmDialog
        open={confirming}
        onClose={() => {
          if (remove.pending) return
          remove.clearError()
          setConfirming(false)
        }}
        onConfirm={async () => {
          await remove.run(app.slug, deleteData)
        }}
        danger
        title={t('uninstallConfirmTitle', { name: app.name })}
        message={deleteData ? t('uninstallConfirmDelete') : t('uninstallConfirmKeep')}
        warning={deleteData ? t('uninstallDeleteWarning') : undefined}
        requireText={deleteData ? app.slug : undefined}
        confirmLabel={t('uninstall')}
        cancelLabel={t('cancel')}
        pending={remove.pending}
        error={remove.error}
      />
    </>
  )
}
