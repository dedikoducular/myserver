import { useState, type ReactNode } from 'react'
import {
  Boxes,
  ExternalLink,
  FileText,
  LayoutGrid,
  List,
  MoreHorizontal,
  Play,
  RefreshCw,
  RotateCw,
  Settings,
  ShieldAlert,
  Square,
  Trash2,
  Activity,
} from 'lucide-react'
import { Badge, Button, Card, Menu, Status, type MenuItem } from '@/components/ui'
import { cx } from '@/lib/format'
import { t } from './strings'
import type { CatalogApp, InstalledApp, ViewMode } from './types'
import { iconUrl, isUp, openWebUI, operationLabel, portAddress, stateInfo } from './util'

export function AppIcon({ icon, size = 'md' }: { icon: string; size?: 'sm' | 'md' | 'lg' }) {
  const [failed, setFailed] = useState(false)
  const url = iconUrl(icon)
  const box = size === 'sm' ? 'size-9 rounded-lg' : size === 'lg' ? 'size-14 rounded-2xl' : 'size-12 rounded-xl'
  if (!url || failed) {
    return (
      <span className={cx('inline-flex shrink-0 items-center justify-center border border-line bg-raised text-muted', box)} aria-hidden>
        <Boxes className={size === 'sm' ? 'size-4' : 'size-6'} />
      </span>
    )
  }
  return <img src={url} alt="" loading="lazy" onError={() => setFailed(true)} className={cx('shrink-0 object-cover', box)} />
}

export function ViewToggle({ value, onChange }: { value: ViewMode; onChange: (mode: ViewMode) => void }) {
  const item = (mode: ViewMode, label: string, Icon: typeof List) => (
    <button
      type="button"
      aria-label={label}
      title={label}
      aria-pressed={value === mode}
      onClick={() => onChange(mode)}
      className={cx(
        'inline-flex size-10 items-center justify-center rounded-lg transition-colors sm:size-8',
        value === mode ? 'bg-raised text-accent' : 'text-muted hover:text-fg',
      )}
    >
      <Icon className="size-4" aria-hidden />
    </button>
  )
  return (
    <div className="flex shrink-0 gap-1 rounded-xl border border-line bg-surface p-1">
      {item('grid', t('gridView'), LayoutGrid)}
      {item('list', t('listView'), List)}
    </div>
  )
}

export function AppStatus({ app, busy }: { app: InstalledApp; busy?: string }) {
  const op = busy || app.operation
  if (op) {
    return (
      <Status tone="accent" pulse>
        {operationLabel(op)}
      </Status>
    )
  }
  const info = stateInfo(app.state)
  return (
    <Status tone={info.tone} pulse={info.pulse}>
      {info.label}
    </Status>
  )
}

export type AppAction = 'open' | 'start' | 'stop' | 'restart' | 'update' | 'logs' | 'settings' | 'uninstall' | 'progress'

export function appMenu(app: InstalledApp, isAdmin: boolean, busy: string | undefined, run: (action: AppAction) => void): MenuItem[] {
  const items: MenuItem[] = []
  const working = Boolean(busy || app.operation)
  const up = isUp(app)
  if (app.web_ui) {
    const local = app.web_ui.local_only
    items.push({ label: local ? t('openLocalOnly') : t('open'), icon: ExternalLink, onSelect: () => run('open'), disabled: !up || local })
  }
  if (!isAdmin) return items
  if (app.job_id) {
    items.push({ label: t('progress'), icon: Activity, onSelect: () => run('progress') })
  }
  items.push(
    { label: t('start'), icon: Play, onSelect: () => run('start'), disabled: working || app.state === 'running' || app.state === 'missing', separated: items.length > 0 },
    { label: t('stop'), icon: Square, onSelect: () => run('stop'), disabled: working || !up },
    { label: t('restart'), icon: RotateCw, onSelect: () => run('restart'), disabled: working || app.state === 'missing' },
    { label: t('update'), icon: RefreshCw, onSelect: () => run('update'), disabled: working, separated: true },
    { label: t('logs'), icon: FileText, onSelect: () => run('logs'), disabled: app.state === 'missing' || app.state === 'unknown' },
    { label: t('settings'), icon: Settings, onSelect: () => run('settings'), disabled: working },
    { label: t('uninstall'), icon: Trash2, onSelect: () => run('uninstall'), disabled: working, danger: true, separated: true },
  )
  return items
}

