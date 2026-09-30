import { useEffect, useId, useState, type ReactNode } from 'react'
import { ChevronRight, CornerLeftUp, Folder, FolderPlus, FolderSymlink, HardDrive } from 'lucide-react'
import { Alert, Button, EmptyState, ErrorState, Input, LoadingState, Modal } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { cx } from '@/lib/format'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { t } from './strings'
import type { FileEntry, ListResponse, RootsResponse } from './types'
import { crumbs, nameError } from './util'

export interface FolderPickerProps {
  open: boolean
  onClose: () => void
  /** Called with the absolute path of the chosen directory. */
  onSelect: (path: string) => void | Promise<void>
  title?: string
  /** Directory shown when the dialog opens. */
  initialPath?: string
  /** Label of the confirm button. */
  confirmLabel?: string
  /** Extra controls shown above the footer (e.g. a conflict option). */
  children?: ReactNode
  /** Lets admins create a folder from inside the dialog (default true). */
  allowCreate?: boolean
}

const rowClass =
  'flex min-h-12 w-full items-center gap-3 rounded-xl px-3 text-left text-sm text-fg transition-colors hover:bg-raised disabled:opacity-50'

/** Dialog for choosing a directory inside the allowed roots. */
export function FolderPicker({
  open,
  onClose,
  onSelect,
  title,
  initialPath,
  confirmLabel,
  children,
  allowCreate = true,
}: FolderPickerProps) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const [path, setPath] = useState<string | null>(initialPath ?? null)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [createError, setCreateError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const nameId = useId()

  useEffect(() => {
    if (!open) return
    setPath(initialPath ?? null)
    setCreating(false)
    setNewName('')
    setCreateError(null)
    setError(null)
  }, [open, initialPath])

  const roots = useQuery<RootsResponse>('/files/roots', { enabled: open })
  const list = useQuery<ListResponse>(path ? '/files/list' : null, {
    enabled: open && path !== null,
    query: { path: path ?? '', dirs_only: true, limit: 5000 },
  })

  const go = (next: string | null) => {
    setPath(next)
    setCreating(false)
    setCreateError(null)
    setError(null)
  }

  const create = async () => {
    if (!path) return
    const problem = nameError(newName)
    if (problem) {
      setCreateError(problem)
      return
    }
    setBusy(true)
    try {
      const made = await api.post<FileEntry>('/files/mkdir', { path, name: newName.trim() })
      setNewName('')
      go(made.path)
    } catch (e) {
      setCreateError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  const confirm = async () => {
    if (!path) return
    setBusy(true)
    setError(null)
    try {
      await onSelect(path)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  const loaded = path !== null && list.data && (list.data.path === path || !list.fetching) ? list.data : undefined
  const trail = loaded ? crumbs(loaded.path, loaded.root) : []

  let body: ReactNode
  if (path === null) {
    if (roots.loading) body = <LoadingState />
    else if (roots.error) body = <ErrorState message={roots.error} onRetry={() => void roots.reload()} />
    else if (!roots.data || roots.data.roots.length === 0) body = <EmptyState icon={HardDrive} title={t('noRoots')} description={t('noRootsHint')} />
    else
      body = (
        <ul className="flex flex-col gap-1">
          {roots.data.roots.map((r) => (
            <li key={r.path}>
              <button type="button" className={rowClass} disabled={!r.exists} onClick={() => go(r.path)}>
                <HardDrive className="size-5 shrink-0 text-accent" aria-hidden />
                <span className="min-w-0 flex-1 truncate font-mono">{r.path}</span>
                {!r.exists && <span className="shrink-0 text-xs text-warning">{t('rootMissing')}</span>}
                <ChevronRight className="size-4 shrink-0 text-faint" aria-hidden />
              </button>
            </li>
          ))}
        </ul>
      )
  } else if (list.error && !list.fetching) {
    body = <ErrorState message={list.error} onRetry={() => void list.reload()} />
  } else if (!loaded) {
    body = <LoadingState label={t('loadingDir')} />
  } else {
    body = (
      <>
        <ul className="flex flex-col gap-1">
          <li>
            <button type="button" className={cx(rowClass, 'text-muted')} onClick={() => go(loaded.parent)}>
              <CornerLeftUp className="size-5 shrink-0" aria-hidden />
              <span>{loaded.parent ? t('up') : t('places')}</span>
            </button>
          </li>
          {loaded.entries.map((e) => (
            <li key={e.path}>
              <button
                type="button"
                className={rowClass}
                aria-label={t('pickerEnter', { name: e.name })}
                onClick={() => go(e.path)}
              >
                {e.type === 'symlink' ? (
                  <FolderSymlink className="size-5 shrink-0 text-cyan" aria-hidden />
                ) : (
                  <Folder className="size-5 shrink-0 text-accent" aria-hidden />
                )}
                <span className="min-w-0 flex-1 truncate">{e.name}</span>
                <ChevronRight className="size-4 shrink-0 text-faint" aria-hidden />
              </button>
            </li>
          ))}
        </ul>
        {loaded.entries.length === 0 && (
          <EmptyState icon={Folder} title={t('pickerNoFolders')} description={t('pickerNoFoldersHint')} className="py-6" />
        )}
      </>
    )
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title ?? t('picker')}
      size="md"
      busy={busy}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t('cancel')}
          </Button>
          <Button variant="primary" onClick={() => void confirm()} disabled={!loaded} loading={busy}>
            {confirmLabel ?? t('pickerSelect')}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        <div className="rounded-xl border border-line bg-surface px-3 py-2">
          <p className="text-[11px] font-medium uppercase tracking-wide text-faint">{t('pickerCurrent')}</p>
          {trail.length > 0 ? (
            <nav aria-label={t('breadcrumb')} className="mt-0.5 flex flex-wrap items-center gap-x-1 text-sm">
              {trail.map((c, i) => (
                <span key={c.path} className="inline-flex min-w-0 items-center gap-1">
                  {i > 0 && <ChevronRight className="size-3 shrink-0 text-faint" aria-hidden />}
                  <button
                    type="button"
                    onClick={() => go(c.path)}
                    className={cx('max-w-48 truncate rounded py-1 hover:text-accent', i === trail.length - 1 ? 'font-medium text-fg' : 'text-muted')}
                  >
                    {c.label}
                  </button>
                </span>
              ))}
            </nav>
          ) : (
            <p className="mt-0.5 text-sm text-muted">{path ?? t('pickerNone')}</p>
          )}
        </div>

        <div className="max-h-[45dvh] min-h-40 overflow-y-auto">{body}</div>

        {allowCreate && isAdmin && loaded && (
          <div>
            {creating ? (
              <form
                className="flex flex-col gap-1.5"
                onSubmit={(e) => {
                  e.preventDefault()
                  void create()
                }}
              >
                <label htmlFor={nameId} className="text-xs font-medium text-muted">
                  {t('folderName')}
                </label>
                <div className="flex gap-2">
                  <Input
                    id={nameId}
                    value={newName}
                    onChange={(e) => {
                      setNewName(e.target.value)
                      setCreateError(null)
                    }}
                    invalid={Boolean(createError)}
                    maxLength={255}
                    autoComplete="off"
                    spellCheck={false}
                    autoFocus
                  />
                  <Button type="submit" variant="primary" loading={busy}>
                    {t('create')}
                  </Button>
                </div>
                {createError && (
                  <p role="alert" className="text-xs text-danger">
                    {createError}
                  </p>
                )}
              </form>
            ) : (
              <Button icon={FolderPlus} onClick={() => setCreating(true)}>
                {t('newFolder')}
              </Button>
            )}
          </div>
        )}

        {children}
        {error && <Alert tone="danger">{error}</Alert>}
      </div>
    </Modal>
  )
}
