import { useCallback, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { HardDrive, RefreshCw } from 'lucide-react'
import {
  Alert,
  Button,
  Card,
  CardHeader,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  PageHeader,
  Skeleton,
  Status,
  Switch,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { useEventSource } from '@/hooks/useStream'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { DiskCard, type DiskActions } from './DiskCard'
import { BrowseBlockedDialog, FormatDialog, MountDialog, SmartDialog, filesPath } from './dialogs'
import { t } from './strings'
import type { Device, DisksResponse, MountPointInfo, PersistentMount } from './types'

export { StorageSettingsSection } from './settings'

export default function StoragePage() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const navigate = useNavigate()
  const [showAll, setShowAll] = useState(false)
  const disks = useQuery<DisksResponse>('/storage/disks', { query: { all: showAll ? 'true' : undefined } })

  const [mountTarget, setMountTarget] = useState<Device | null>(null)
  const [unmountTarget, setUnmountTarget] = useState<Device | null>(null)
  const [formatTarget, setFormatTarget] = useState<Device | null>(null)
  const [smartTarget, setSmartTarget] = useState<Device | null>(null)
  const [blockedPath, setBlockedPath] = useState<string | null>(null)
  const [orphan, setOrphan] = useState<PersistentMount | null>(null)
  const [unmountError, setUnmountError] = useState<string | null>(null)
  const [orphanError, setOrphanError] = useState<string | null>(null)
  const [pending, setPending] = useState<string | null>(null)

  const { reload } = disks
  const timer = useRef<number | null>(null)
  // Several kernel events arrive for one plugged disk; reload once.
  const onEvent = useCallback(() => {
    if (timer.current !== null) window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => {
      timer.current = null
      void reload()
    }, 300)
  }, [reload])
  const stream = useEventSource('/storage/events', onEvent, { events: ['devices'] })
  const refresh = useCallback(() => void reload(), [reload])

  const persist = useAction(
    async (d: Device, enable: boolean) => {
      setPending(d.name)
      try {
        if (enable) await api.post('/storage/persist', { device: d.name })
        else await api.del(`/storage/persist/${encodeURIComponent(d.uuid)}`)
        return enable
      } finally {
        setPending(null)
      }
    },
    {
      onSuccess: (enabled) => {
        toast.success(enabled ? t('persistDone') : t('persistRemoved'))
        refresh()
      },
      onError: (m) => toast.error(m),
    },
  )

  const unmount = async () => {
    const d = unmountTarget
    if (!d) return
    setUnmountError(null)
    setPending(d.name)
    try {
      await api.post('/storage/unmount', { device: d.name })
      toast.success(t('unmountDone', { name: d.name }))
      setUnmountTarget(null)
    } catch (e) {
      setUnmountError(errorMessage(e))
    } finally {
      setPending(null)
      refresh()
    }
  }

  const removeOrphan = async () => {
    if (!orphan) return
    setOrphanError(null)
    try {
      await api.del(`/storage/persist/${encodeURIComponent(orphan.uuid)}`)
      toast.success(t('persistRemoved'))
      setOrphan(null)
      refresh()
    } catch (e) {
      setOrphanError(errorMessage(e))
    }
  }

  const data = disks.data
  const actions: DiskActions = {
    isAdmin,
    detection: data?.system_detection ?? false,
    smartInstalled: data?.smart_installed ?? false,
    tempWarning: data?.temp_warning ?? 55,
    pending,
    onMount: setMountTarget,
    onUnmount: (d) => {
      setUnmountError(null)
      setUnmountTarget(d)
    },
    onBrowse: (mp: MountPointInfo) => {
      if (mp.browsable) navigate(filesPath(mp.path))
      else setBlockedPath(mp.path)
    },
    onFormat: setFormatTarget,
    onSmart: setSmartTarget,
    onPersist: (d, enable) => void persist.run(d, enable),
  }
  const orphans = data?.persistent_mounts.filter((p) => !p.present) ?? []

  return (
    <div>
      <PageHeader
        title={t('title')}
        description={t('description')}
        icon={HardDrive}
        actions={
          <>
            <Status tone={stream === 'open' ? 'success' : 'neutral'} pulse={stream === 'open'}>
              {stream === 'open' ? t('live') : t('liveOff')}
            </Status>
            <label className="flex min-h-10 items-center gap-2 text-xs text-muted">
              <Switch checked={showAll} onChange={setShowAll} label={t('showAll')} />
              <span aria-hidden>{t('showAll')}</span>
            </label>
            <Button icon={RefreshCw} onClick={refresh} loading={disks.fetching && !disks.loading}>
              {t('refresh')}
            </Button>
          </>
        }
      />

      {disks.loading ? (
        <div className="grid gap-4 xl:grid-cols-2" aria-busy="true" aria-label={t('loading')}>
          <Skeleton className="h-56" />
          <Skeleton className="h-56" />
        </div>
      ) : disks.error && !data ? (
        <Card>
          <ErrorState message={disks.error} onRetry={refresh} />
        </Card>
      ) : !data || data.devices.length === 0 ? (
        <Card>
          <EmptyState icon={HardDrive} title={t('emptyTitle')} description={t('emptyDescription')} />
        </Card>
      ) : (
        <div className="flex flex-col gap-4">
          {disks.error && <Alert tone="danger">{disks.error}</Alert>}
          {!data.system_detection && (
            <Alert tone="danger" title={t('detectionTitle')}>
              {t('detectionBody')}
            </Alert>
          )}
          {!data.smart_installed && (
            <Alert tone="warning" title={t('smartMissingTitle')}>
              {t('smartMissingBody')}
            </Alert>
          )}
          <div className="grid items-start gap-4 xl:grid-cols-2">
            {data.devices.map((d) => (
              <DiskCard key={d.name} disk={d} actions={actions} />
            ))}
          </div>
          {orphans.length > 0 && (
            <Card>
              <CardHeader title={t('orphanTitle')} subtitle={t('orphanDescription')} />
              <ul className="flex flex-col gap-2">
                {orphans.map((p) => (
                  <li key={p.uuid} className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-line bg-surface px-3 py-2">
                    <div className="min-w-0 text-xs">
                      <p className="font-mono text-sm text-fg">{p.mountpoint}</p>
                      <p className="mt-0.5 break-all text-muted">
                        {p.fstype} · UUID {p.uuid}
                      </p>
                    </div>
                    {isAdmin && (
                      <Button
                        variant="danger"
                        onClick={() => {
                          setOrphanError(null)
                          setOrphan(p)
                        }}
                      >
                        {t('remove')}
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            </Card>
          )}
        </div>
      )}

      <MountDialog device={mountTarget} onClose={() => setMountTarget(null)} onDone={refresh} />
      <FormatDialog device={formatTarget} onClose={() => setFormatTarget(null)} onDone={refresh} />
      <SmartDialog device={smartTarget} onClose={() => setSmartTarget(null)} onDone={refresh} />
      <BrowseBlockedDialog path={blockedPath} onClose={() => setBlockedPath(null)} onDone={refresh} />
      <ConfirmDialog
        open={unmountTarget !== null}
        onClose={() => setUnmountTarget(null)}
        onConfirm={unmount}
        title={t('unmountTitle', { name: unmountTarget?.name ?? '' })}
        message={t('unmountMessage', { path: unmountTarget?.mountpoints.map((m) => m.path).join(', ') ?? '' })}
        warning={t('unmountWarning')}
        confirmLabel={t('unmount')}
        cancelLabel={t('cancel')}
        error={unmountError}
      />
      <ConfirmDialog
        open={orphan !== null}
        onClose={() => setOrphan(null)}
        onConfirm={removeOrphan}
        danger
        title={t('persistRemoveTitle')}
        message={t('persistRemoveMessage', { path: orphan?.mountpoint ?? '' })}
        confirmLabel={t('remove')}
        cancelLabel={t('cancel')}
        error={orphanError}
      />
    </div>
  )
}