export function InstalledCard({
  app,
  view,
  isAdmin,
  busy,
  onAction,
}: {
  app: InstalledApp
  view: ViewMode
  isAdmin: boolean
  busy?: string
  onAction: (app: InstalledApp, action: AppAction) => void
}) {
  const items = appMenu(app, isAdmin, busy, (action) => onAction(app, action))
  const menu = items.length > 0 && (
    <Menu label={t('menuLabel', { name: app.name })} items={items} trigger={<MoreHorizontal className="size-5" aria-hidden />} />
  )
  const canOpen = Boolean(app.web_ui) && !app.web_ui?.local_only && isUp(app)
  const exposure = app.ports.length > 0 && (
    <p
      className="truncate font-mono text-[11px] text-faint"
      title={
        (app.bind_address === 'loopback' ? t('exposedLocal') : t('exposedAll')) + ': ' + app.ports.map((p) => portAddress(p)).join(', ')
      }
    >
      {app.ports.map((p) => portAddress(p)).join(' · ')}
    </p>
  )
  const title = canOpen ? (
    <button
      type="button"
      onClick={() => app.web_ui && openWebUI(app.web_ui)}
      className="max-w-full truncate text-left text-sm font-semibold text-fg hover:text-accent"
      title={t('open')}
    >
      {app.name}
    </button>
  ) : (
    <p className="truncate text-sm font-semibold text-fg">{app.name}</p>
  )

  if (view === 'list') {
    return (
      <Card padded={false} className="flex items-center gap-3 px-3 py-2.5">
        <AppIcon icon={app.icon} size="sm" />
        <div className="min-w-0 flex-1">
          {title}
          <p className="truncate text-xs text-muted">{app.description}</p>
          {exposure}
        </div>
        <div className="hidden shrink-0 sm:block">
          {app.custom ? <CustomBadge custom /> : <Badge>{app.category_label}</Badge>}
        </div>
        <div className="shrink-0">
          <AppStatus app={app} busy={busy} />
        </div>
        {menu}
      </Card>
    )
  }
  return (
    <Card padded={false} className="flex gap-3 p-3.5">
      <AppIcon icon={app.icon} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex min-w-0 items-center gap-1.5">
          <div className="min-w-0">{title}</div>
          <CustomBadge custom={app.custom} />
        </div>
        <p className="truncate text-xs text-muted">{app.description}</p>
        {exposure}
        <div className="mt-1.5 flex items-center justify-between gap-2">
          <AppStatus app={app} busy={busy} />
          {menu}
        </div>
      </div>
    </Card>
  )
}

function CustomBadge({ custom }: { custom: boolean }) {
  return custom ? <Badge tone="purple">{t('customBadge')}</Badge> : null
}

export function CatalogCard({
  app,
  view,
  isAdmin,
  onSelect,
  onDeleteCustom,
}: {
  app: CatalogApp
  view: ViewMode
  isAdmin: boolean
  onSelect: (app: CatalogApp) => void
  /** Deletes the definition of a custom application that is not installed. */
  onDeleteCustom?: (app: CatalogApp) => void
}) {
  const menu = app.custom && isAdmin && !app.installed && onDeleteCustom && (
    <Menu
      label={t('customMenu', { name: app.name })}
      items={[{ label: t('customDelete'), icon: Trash2, danger: true, disabled: Boolean(app.operation), onSelect: () => onDeleteCustom(app) }]}
      trigger={<MoreHorizontal className="size-5" aria-hidden />}
    />
  )
  let action: ReactNode
  if (app.operation === 'install') {
    action = (
      <Button size="sm" variant="secondary" loading onClick={() => onSelect(app)} className="pointer-events-auto">
        {t('installing')}
      </Button>
    )
  } else if (app.installed) {
    action = <Badge tone="success">{t('installed')}</Badge>
  } else if (!app.installable) {
    action = (
      <Button size="sm" variant="secondary" onClick={() => onSelect(app)} title={app.unsupported_reason}>
        {t('unsupported')}
      </Button>
    )
  } else {
    action = (
      <Button size="sm" variant={isAdmin ? 'primary' : 'secondary'} onClick={() => onSelect(app)}>
        {isAdmin ? t('install') : t('details')}
      </Button>
    )
  }
  const risk = app.warning_level === 'danger' && (
    <span className="inline-flex text-warning" title={t('securityWarnings')}>
      <ShieldAlert className="size-4" aria-label={t('securityWarnings')} />
    </span>
  )
  if (view === 'list') {
    return (
      <Card padded={false} className="flex items-center gap-3 px-3 py-2.5">
        <AppIcon icon={app.icon} size="sm" />
        <div className="min-w-0 flex-1">
          <p className="flex items-center gap-1.5 text-sm font-semibold text-fg">
            <span className="truncate">{app.name}</span>
            {risk}
          </p>
          <p className="truncate text-xs text-muted">{app.description}</p>
        </div>
        <div className="hidden shrink-0 sm:block">
          {app.custom ? <CustomBadge custom /> : <Badge>{app.category_label}</Badge>}
        </div>
        <div className="shrink-0">{action}</div>
        {menu}
      </Card>
    )
  }
  return (
    <Card padded={false} className="flex gap-3 p-3.5">
      <AppIcon icon={app.icon} />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <div className="flex items-center gap-1.5 text-sm font-semibold text-fg">
          <span className="truncate">{app.name}</span>
          {risk}
          {menu && <div className="-my-1 ml-auto shrink-0">{menu}</div>}
        </div>
        <p className="truncate text-xs text-muted">{app.description}</p>
        <div className="mt-1.5 flex items-center justify-between gap-2">
          {app.custom ? <CustomBadge custom /> : <Badge>{app.category_label}</Badge>}
          {action}
        </div>
      </div>
    </Card>
  )
}

/** Cards flow into as many columns as the container allows, so the same
 *  grid works on the page and inside a dashboard widget of any width. */
export function gridClass(view: ViewMode): string {
  if (view === 'list') return 'flex flex-col gap-2'
  return 'grid grid-cols-[repeat(auto-fill,minmax(min(100%,230px),1fr))] gap-3'
}
