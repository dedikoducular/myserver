import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent, type KeyboardEvent, type MouseEvent, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  ArrowDown,
  ArrowUp,
  ChevronRight,
  Copy,
  CornerLeftUp,
  Download,
  FileArchive,
  FolderInput,
  FolderOpen,
  FolderPlus,
  HardDrive,
  Info,
  LayoutGrid,
  List,
  MoreVertical,
  PackageOpen,
  Pencil,
  RefreshCw,
  Trash2,
  Upload,
  UploadCloud,
  X,
  type LucideIcon,
} from 'lucide-react'
import {
  Alert,
  Button,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  LoadingState,
  Modal,
  PageHeader,
  ProgressBar,
  Select,
  type MenuItem,
} from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { cx, formatBytes, formatShortDateTime } from '@/lib/format'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { DeleteDialog, DestinationDialog, MediaDialog, NameDialog, PropertiesDialog, TextDialog, type DestinationMode } from './dialogs'
import { JobsPanel, jobKindLabel, useJobs } from './jobs'
import { t } from './strings'
import type { FileEntry, Job, ListResponse, RootInfo, RootsResponse, SortKey, SortOrder } from './types'
import { UploadsPanel, useUploads } from './uploads'
import { crumbs, entryIcon, iconClass, isArchive, isDirLike, previewKind, startDownload, type PreviewKind } from './util'

const PAGE_STEP = 1000
const PAGE_MAX = 5000
const VIEW_KEY = 'myserver.files.view'

type View = 'list' | 'grid'

type Dialog =
  | { kind: 'mkdir' }
  | { kind: 'rename'; entry: FileEntry }
  | { kind: 'delete'; entries: FileEntry[] }
  | { kind: 'zip'; entries: FileEntry[] }
  | { kind: DestinationMode; entries: FileEntry[] }
  | { kind: 'properties'; entry: FileEntry }
  | { kind: 'media'; entry: FileEntry; preview: PreviewKind }
  | { kind: 'text'; entry: FileEntry }

interface MenuState {
  title: string
  items: MenuItem[]
  position: { x: number; y: number } | null
}

function readView(): View {
  try {
    return localStorage.getItem(VIEW_KEY) === 'grid' ? 'grid' : 'list'
  } catch {
    return 'list'
  }
}

/* ---------- places ---------- */

function usagePercent(r: RootInfo): number | null {
  if (r.total_bytes == null || r.free_bytes == null || r.total_bytes <= 0) return null
  return ((r.total_bytes - r.free_bytes) / r.total_bytes) * 100
}

function PlaceButton({ root, active, onOpen }: { root: RootInfo; active: boolean; onOpen: (path: string) => void }) {
  const pct = usagePercent(root)
  return (
    <button
      type="button"
      disabled={!root.exists}
      onClick={() => onOpen(root.path)}
      aria-current={active ? 'page' : undefined}
      className={cx(
        'flex w-full min-w-0 flex-col gap-1.5 rounded-xl border px-3 py-2.5 text-left transition-colors disabled:opacity-50',
        active ? 'border-accent/40 bg-accent/12' : 'border-transparent hover:bg-raised',
      )}
    >
      <span className="flex items-center gap-2.5">
        <HardDrive className={cx('size-4 shrink-0', active ? 'text-accent' : 'text-muted')} aria-hidden />
        <span className="min-w-0 flex-1 truncate font-mono text-sm text-fg">{root.path}</span>
      </span>
      {!root.exists ? (
        <span className="text-xs text-warning">{t('rootMissing')}</span>
      ) : (
        pct !== null && (
          <>
            <ProgressBar value={pct} label={t('rootUsage', { name: root.path })} />
            <span className="text-[11px] text-muted">
              {t('rootFree', { free: formatBytes(root.free_bytes), total: formatBytes(root.total_bytes) })}
            </span>
          </>
        )
      )}
    </button>
  )
}

/* ---------- action menu: floating on desktop, bottom sheet on phones ---------- */

