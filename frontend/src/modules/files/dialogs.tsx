import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { Download } from 'lucide-react'
import {
  Alert,
  Button,
  ConfirmDialog,
  ErrorState,
  Field,
  Input,
  KeyValueList,
  LoadingState,
  Modal,
  Select,
  Spinner,
} from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { formatBytes, formatDateTime } from '@/lib/format'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { FolderPicker } from './FolderPicker'
import type { JobsHandle } from './jobs'
import { t } from './strings'
import type { ConflictPolicy, FileEntry, Job, StatResponse, TextFile, TreeTotals } from './types'
import { isDirLike, multiPath, nameError, parentOf, previewUrl, startDownload, typeLabel, type PreviewKind } from './util'

/* ---------- name dialog (new folder, rename, zip name) ---------- */

export function NameDialog({
  open,
  onClose,
  title,
  description,
  label,
  initial,
  confirmLabel,
  onSubmit,
}: {
  open: boolean
  onClose: () => void
  title: string
  description?: ReactNode
  label: string
  initial: string
  confirmLabel: string
  /** Throwing keeps the dialog open and shows the message. */
  onSubmit: (name: string) => Promise<void>
}) {
  const [value, setValue] = useState(initial)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const id = useId()
  const input = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!open) return
    setValue(initial)
    setError(null)
    // Select the base name so typing replaces it but keeps the extension.
    const timer = window.setTimeout(() => {
      const el = input.current
      if (!el) return
      el.focus()
      const dot = initial.lastIndexOf('.')
      el.setSelectionRange(0, dot > 0 ? dot : initial.length)
    }, 0)
    return () => window.clearTimeout(timer)
  }, [open, initial])

  const submit = async () => {
    const problem = nameError(value)
    if (problem) {
      setError(problem)
      return
    }
    setBusy(true)
    try {
      await onSubmit(value.trim())
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      description={description}
      size="sm"
      busy={busy}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t('cancel')}
          </Button>
          <Button variant="primary" onClick={() => void submit()} loading={busy}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <Field label={label} htmlFor={id} error={error}>
          <Input
            ref={input}
            id={id}
            data-autofocus
            value={value}
            onChange={(e) => {
              setValue(e.target.value)
              setError(null)
            }}
            invalid={Boolean(error)}
            maxLength={255}
            autoComplete="off"
            spellCheck={false}
          />
        </Field>
      </form>
    </Modal>
  )
}

/* ---------- delete ---------- */

export function DeleteDialog({
  entries,
  onClose,
  onStarted,
}: {
  entries: FileEntry[] | null
  onClose: () => void
  onStarted: (job: Job) => void
}) {
  const open = entries !== null && entries.length > 0
  const paths = entries?.map((e) => e.path) ?? []
  const count = useQuery<TreeTotals>(open ? multiPath('/files/count', paths) : null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => setError(null), [open])

  if (!open || !entries) return null
  const first = entries[0]
  const totals = count.data
  // Anything beyond a single item must be confirmed by typing.
  const many = entries.length > 1 || entries.some(isDirLike) || (totals ? totals.items > 1 : false)

  let detail: ReactNode
  if (count.loading) {
    detail = (
      <span className="inline-flex items-center gap-2 text-xs text-muted">
        <Spinner className="size-3.5" label={t('deleteCounting')} />
        {t('deleteCounting')}
      </span>
    )
  } else if (count.error) {
    detail = <span className="text-xs text-warning">{t('deleteCountFailed', { message: count.error })}</span>
  } else if (totals) {
    detail = (
      <span className="text-xs text-muted">
        {t(totals.truncated ? 'deleteTotalsTruncated' : 'deleteTotals', {
          items: totals.items.toLocaleString('tr-TR'),
          size: formatBytes(totals.bytes),
        })}
      </span>
    )
  }

  return (
    <ConfirmDialog
      open
      danger
      onClose={onClose}
      title={t('deleteTitle')}
      confirmLabel={t('remove')}
      cancelLabel={t('cancel')}
      requireText={many ? t('deleteConfirmWord') : undefined}
      warning={t('deleteWarning')}
      error={error}
      message={
        <div className="flex flex-col gap-1.5">
          <p className="break-words">
            {entries.length === 1 && first ? t('deleteOne', { name: first.name }) : t('deleteMany', { count: entries.length })}
          </p>
          {detail}
        </div>
      }
      onConfirm={async () => {
        try {
          const job = await api.post<Job>('/files/delete', { paths })
          toast.info(t('deleteStarted'))
          onStarted(job)
          onClose()
        } catch (e) {
          setError(errorMessage(e))
        }
      }}
    />
  )
}

