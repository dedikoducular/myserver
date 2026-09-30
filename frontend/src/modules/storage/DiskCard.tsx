import { Activity, Disc, Eraser, FolderOpen, HardDrive, Link2, Lock, Pin, PinOff, Thermometer, Unlink, Usb } from 'lucide-react'
import { Badge, Button, Card, IconTile, ProgressBar } from '@/components/ui'
import { cx, formatBytes, formatPercent, formatTemperature } from '@/lib/format'
import { busLabel, isRemovable, smartLabel, smartTone, t, typeLabel } from './strings'
import type { Device, MountPointInfo } from './types'

export interface DiskActions {
  isAdmin: boolean
  /** False when the system disk could not be determined: nothing may change. */
  detection: boolean
  smartInstalled: boolean
  tempWarning: number
  pending: string | null
  onMount: (d: Device) => void
  onUnmount: (d: Device) => void
  onBrowse: (mp: MountPointInfo) => void
  onFormat: (d: Device) => void
  onSmart: (d: Device) => void
  onPersist: (d: Device, enable: boolean) => void
}

interface Row {
  device: Device
  depth: number
}

function rows(disk: Device): Row[] {
  const out: Row[] = []
  const walk = (list: Device[], depth: number) => {
    for (const d of list) {
      out.push({ device: d, depth })
      walk(d.children, depth + 1)
    }
  }
  walk(disk.children, 0)
  // A disk formatted without a partition table carries the filesystem itself.
  if (out.length === 0 && (disk.fstype || disk.mountpoints.length > 0)) out.push({ device: disk, depth: 0 })
  return out
}

function systemText(d: Device): string {
  return d.system_reason ? t('systemExplain', { reason: d.system_reason }) : t('systemExplainPlain')
}

function VolumeRow({ row, actions }: { row: Row; actions: DiskActions }) {
  const d = row.device
  const mp = d.mountpoints[0]
  const locked = d.system || !actions.detection
  const canAct = actions.isAdmin && d.manageable && !locked
  const busy = actions.pending === d.name
  const formattable = canAct && d.type === 'part' && !mp && !d.in_use && !d.read_only

  return (
    <li className={cx('rounded-xl border border-line bg-surface p-3', row.depth > 0 && 'ml-4 sm:ml-6')}>
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="font-mono text-sm font-semibold text-fg">{d.name}</span>
            <Badge>{typeLabel(d.type)}</Badge>
            {d.fstype && <Badge tone="accent">{d.fstype}</Badge>}
            {d.swap && <Badge tone="purple">{t('swap')}</Badge>}
            {d.persistent && <Badge tone="cyan">{t('persistent')}</Badge>}
            {mp?.read_only && <Badge tone="warning">{t('readOnly')}</Badge>}
            {d.system && (
              <Badge tone="warning">
                <Lock className="size-3" aria-hidden />
                {t('systemDisk')}
              </Badge>
            )}
          </div>
          <p className="mt-1 break-all text-xs text-muted">
            {formatBytes(d.size)}
            {d.label && ` · ${d.label}`}
            {' · '}
            {mp ? d.mountpoints.map((m) => m.path).join(', ') : d.swap ? t('swap') : d.fstype ? t('notMounted') : t('noFilesystem')}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {mp && (
            <Button icon={FolderOpen} onClick={() => actions.onBrowse(mp)}>
              {t('browse')}
            </Button>
          )}
          {actions.isAdmin && d.manageable && !mp && d.fstype && !d.swap && (
            <Button icon={Link2} variant="primary" disabled={!d.mountable || locked || busy} onClick={() => actions.onMount(d)}>
              {t('mount')}
            </Button>
          )}
          {actions.isAdmin && d.manageable && mp && (
            <Button icon={Unlink} disabled={locked} loading={busy} onClick={() => actions.onUnmount(d)}>
              {t('unmount')}
            </Button>
          )}
          {canAct && mp && d.uuid && (
            <Button icon={d.persistent ? PinOff : Pin} variant="ghost" disabled={busy} onClick={() => actions.onPersist(d, !d.persistent)}>
              {d.persistent ? t('removePersistent') : t('makePersistent')}
            </Button>
          )}
          {actions.isAdmin && d.type === 'part' && d.manageable && (
            <Button icon={Eraser} variant="danger" disabled={!formattable} onClick={() => actions.onFormat(d)}>
              {t('format')}
            </Button>
          )}
        </div>
      </div>
      {d.usage && (
        <div className="mt-3">
          <div className="mb-1.5 flex items-center justify-between gap-3 text-xs">
            <span className="text-muted">
              {t('usedOf', { used: formatBytes(d.usage.used), total: formatBytes(d.usage.total) })}
              <span className="text-faint"> · {t('free', { size: formatBytes(d.usage.free) })}</span>
            </span>
            <span className="font-medium text-fg">{formatPercent(d.usage.percent)}</span>
          </div>
          <ProgressBar value={d.usage.percent} label={t('usageLabel', { name: d.name })} />
        </div>
      )}
    </li>
  )
}