function ActionMenu({ menu, onClose }: { menu: MenuState | null; onClose: () => void }) {
  const box = useRef<HTMLDivElement>(null)
  const floating = menu !== null && menu.position !== null && window.matchMedia('(min-width: 640px)').matches

  useEffect(() => {
    if (!floating) return
    const onDown = (e: globalThis.MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) onClose()
    }
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose()
        return
      }
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
      e.preventDefault()
      const els = Array.from(box.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([disabled])') ?? [])
      if (els.length === 0) return
      const i = els.indexOf(document.activeElement as HTMLElement)
      els[e.key === 'ArrowDown' ? (i + 1) % els.length : (i - 1 + els.length) % els.length]?.focus()
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    window.addEventListener('resize', onClose)
    window.addEventListener('scroll', onClose, true)
    box.current?.querySelector<HTMLElement>('[role="menuitem"]:not([disabled])')?.focus()
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('resize', onClose)
      window.removeEventListener('scroll', onClose, true)
    }
  }, [floating, onClose, menu])

  if (!menu) return null
  const run = (item: MenuItem) => {
    onClose()
    item.onSelect()
  }

  if (floating && menu.position) {
    const width = 224
    const height = menu.items.length * 40 + 16
    const left = Math.max(8, Math.min(menu.position.x, window.innerWidth - width - 8))
    const top = Math.max(8, Math.min(menu.position.y, window.innerHeight - height - 8))
    return (
      <div
        ref={box}
        role="menu"
        aria-label={menu.title}
        style={{ left, top, width }}
        className="ms-fade-in fixed z-50 rounded-xl border border-line bg-card p-1 shadow-card"
      >
        {menu.items.map((item) => (
          <div key={item.label}>
            {item.separated && <div className="my-1 border-t border-line" />}
            <button
              type="button"
              role="menuitem"
              disabled={item.disabled}
              onClick={() => run(item)}
              className={cx(
                'flex h-10 w-full items-center gap-2.5 rounded-lg px-2.5 text-left text-sm transition-colors disabled:opacity-40',
                item.danger ? 'text-danger hover:bg-danger/10' : 'text-fg hover:bg-raised',
              )}
            >
              {item.icon && <item.icon className="size-4 shrink-0" aria-hidden />}
              {item.label}
            </button>
          </div>
        ))}
      </div>
    )
  }

  return (
    <Modal open onClose={onClose} title={menu.title} size="sm">
      <ul className="-mx-2 flex flex-col">
        {menu.items.map((item) => (
          <li key={item.label} className={cx(item.separated && 'mt-1 border-t border-line pt-1')}>
            <button
              type="button"
              disabled={item.disabled}
              onClick={() => run(item)}
              className={cx(
                'flex min-h-12 w-full items-center gap-3 rounded-xl px-3 text-left text-sm transition-colors disabled:opacity-40',
                item.danger ? 'text-danger hover:bg-danger/10' : 'text-fg hover:bg-raised',
              )}
            >
              {item.icon && <item.icon className="size-5 shrink-0" aria-hidden />}
              {item.label}
            </button>
          </li>
        ))}
      </ul>
    </Modal>
  )
}

/* ---------- entries ---------- */

interface EntryProps {
  entry: FileEntry
  selected: boolean
  onOpen: (e: FileEntry) => void
  onSelect: (e: FileEntry, ev: MouseEvent) => void
  onToggle: (e: FileEntry) => void
  onMenu: (e: FileEntry, position: { x: number; y: number } | null) => void
}

function Checkbox({ checked, label, onChange }: { checked: boolean; label: string; onChange: () => void }) {
  return (
    <label className="flex size-10 shrink-0 cursor-pointer items-center justify-center" onClick={(e) => e.stopPropagation()}>
      <input type="checkbox" checked={checked} onChange={onChange} aria-label={label} className="size-4 accent-accent" />
    </label>
  )
}

function subline(e: FileEntry): string {
  const date = formatShortDateTime(e.modified_at)
  return isDirLike(e) ? date : `${formatBytes(e.size)} · ${date}`
}