/* ---------- destination (move / copy / extract) ---------- */

export type DestinationMode = 'move' | 'copy' | 'extract'

export function DestinationDialog({
  mode,
  entries,
  currentPath,
  onClose,
  onStarted,
}: {
  mode: DestinationMode | null
  entries: FileEntry[]
  currentPath: string | null
  onClose: () => void
  onStarted: (job: Job) => void
}) {
  const [conflict, setConflict] = useState<ConflictPolicy>('rename')
  const id = useId()
  useEffect(() => {
    if (mode) setConflict('rename')
  }, [mode])

  const titles: Record<DestinationMode, string> = { move: t('moveTitle'), copy: t('copyTitle'), extract: t('extractTitle') }
  const confirms: Record<DestinationMode, string> = { move: t('moveHere'), copy: t('copyHere'), extract: t('extractHere') }
  const started: Record<DestinationMode, string> = { move: t('moveStarted'), copy: t('copyStarted'), extract: t('extractStarted') }

  return (
    <FolderPicker
      open={mode !== null && entries.length > 0}
      onClose={onClose}
      title={mode ? titles[mode] : undefined}
      confirmLabel={mode ? confirms[mode] : undefined}
      initialPath={currentPath ?? undefined}
      onSelect={async (dest) => {
        if (!mode) return
        const first = entries[0]
        const job =
          mode === 'extract'
            ? await api.post<Job>('/files/extract', { path: first?.path ?? '', dest, conflict })
            : await api.post<Job>(`/files/${mode}`, { paths: entries.map((e) => e.path), dest, conflict })
        toast.info(started[mode])
        onStarted(job)
        onClose()
      }}
    >
      <Field label={t('conflictLabel')} htmlFor={id}>
        <Select id={id} value={conflict} onChange={(e) => setConflict(e.target.value as ConflictPolicy)}>
          <option value="rename">{t('conflictRename')}</option>
          <option value="overwrite">{t('conflictOverwrite')}</option>
          <option value="skip">{t('conflictSkip')}</option>
        </Select>
      </Field>
      {mode === 'extract' && <Alert tone="accent">{t('extractSafety')}</Alert>}
    </FolderPicker>
  )
}

/* ---------- properties ---------- */