export function DiskCard({ disk, actions }: { disk: Device; actions: DiskActions }) {
  const removable = isRemovable(disk)
  const bus = busLabel(disk.transport)
  const list = rows(disk)
  const locked = disk.system || !actions.detection
  const model = [disk.vendor, disk.model].filter(Boolean).join(' ') || t('unknownModel')
  const temp = disk.smart?.temperature ?? null
  const hot = temp != null && temp >= actions.tempWarning
  const mounted = list.some((r) => r.device.mountpoints.length > 0 || r.device.in_use)
  const physical = disk.type === 'disk' && /^(sd|vd|xvd|nvme|mmcblk)/.test(disk.name)
  const canFormat = actions.isAdmin && physical && !locked && !mounted && !disk.read_only && !disk.in_use
  const Icon = disk.type === 'rom' ? Disc : disk.transport === 'usb' ? Usb : HardDrive

  return (
    <Card>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-1 items-start gap-3">
          <IconTile icon={Icon} tone={disk.system ? 'warning' : removable ? 'cyan' : 'accent'} />
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-1.5">
              <h2 className="font-mono text-base font-semibold text-fg">{disk.name}</h2>
              {bus && <Badge tone="accent">{bus}</Badge>}
              {disk.type === 'disk' && <Badge>{disk.rotational ? t('hdd') : t('ssd')}</Badge>}
              {disk.type !== 'disk' && <Badge>{typeLabel(disk.type)}</Badge>}
              {removable && <Badge tone="cyan">{t('removable')}</Badge>}
              {disk.read_only && <Badge tone="warning">{t('readOnly')}</Badge>}
              {disk.system && (
                <Badge tone="warning">
                  <Lock className="size-3" aria-hidden />
                  {t('systemDisk')}
                </Badge>
              )}
            </div>
            <p className="mt-0.5 truncate text-sm text-muted">{model}</p>
            <p className="mt-0.5 break-all text-xs text-faint">
              {t('capacity')}: {formatBytes(disk.size)}
              {disk.serial && ` · ${t('serial')}: ${disk.serial}`}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {physical && (
            <>
              <span className={cx('inline-flex items-center gap-1 text-sm font-medium', hot ? 'text-warning' : 'text-muted')} title={t('temperature')}>
                <Thermometer className="size-4" aria-hidden />
                <span className="sr-only">{t('temperature')}: </span>
                {formatTemperature(temp)}
              </span>
              <Badge tone={smartTone(disk.smart?.status)}>
                {actions.smartInstalled || disk.smart ? smartLabel(disk.smart?.status) : t('smartNotInstalled')}
              </Badge>
            </>
          )}
        </div>
      </div>

      {disk.system && (
        <p className="mt-3 flex items-start gap-2 rounded-xl border border-warning/25 bg-warning/12 px-3 py-2 text-xs text-muted">
          <Lock className="mt-0.5 size-3.5 shrink-0 text-warning" aria-hidden />
          {systemText(disk)}
        </p>
      )}

      {list.length > 0 ? (
        <ul className="mt-4 flex flex-col gap-2">
          {list.map((r) => (
            <VolumeRow key={r.device.name + ':' + r.depth} row={r} actions={actions} />
          ))}
        </ul>
      ) : (
        <p className="mt-4 text-xs text-faint">{t('noPartitions')}</p>
      )}

      {physical && (
        <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-line pt-4">
          <Button icon={Activity} onClick={() => actions.onSmart(disk)}>
            {t('smart')}
          </Button>
          {actions.isAdmin && (
            <Button icon={Eraser} variant="danger" disabled={!canFormat} onClick={() => actions.onFormat(disk)}>
              {t('format')}
            </Button>
          )}
        </div>
      )}
    </Card>
  )
}