function EntryRow({ entry, selected, onOpen, onSelect, onToggle, onMenu }: EntryProps) {
  const { icon: Icon, tone } = entryIcon(entry)
  return (
    <li
      data-path={entry.path}
      role="option"
      tabIndex={0}
      aria-selected={selected}
      onClick={(ev) => onSelect(entry, ev)}
      onDoubleClick={() => onOpen(entry)}
      onContextMenu={(ev) => {
        ev.preventDefault()
        onMenu(entry, { x: ev.clientX, y: ev.clientY })
      }}
      className={cx(
        'flex min-h-12 items-center gap-1 border-b border-line/60 pr-1 text-sm transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent',
        selected ? 'bg-accent/12' : 'hover:bg-raised/50',
      )}
    >
      <Checkbox checked={selected} label={t('selectItem', { name: entry.name })} onChange={() => onToggle(entry)} />
      <button
        type="button"
        tabIndex={-1}
        onClick={(ev) => {
          ev.stopPropagation()
          onOpen(entry)
        }}
        className="flex min-h-12 min-w-0 flex-1 items-center gap-3 text-left"
      >
        <Icon className={cx('size-5 shrink-0', iconClass(tone))} aria-hidden />
        <span className="min-w-0">
          <span className="block truncate text-fg">{entry.name}</span>
          <span className="block truncate text-xs text-muted sm:hidden">{subline(entry)}</span>
        </span>
      </button>
      <span className="hidden w-24 shrink-0 text-right text-muted tabular-nums sm:block">{isDirLike(entry) ? '—' : formatBytes(entry.size)}</span>
      <span className="hidden w-28 shrink-0 pl-4 font-mono text-xs text-muted md:block">{entry.mode}</span>
      <span className="hidden w-36 shrink-0 truncate pl-2 text-xs text-muted xl:block">
        {entry.owner ?? '—'}:{entry.group ?? '—'}
      </span>
      <span className="hidden w-36 shrink-0 pl-2 text-xs text-muted tabular-nums sm:block">{formatShortDateTime(entry.modified_at)}</span>
      <IconButton
        icon={MoreVertical}
        label={t('itemActions', { name: entry.name })}
        tabIndex={-1}
        onClick={(ev) => {
          ev.stopPropagation()
          const r = ev.currentTarget.getBoundingClientRect()
          onMenu(entry, { x: r.right - 224, y: r.bottom + 4 })
        }}
      />
    </li>
  )
}

function EntryTile({ entry, selected, onOpen, onSelect, onToggle, onMenu }: EntryProps) {
  const { icon: Icon, tone } = entryIcon(entry)
  return (
    <li
      data-path={entry.path}
      role="option"
      tabIndex={0}
      aria-selected={selected}
      onClick={(ev) => onSelect(entry, ev)}
      onDoubleClick={() => onOpen(entry)}
      onContextMenu={(ev) => {
        ev.preventDefault()
        onMenu(entry, { x: ev.clientX, y: ev.clientY })
      }}
      className={cx(
        'relative flex flex-col rounded-xl border transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent',
        selected ? 'border-accent/40 bg-accent/12' : 'border-line bg-surface hover:border-line-strong',
      )}
    >
      <div className="flex items-center justify-between">
        <Checkbox checked={selected} label={t('selectItem', { name: entry.name })} onChange={() => onToggle(entry)} />
        <IconButton
          icon={MoreVertical}
          label={t('itemActions', { name: entry.name })}
          tabIndex={-1}
          onClick={(ev) => {
            ev.stopPropagation()
            const r = ev.currentTarget.getBoundingClientRect()
            onMenu(entry, { x: r.right - 224, y: r.bottom + 4 })
          }}
        />
      </div>
      <button
        type="button"
        tabIndex={-1}
        onClick={(ev) => {
          ev.stopPropagation()
          onOpen(entry)
        }}
        className="flex min-w-0 flex-col items-center gap-2 px-3 pb-3 text-center"
      >
        <Icon className={cx('size-10', iconClass(tone))} aria-hidden />
        <span className="line-clamp-2 w-full break-words text-xs text-fg">{entry.name}</span>
        <span className="text-[11px] text-muted">{isDirLike(entry) ? t('typeFolder') : formatBytes(entry.size)}</span>
      </button>
    </li>
  )
}

function SortHeader({
  column,
  label,
  sort,
  order,
  onSort,
  className,
}: {
  column: SortKey
  label: string
  sort: SortKey
  order: SortOrder
  onSort: (key: SortKey) => void
  className?: string
}) {
  const active = sort === column
  const Arrow = order === 'asc' ? ArrowUp : ArrowDown
  return (
    <button
      type="button"
      onClick={() => onSort(column)}
      aria-label={t('sortColumn', { column: label })}
      aria-pressed={active}
      className={cx(
        'inline-flex h-9 items-center gap-1 text-[11px] font-medium uppercase tracking-wide transition-colors hover:text-fg',
        active ? 'text-fg' : 'text-faint',
        className,
      )}
    >
      {label}
      {active && <Arrow className="size-3" aria-hidden />}
    </button>
  )
}

/* ---------- page ---------- */