export function PropertiesDialog({
  entry,
  jobs,
  onClose,
  onChanged,
}: {
  entry: FileEntry | null
  jobs: JobsHandle
  onClose: () => void
  onChanged: () => void
}) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const open = entry !== null
  const stat = useQuery<StatResponse>(open ? '/files/stat' : null, { query: { path: entry?.path ?? '' } })
  const [sizeJob, setSizeJob] = useState<string | null>(null)
  const [sizeError, setSizeError] = useState<string | null>(null)
  const [mode, setMode] = useState('')
  const [modeError, setModeError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const modeId = useId()
  const path = entry?.path
  const dir = entry ? isDirLike(entry) : false
  const watch = jobs.watch

  useEffect(() => {
    setSizeJob(null)
    setSizeError(null)
    setModeError(null)
    setMode(entry?.mode_octal.replace(/^0(?=\d{3}$)/, '') ?? '')
    if (!path || !dir) return
    let alive = true
    api
      .post<Job>('/files/size', { path })
      .then((job) => {
        if (!alive) return
        setSizeJob(job.id)
        watch(job)
      })
      .catch((e: unknown) => alive && setSizeError(errorMessage(e)))
    return () => {
      alive = false
    }
    // Restart only when another item is opened.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, dir, watch])

  // Stop the calculation if the dialog is closed before it finishes.
  const cancel = jobs.cancel
  const job = sizeJob ? jobs.jobs.find((j) => j.id === sizeJob) : undefined
  const running = job?.status === 'running'
  const live = useRef<string | null>(null)
  live.current = running && sizeJob ? sizeJob : null
  useEffect(
    () => () => {
      if (live.current) cancel(live.current)
      live.current = null
    },
    [path, cancel],
  )

  if (!entry) return null
  const e = stat.data?.entry.path === entry.path ? stat.data.entry : entry

  let size: ReactNode = formatBytes(e.size)
  if (dir) {
    const totals = job?.result as TreeTotals | null | undefined
    if (sizeError) size = t('sizeFailed', { message: sizeError })
    else if (job?.status === 'done' && totals)
      size = t(totals.truncated ? 'sizeResultTruncated' : 'sizeResult', {
        size: formatBytes(totals.bytes),
        items: totals.items.toLocaleString('tr-TR'),
      })
    else if (job && job.status !== 'running') size = t('sizeFailed', { message: job.message })
    else
      size = (
        <span className="inline-flex items-center gap-2 text-muted">
          <Spinner className="size-3.5" />
          {t('sizeCalculating', { items: (job?.items_done ?? 0).toLocaleString('tr-TR') })}
        </span>
      )
  }

  const items: Array<{ label: string; value: ReactNode }> = [
    { label: t('propName'), value: e.name },
    { label: t('propPath'), value: <span className="font-mono text-xs">{parentOf(e.path)}</span> },
    { label: t('propType'), value: typeLabel(e) },
  ]
  if (e.type === 'symlink') {
    items.push({
      label: t('propTarget'),
      value: (
        <span className="font-mono text-xs">
          {e.link_target ?? t('propUnknown')}
          {e.link_kind === 'unreachable' && <span className="ml-2 font-sans text-warning">{t('linkUnreachable')}</span>}
        </span>
      ),
    })
  }
  items.push({ label: dir ? t('propContent') : t('propSize'), value: size })
  if (!dir && e.type !== 'other') items.push({ label: t('propMime'), value: e.mime || t('propUnknown') })
  items.push(
    { label: t('propMode'), value: <span className="font-mono text-xs">{`${e.mode} (${e.mode_octal})`}</span> },
    { label: t('propOwner'), value: e.owner ?? t('propUnknown') },
    { label: t('propGroup'), value: e.group ?? t('propUnknown') },
    { label: t('propModified'), value: formatDateTime(e.modified_at) },
  )

  const applyMode = async () => {
    if (!/^0?[0-7]{3}$/.test(mode.trim())) {
      setModeError(t('chmodInvalid'))
      return
    }
    setBusy(true)
    try {
      await api.post<FileEntry>('/files/chmod', { path: entry.path, mode: mode.trim() })
      toast.success(t('chmodDone'))
      await stat.reload()
      onChanged()
    } catch (err) {
      setModeError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  const canChmod = isAdmin && (e.type === 'file' || e.type === 'directory')

  return (
    <Modal
      open
      onClose={onClose}
      title={t('propsTitle')}
      size="md"
      footer={
        <Button variant="ghost" onClick={onClose}>
          {t('close')}
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        {stat.error && <Alert tone="danger">{stat.error}</Alert>}
        <KeyValueList items={items} />
        {canChmod && (
          <form
            className="border-t border-line pt-4"
            onSubmit={(ev) => {
              ev.preventDefault()
              void applyMode()
            }}
          >
            <Field label={t('chmodLabel')} htmlFor={modeId} hint={t('chmodHint')} error={modeError}>
              <div className="flex gap-2">
                <Input
                  id={modeId}
                  value={mode}
                  onChange={(ev) => {
                    setMode(ev.target.value)
                    setModeError(null)
                  }}
                  invalid={Boolean(modeError)}
                  inputMode="numeric"
                  maxLength={4}
                  className="font-mono"
                  autoComplete="off"
                />
                <Button type="submit" loading={busy}>
                  {t('chmodApply')}
                </Button>
              </div>
            </Field>
          </form>
        )}
      </div>
    </Modal>
  )
}

/* ---------- media preview ---------- */

export function MediaDialog({ entry, kind, onClose }: { entry: FileEntry | null; kind: PreviewKind | null; onClose: () => void }) {
  const [failed, setFailed] = useState(false)
  useEffect(() => setFailed(false), [entry?.path])
  if (!entry || !kind || kind === 'text') return null
  const src = previewUrl(entry.path)
  return (
    <Modal
      open
      onClose={onClose}
      title={entry.name}
      description={`${formatBytes(entry.size)} · ${formatDateTime(entry.modified_at)}`}
      size="xl"
      footer={
        <>
          <Button icon={Download} onClick={() => startDownload([entry.path])}>
            {t('download')}
          </Button>
          <Button variant="ghost" onClick={onClose}>
            {t('close')}
          </Button>
        </>
      }
    >
      <div className="flex min-h-40 items-center justify-center">
        {failed ? (
          <ErrorState message={t('previewUnavailable')} />
        ) : kind === 'image' ? (
          <img src={src} alt={entry.name} onError={() => setFailed(true)} className="max-h-[70dvh] max-w-full rounded-lg object-contain" />
        ) : kind === 'video' ? (
          <video src={src} controls preload="metadata" onError={() => setFailed(true)} className="max-h-[70dvh] w-full rounded-lg bg-bg" />
        ) : (
          <audio src={src} controls preload="metadata" onError={() => setFailed(true)} className="w-full" />
        )}
      </div>
    </Modal>
  )
}

/* ---------- text viewer / editor ---------- */

export function TextDialog({ entry, onClose, onSaved }: { entry: FileEntry | null; onClose: () => void; onSaved: () => void }) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const open = entry !== null
  const file = useQuery<TextFile>(open ? '/files/text' : null, { query: { path: entry?.path ?? '' } })
  const [text, setText] = useState('')
  const [base, setBase] = useState<TextFile | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmClose, setConfirmClose] = useState(false)
  const id = useId()

  useEffect(() => {
    setBase(null)
    setText('')
    setError(null)
    setConfirmClose(false)
  }, [entry?.path])

  useEffect(() => {
    if (file.data && entry && file.data.path === entry.path && !file.fetching) {
      setBase(file.data)
      setText(file.data.content)
    }
  }, [file.data, file.fetching, entry])

  if (!entry) return null
  const dirty = base !== null && text !== base.content

  const close = () => {
    if (dirty) setConfirmClose(true)
    else onClose()
  }

  const save = async () => {
    if (!base) return
    setBusy(true)
    setError(null)
    try {
      const saved = await api.put<FileEntry>('/files/text', { path: entry.path, content: text, modified_at: base.modified_at })
      setBase({ ...base, content: text, modified_at: saved.modified_at, size: saved.size })
      toast.success(t('textSaved'))
      onSaved()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Modal
        open={!confirmClose}
        onClose={close}
        title={entry.name}
        description={isAdmin ? t('textTitleEdit') : t('textTitleView')}
        size="xl"
        busy={busy}
        footer={
          <>
            <Button icon={Download} onClick={() => startDownload([entry.path])}>
              {t('download')}
            </Button>
            <Button variant="ghost" onClick={close} disabled={busy}>
              {t('close')}
            </Button>
            {isAdmin && (
              <Button variant="primary" onClick={() => void save()} loading={busy} disabled={!dirty}>
                {t('save')}
              </Button>
            )}
          </>
        }
      >
        {file.error && !base ? (
          <ErrorState message={file.error} onRetry={() => void file.reload()} />
        ) : !base ? (
          <LoadingState />
        ) : (
          <div className="flex flex-col gap-3">
            <label htmlFor={id} className="sr-only">
              {t('textContent')}
            </label>
            <textarea
              id={id}
              value={text}
              onChange={(e) => setText(e.target.value)}
              readOnly={!isAdmin}
              spellCheck={false}
              wrap="off"
              className="h-[60dvh] w-full resize-none rounded-xl border border-line bg-surface p-3 font-mono text-xs leading-5 text-fg focus:border-accent focus:outline-none"
            />
            {error && <Alert tone="danger">{error}</Alert>}
          </div>
        )}
      </Modal>
      <ConfirmDialog
        open={confirmClose}
        onClose={() => setConfirmClose(false)}
        onConfirm={onClose}
        title={t('textUnsavedTitle')}
        message={t('textUnsaved')}
        confirmLabel={t('textDiscard')}
        cancelLabel={t('cancel')}
        danger
      />
    </>
  )
}