export default function FilesPage() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const [params, setParams] = useSearchParams()
  const path = params.get('path')

  const [view, setView] = useState<View>(readView)
  const [sort, setSort] = useState<SortKey>('name')
  const [order, setOrder] = useState<SortOrder>('asc')
  const [limit, setLimit] = useState(PAGE_STEP)
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set())
  const [dialog, setDialog] = useState<Dialog | null>(null)
  const [menu, setMenu] = useState<MenuState | null>(null)
  const [dragging, setDragging] = useState(false)
  const closeMenu = useCallback(() => setMenu(null), [])
  const anchor = useRef<string | null>(null)
  const dragDepth = useRef(0)
  const fileInput = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLUListElement>(null)

  const roots = useQuery<RootsResponse>('/files/roots')
  const list = useQuery<ListResponse>(path ? '/files/list' : null, {
    query: { path: path ?? '', sort, order, limit },
  })
  // The server returns the cleaned path, which may differ from a hand-typed URL.
  const data = path !== null && list.data && (samePath(list.data.path, path) || !list.fetching) ? list.data : undefined
  const entries = useMemo(() => data?.entries ?? [], [data])

  const pathRef = useRef(path)
  pathRef.current = path
  const reloadList = list.reload
  const reloadRoots = roots.reload
  const refresh = useCallback(() => {
    void reloadList()
    void reloadRoots()
  }, [reloadList, reloadRoots])

  const jobs = useJobs((job: Job) => {
    if (job.kind === 'size') return
    const kind = jobKindLabel(job.kind)
    if (job.status === 'done') {
      toast.success(job.skipped > 0 ? t('jobFinishedSkippedToast', { kind, count: job.skipped }) : t('jobFinishedToast', { kind }))
    } else if (job.status === 'cancelled') {
      toast.warning(t('jobCancelledToast', { kind }))
    } else {
      toast.error(t('jobFailedToast', { kind, message: job.message }))
    }
    refresh()
  })
  const uploads = useUploads(roots.data?.max_upload_bytes, (dir) => {
    if (dir === pathRef.current) refresh()
  })

  useEffect(() => {
    setSelected(new Set())
    setLimit(PAGE_STEP)
    setMenu(null)
    anchor.current = null
  }, [path])

  // Drop selected paths that no longer exist after a reload.
  useEffect(() => {
    setSelected((prev) => {
      if (prev.size === 0) return prev
      const present = new Set(entries.map((e) => e.path))
      const next = new Set([...prev].filter((p) => present.has(p)))
      return next.size === prev.size ? prev : next
    })
  }, [entries])

  const go = useCallback(
    (next: string | null) => {
      setParams(next ? { path: next } : {})
    },
    [setParams],
  )

  const changeView = (v: View) => {
    setView(v)
    try {
      localStorage.setItem(VIEW_KEY, v)
    } catch {
      // The preference then lasts for this page load only.
    }
  }

  const changeSort = (key: SortKey) => {
    if (key === sort) setOrder(order === 'asc' ? 'desc' : 'asc')
    else {
      setSort(key)
      setOrder(key === 'name' ? 'asc' : 'desc')
    }
  }

  const textMax = roots.data?.text_max_bytes ?? 1 << 20

  const open = useCallback(
    (e: FileEntry) => {
      if (isDirLike(e)) {
        go(e.path)
        return
      }
      if (e.type === 'symlink' && e.link_kind === 'unreachable') {
        toast.warning(t('linkOutside'))
        return
      }
      if (e.type === 'other' || e.link_kind === 'other') {
        toast.warning(t('otherNotOpenable'))
        return
      }
      const kind = previewKind(e, textMax)
      if (kind === 'text') setDialog({ kind: 'text', entry: e })
      else if (kind) setDialog({ kind: 'media', entry: e, preview: kind })
      else startDownload([e.path])
    },
    [go, textMax],
  )

  const select = (e: FileEntry, ev: MouseEvent) => {
    if (ev.shiftKey && anchor.current) {
      const a = entries.findIndex((x) => x.path === anchor.current)
      const b = entries.findIndex((x) => x.path === e.path)
      if (a >= 0 && b >= 0) {
        const [from, to] = a < b ? [a, b] : [b, a]
        setSelected(new Set(entries.slice(from, to + 1).map((x) => x.path)))
        return
      }
    }
    anchor.current = e.path
    if (ev.ctrlKey || ev.metaKey) toggle(e)
    else setSelected(new Set([e.path]))
  }

  const toggle = (e: FileEntry) => {
    anchor.current = e.path
    setSelected((prev) => {
      const next = new Set(prev)
      if (!next.delete(e.path)) next.add(e.path)
      return next
    })
  }

  const chosen = useMemo(() => entries.filter((e) => selected.has(e.path)), [entries, selected])
  const allSelected = entries.length > 0 && chosen.length === entries.length

  const actionsFor = useCallback(
    (targets: FileEntry[]): MenuItem[] => {
      const items: MenuItem[] = []
      const single = targets.length === 1 ? targets[0] : undefined
      const add = (label: string, icon: LucideIcon, onSelect: () => void, extra: Partial<MenuItem> = {}) =>
        items.push({ label, icon, onSelect, ...extra })
      if (single) add(t('open'), FolderOpen, () => open(single))
      add(t('download'), Download, () => startDownload(targets.map((e) => e.path)))
      if (isAdmin) {
        add(t('move'), FolderInput, () => setDialog({ kind: 'move', entries: targets }), { separated: true })
        add(t('copy'), Copy, () => setDialog({ kind: 'copy', entries: targets }))
        if (single) add(t('rename'), Pencil, () => setDialog({ kind: 'rename', entry: single }))
        add(t('zip'), FileArchive, () => setDialog({ kind: 'zip', entries: targets }))
        if (single && isArchive(single)) add(t('unzip'), PackageOpen, () => setDialog({ kind: 'extract', entries: [single] }))
        add(t('remove'), Trash2, () => setDialog({ kind: 'delete', entries: targets }), { danger: true, separated: true })
      }
      if (single) add(t('properties'), Info, () => setDialog({ kind: 'properties', entry: single }), { separated: true })
      return items
    },
    [isAdmin, open],
  )

  const openMenu = (e: FileEntry, position: { x: number; y: number } | null) => {
    // Acting on an item outside the selection replaces the selection.
    const targets = selected.has(e.path) && chosen.length > 1 ? chosen : [e]
    if (targets.length === 1) setSelected(new Set([e.path]))
    setMenu({
      title: targets.length === 1 ? e.name : t('selectedCount', { count: targets.length }),
      items: actionsFor(targets),
      position,
    })
  }

  const onKeyDown = (ev: KeyboardEvent<HTMLUListElement>) => {
    const target = ev.target as HTMLElement
    if (target.tagName === 'INPUT' && ev.key !== 'Delete' && ev.key !== 'F2') return
    const row = target.closest<HTMLElement>('[data-path]')
    const focused = row ? entries.find((e) => e.path === row.dataset.path) : undefined
    const targets = focused && !selected.has(focused.path) ? [focused] : chosen.length > 0 ? chosen : focused ? [focused] : []

    switch (ev.key) {
      case 'Enter':
        if (focused) {
          ev.preventDefault()
          open(focused)
        }
        break
      case ' ':
        if (focused && target === row) {
          ev.preventDefault()
          toggle(focused)
        }
        break
      case 'Delete':
        if (isAdmin && targets.length > 0) {
          ev.preventDefault()
          setDialog({ kind: 'delete', entries: targets })
        }
        break
      case 'F2':
        if (isAdmin && focused) {
          ev.preventDefault()
          setDialog({ kind: 'rename', entry: focused })
        }
        break
      case 'Escape':
        setSelected(new Set())
        break
      case 'a':
      case 'A':
        if (ev.ctrlKey || ev.metaKey) {
          ev.preventDefault()
          setSelected(new Set(entries.map((e) => e.path)))
        }
        break
      case 'ArrowDown':
      case 'ArrowUp':
      case 'ArrowRight':
      case 'ArrowLeft': {
        if (!row) break
        if (view === 'list' && (ev.key === 'ArrowLeft' || ev.key === 'ArrowRight')) break
        ev.preventDefault()
        const rows = Array.from(listRef.current?.querySelectorAll<HTMLElement>('[data-path]') ?? [])
        const i = rows.indexOf(row)
        const forward = ev.key === 'ArrowDown' || ev.key === 'ArrowRight'
        rows[forward ? Math.min(rows.length - 1, i + 1) : Math.max(0, i - 1)]?.focus()
        break
      }
    }
  }

  /* ----- uploads ----- */

  const addFiles = (files: File[]) => {
    if (!path || files.length === 0) return
    uploads.add(files, path)
  }

  const onDrop = (ev: DragEvent) => {
    ev.preventDefault()
    dragDepth.current = 0
    setDragging(false)
    if (!isAdmin || !path) return
    const files: File[] = []
    let skipped = false
    const items = Array.from(ev.dataTransfer.items)
    if (items.length > 0) {
      for (const item of items) {
        if (item.kind !== 'file') continue
        if (item.webkitGetAsEntry?.()?.isDirectory) {
          skipped = true
          continue
        }
        const f = item.getAsFile()
        if (f) files.push(f)
      }
    } else {
      files.push(...Array.from(ev.dataTransfer.files))
    }
    if (skipped) toast.warning(t('folderUploadUnsupported'))
    addFiles(files)
  }

  const hasFiles = (ev: DragEvent) => Array.from(ev.dataTransfer.types).includes('Files')

  /* ----- render ----- */

  const rootList = roots.data?.roots ?? []
  const trail = data ? crumbs(data.path, data.root) : []
  const started = (job: Job) => {
    jobs.watch(job)
    setSelected(new Set())
  }

  const places = (
    <nav aria-label={t('places')} className="flex flex-col gap-1">
      {rootList.map((r) => (
        <PlaceButton key={r.path} root={r} active={data?.root === r.path} onOpen={go} />
      ))}
    </nav>
  )

  let content: ReactNode
  if (path === null) {
    if (roots.loading) content = <LoadingState />
    else if (roots.error) content = <ErrorState message={roots.error} onRetry={() => void roots.reload()} />
    else if (rootList.length === 0) content = <EmptyState icon={HardDrive} title={t('noRoots')} description={t('noRootsHint')} />
    else
      content = (
        <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
          {rootList.map((r) => (
            <div key={r.path} className="rounded-xl border border-line bg-surface">
              <PlaceButton root={r} active={false} onOpen={go} />
            </div>
          ))}
        </div>
      )
  } else if (list.error && !list.fetching) {
    content = <ErrorState message={list.error} onRetry={() => void list.reload()} />
  } else if (!data) {
    content = <LoadingState label={t('loadingDir')} />
  } else if (entries.length === 0) {
    content = (
      <EmptyState
        icon={FolderOpen}
        title={t('emptyDir')}
        description={isAdmin ? t('emptyDirHint') : t('emptyDirHintReadOnly')}
        action={
          isAdmin ? (
            <Button icon={Upload} variant="primary" onClick={() => fileInput.current?.click()}>
              {t('upload')}
            </Button>
          ) : undefined
        }
      />
    )
  } else {
    const entryProps = { onOpen: open, onSelect: select, onToggle: toggle, onMenu: openMenu }
    content = (
      <>
        {data.truncated && (
          <Alert tone="warning" className="mb-3">
            {t('truncated', { count: data.total.toLocaleString('tr-TR') })}
          </Alert>
        )}
        {view === 'list' ? (
          <>
            <div className="flex items-center gap-1 border-b border-line pr-1">
              <Checkbox
                checked={allSelected}
                label={t('selectAll')}
                onChange={() => setSelected(allSelected ? new Set() : new Set(entries.map((e) => e.path)))}
              />
              <SortHeader column="name" label={t('colName')} sort={sort} order={order} onSort={changeSort} className="flex-1 justify-start" />
              <SortHeader column="size" label={t('colSize')} sort={sort} order={order} onSort={changeSort} className="hidden w-24 justify-end sm:inline-flex" />
              <SortHeader column="mode" label={t('colMode')} sort={sort} order={order} onSort={changeSort} className="hidden w-28 pl-4 md:inline-flex" />
              <span className="hidden w-36 pl-2 text-[11px] font-medium uppercase tracking-wide text-faint xl:block">{t('colOwner')}</span>
              <SortHeader column="modified" label={t('colModified')} sort={sort} order={order} onSort={changeSort} className="hidden w-36 pl-2 sm:inline-flex" />
              <span className="size-10 shrink-0" aria-hidden />
            </div>
            <ul ref={listRef} onKeyDown={onKeyDown} aria-label={data.path} aria-multiselectable role="listbox">
              {entries.map((e) => (
                <EntryRow key={e.path} entry={e} selected={selected.has(e.path)} {...entryProps} />
              ))}
            </ul>
          </>
        ) : (
          <ul
            ref={listRef}
            onKeyDown={onKeyDown}
            aria-label={data.path}
            aria-multiselectable
            role="listbox"
            className="grid grid-cols-2 gap-2 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-6"
          >
            {entries.map((e) => (
              <EntryTile key={e.path} entry={e} selected={selected.has(e.path)} {...entryProps} />
            ))}
          </ul>
        )}
        <div className="mt-3 flex flex-wrap items-center justify-between gap-2 text-xs text-muted">
          <span>
            {entries.length < data.total
              ? t('showing', { shown: entries.length.toLocaleString('tr-TR'), total: data.total.toLocaleString('tr-TR') })
              : t('itemCount', { count: data.total.toLocaleString('tr-TR') })}
          </span>
          {entries.length < data.total &&
            (limit < PAGE_MAX ? (
              <Button size="sm" loading={list.fetching} onClick={() => setLimit(Math.min(PAGE_MAX, limit + PAGE_STEP))}>
                {t('showMore')}
              </Button>
            ) : (
              <span>{t('pageCap', { count: PAGE_MAX.toLocaleString('tr-TR') })}</span>
            ))}
        </div>
      </>
    )
  }

  const toolbarActions = chosen.length > 0 ? actionsFor(chosen).filter((a) => a.label !== t('open')) : []

  return (
    <div>
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        icon={FolderOpen}
        actions={
          path !== null &&
          isAdmin && (
            <>
              <Button icon={FolderPlus} onClick={() => setDialog({ kind: 'mkdir' })} disabled={!data}>
                {t('newFolder')}
              </Button>
              <Button icon={Upload} variant="primary" onClick={() => fileInput.current?.click()} disabled={!data}>
                {t('upload')}
              </Button>
            </>
          )
        }
      />
      <input
        ref={fileInput}
        type="file"
        multiple
        hidden
        aria-hidden
        tabIndex={-1}
        onChange={(e) => {
          addFiles(Array.from(e.target.files ?? []))
          e.target.value = ''
        }}
      />

      {!isAdmin && (
        <Alert tone="accent" className="mb-4">
          {t('readOnlyNote')}
        </Alert>
      )}
      <UploadsPanel handle={uploads} />
      <JobsPanel handle={jobs} />

      <div className="flex flex-col gap-4 lg:flex-row lg:items-start">
        {path !== null && rootList.length > 0 && (
          <Card className="hidden w-64 shrink-0 lg:block" padded={false}>
            <div className="p-3">
              <h2 className="px-2 pb-2 text-[11px] font-medium uppercase tracking-wide text-faint">{t('places')}</h2>
              {places}
            </div>
          </Card>
        )}

        <Card
          className="relative min-w-0 flex-1"
          onDragEnter={(ev) => {
            if (!isAdmin || !path || !hasFiles(ev)) return
            ev.preventDefault()
            dragDepth.current++
            setDragging(true)
          }}
          onDragOver={(ev) => {
            if (isAdmin && path && hasFiles(ev)) ev.preventDefault()
          }}
          onDragLeave={() => {
            dragDepth.current = Math.max(0, dragDepth.current - 1)
            if (dragDepth.current === 0) setDragging(false)
          }}
          onDrop={onDrop}
        >
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <nav aria-label={t('breadcrumb')} className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1 text-sm">
              {path !== null && data?.parent != null && (
                <IconButton icon={CornerLeftUp} label={t('up')} onClick={() => go(data.parent)} />
              )}
              <button
                type="button"
                onClick={() => go(null)}
                className={cx('min-h-10 rounded px-1 hover:text-accent', path === null ? 'font-medium text-fg' : 'text-muted')}
              >
                {t('places')}
              </button>
              {trail.map((c, i) => (
                <span key={c.path} className="inline-flex min-w-0 items-center gap-1">
                  <ChevronRight className="size-3.5 shrink-0 text-faint" aria-hidden />
                  <button
                    type="button"
                    onClick={() => go(c.path)}
                    aria-current={i === trail.length - 1 ? 'page' : undefined}
                    className={cx(
                      'min-h-10 max-w-40 truncate rounded px-1 hover:text-accent sm:max-w-60',
                      i === trail.length - 1 ? 'font-medium text-fg' : 'text-muted',
                      i === 0 && 'font-mono',
                    )}
                  >
                    {c.label}
                  </button>
                </span>
              ))}
            </nav>
            {path !== null && (
              <div className="flex items-center gap-1">
                <div className="w-40 sm:hidden">
                  <Select
                    aria-label={t('sortBy')}
                    value={`${sort}:${order}`}
                    onChange={(e) => {
                      const [k, o] = e.target.value.split(':')
                      setSort(k as SortKey)
                      setOrder(o as SortOrder)
                    }}
                  >
                    {(['name', 'size', 'modified'] as const).flatMap((k) =>
                      (['asc', 'desc'] as const).map((o) => (
                        <option key={`${k}:${o}`} value={`${k}:${o}`}>
                          {`${k === 'name' ? t('colName') : k === 'size' ? t('colSize') : t('colModified')} (${o === 'asc' ? t('sortAsc') : t('sortDesc')})`}
                        </option>
                      )),
                    )}
                  </Select>
                </div>
                <IconButton icon={List} label={t('listView')} tone={view === 'list' ? 'accent' : 'neutral'} aria-pressed={view === 'list'} onClick={() => changeView('list')} />
                <IconButton icon={LayoutGrid} label={t('gridView')} tone={view === 'grid' ? 'accent' : 'neutral'} aria-pressed={view === 'grid'} onClick={() => changeView('grid')} />
                <IconButton icon={RefreshCw} label={t('refresh')} loading={list.fetching} onClick={refresh} />
              </div>
            )}
          </div>

          {chosen.length > 0 && (
            <div className="sticky top-0 z-10 mb-3 flex flex-wrap items-center gap-2 rounded-xl border border-accent/25 bg-card px-2 py-1.5 shadow-card">
              <IconButton icon={X} label={t('clearSelection')} onClick={() => setSelected(new Set())} />
              <span className="mr-auto text-sm font-medium text-fg">{t('selectedCount', { count: chosen.length })}</span>
              <div className="hidden flex-wrap items-center gap-1 sm:flex">
                {toolbarActions.map((a) => (
                  <Button key={a.label} size="sm" variant={a.danger ? 'danger' : 'secondary'} icon={a.icon} onClick={a.onSelect}>
                    {a.label}
                  </Button>
                ))}
              </div>
              <Button
                className="sm:hidden"
                icon={MoreVertical}
                onClick={() => setMenu({ title: t('selectedCount', { count: chosen.length }), items: actionsFor(chosen), position: null })}
              >
                {t('actions')}
              </Button>
            </div>
          )}

          {content}

          {dragging && (
            <div className="pointer-events-none absolute inset-0 z-20 flex flex-col items-center justify-center gap-3 rounded-2xl border-2 border-dashed border-accent bg-card/90 text-accent">
              <UploadCloud className="size-10" aria-hidden />
              <p className="text-sm font-medium">{t('dropHere')}</p>
            </div>
          )}
        </Card>
      </div>

      <ActionMenu menu={menu} onClose={closeMenu} />

      <NameDialog
        open={dialog?.kind === 'mkdir'}
        onClose={() => setDialog(null)}
        title={t('newFolderTitle')}
        description={path ? t('newFolderIn', { path }) : undefined}
        label={t('folderName')}
        initial=""
        confirmLabel={t('create')}
        onSubmit={async (name) => {
          await api.post<FileEntry>('/files/mkdir', { path, name })
          toast.success(t('folderCreated'))
          setDialog(null)
          refresh()
        }}
      />
      <NameDialog
        open={dialog?.kind === 'rename'}
        onClose={() => setDialog(null)}
        title={t('renameTitle')}
        label={t('newName')}
        initial={dialog?.kind === 'rename' ? dialog.entry.name : ''}
        confirmLabel={t('rename')}
        onSubmit={async (name) => {
          if (dialog?.kind !== 'rename') return
          await api.post<FileEntry>('/files/rename', { path: dialog.entry.path, name })
          toast.success(t('renamed'))
          setDialog(null)
          refresh()
        }}
      />
      <NameDialog
        open={dialog?.kind === 'zip'}
        onClose={() => setDialog(null)}
        title={t('zipTitle')}
        description={dialog?.kind === 'zip' && path ? t('zipIn', { count: dialog.entries.length, path }) : undefined}
        label={t('zipName')}
        initial={dialog?.kind === 'zip' ? zipName(dialog.entries) : ''}
        confirmLabel={t('create')}
        onSubmit={async (name) => {
          if (dialog?.kind !== 'zip' || !path) return
          const job = await api.post<Job>('/files/zip', { paths: dialog.entries.map((e) => e.path), dest: path, name })
          toast.info(t('zipStarted'))
          started(job)
          setDialog(null)
        }}
      />
      <DeleteDialog entries={dialog?.kind === 'delete' ? dialog.entries : null} onClose={() => setDialog(null)} onStarted={started} />
      <DestinationDialog
        mode={dialog?.kind === 'move' || dialog?.kind === 'copy' || dialog?.kind === 'extract' ? dialog.kind : null}
        entries={dialog?.kind === 'move' || dialog?.kind === 'copy' || dialog?.kind === 'extract' ? dialog.entries : []}
        currentPath={path}
        onClose={() => setDialog(null)}
        onStarted={started}
      />
      <PropertiesDialog
        entry={dialog?.kind === 'properties' ? dialog.entry : null}
        jobs={jobs}
        onClose={() => setDialog(null)}
        onChanged={refresh}
      />
      <MediaDialog
        entry={dialog?.kind === 'media' ? dialog.entry : null}
        kind={dialog?.kind === 'media' ? dialog.preview : null}
        onClose={() => setDialog(null)}
      />
      <TextDialog entry={dialog?.kind === 'text' ? dialog.entry : null} onClose={() => setDialog(null)} onSaved={refresh} />
    </div>
  )
}

function samePath(a: string, b: string): boolean {
  const norm = (p: string) => (p.length > 1 ? p.replace(/\/+$/, '') : p)
  return norm(a) === norm(b)
}

function zipName(entries: FileEntry[]): string {
  const first = entries[0]
  if (entries.length === 1 && first) return `${first.name.replace(/\.[^.]+$/, '') || first.name}.zip`
  return `${t('zipDefaultName')}.zip`
}
