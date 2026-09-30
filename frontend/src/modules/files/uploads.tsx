import { useCallback, useEffect, useRef, useState } from 'react'
import { CheckCircle2, Clock, Loader2, X, XCircle } from 'lucide-react'
import { Button, Card, CardHeader, IconButton, ProgressBar } from '@/components/ui'
import { formatBytes } from '@/lib/format'
import { ApiError, api, errorMessage } from '@/services/api'
import { t } from './strings'
import type { UploadResult } from './types'

export type UploadStatus = 'queued' | 'uploading' | 'done' | 'error' | 'conflict' | 'cancelled'

export interface UploadItem {
  id: number
  file: File
  dir: string
  loaded: number
  status: UploadStatus
  error: string | null
  overwrite: boolean
}

export interface UploadsHandle {
  items: UploadItem[]
  add: (files: File[], dir: string) => void
  cancel: (id: number) => void
  overwrite: (id: number) => void
  clearFinished: () => void
}

const PARALLEL = 2
let nextId = 1

/** Upload queue: one request per file so each has its own progress, at most
 *  two at a time. `onUploaded` fires after each file lands on the server. */
export function useUploads(maxBytes: number | undefined, onUploaded: (dir: string) => void): UploadsHandle {
  const [items, setItems] = useState<UploadItem[]>([])
  const controllers = useRef(new Map<number, AbortController>())
  const uploaded = useRef(onUploaded)
  uploaded.current = onUploaded

  const patch = useCallback((id: number, change: Partial<UploadItem>) => {
    setItems((prev) => prev.map((it) => (it.id === id ? { ...it, ...change } : it)))
  }, [])

  const start = useCallback(
    (item: UploadItem) => {
      const ctrl = new AbortController()
      controllers.current.set(item.id, ctrl)
      const form = new FormData()
      form.append('file', item.file, item.file.name)
      api
        .upload<UploadResult[]>('/files/upload', form, {
          query: { path: item.dir, overwrite: item.overwrite ? 'true' : undefined },
          signal: ctrl.signal,
          onProgress: (loaded) => patch(item.id, { loaded: Math.min(loaded, item.file.size) }),
        })
        .then(() => {
          patch(item.id, { status: 'done', loaded: item.file.size })
          uploaded.current(item.dir)
        })
        .catch((e: unknown) => {
          if (e instanceof DOMException && e.name === 'AbortError') {
            patch(item.id, { status: 'cancelled' })
          } else if (e instanceof ApiError && e.status === 409 && !item.overwrite) {
            patch(item.id, { status: 'conflict', error: e.message })
          } else {
            patch(item.id, { status: 'error', error: errorMessage(e) })
          }
        })
        .finally(() => controllers.current.delete(item.id))
    },
    [patch],
  )

  // Promote queued items whenever a slot is free.
  useEffect(() => {
    const active = items.filter((it) => it.status === 'uploading').length
    const next = items.filter((it) => it.status === 'queued').slice(0, Math.max(0, PARALLEL - active))
    if (next.length === 0) return
    const ids = new Set(next.map((it) => it.id))
    setItems((prev) => prev.map((it) => (ids.has(it.id) ? { ...it, status: 'uploading' } : it)))
    for (const it of next) start(it)
  }, [items, start])

  // Abort whatever is still running when the page is left.
  useEffect(() => {
    const map = controllers.current
    return () => {
      for (const c of map.values()) c.abort()
    }
  }, [])

  const add = useCallback(
    (files: File[], dir: string) => {
      const fresh = files.map<UploadItem>((file) => {
        const tooLarge = maxBytes !== undefined && file.size > maxBytes
        return {
          id: nextId++,
          file,
          dir,
          loaded: 0,
          overwrite: false,
          status: tooLarge ? 'error' : 'queued',
          error: tooLarge ? t('uploadTooLarge', { max: formatBytes(maxBytes) }) : null,
        }
      })
      setItems((prev) => [...prev, ...fresh])
    },
    [maxBytes],
  )

  const cancel = useCallback(
    (id: number) => {
      const ctrl = controllers.current.get(id)
      if (ctrl) ctrl.abort()
      else setItems((prev) => prev.filter((it) => it.id !== id))
    },
    [],
  )

  const overwrite = useCallback(
    (id: number) => patch(id, { status: 'queued', overwrite: true, error: null, loaded: 0 }),
    [patch],
  )

  const clearFinished = useCallback(() => {
    setItems((prev) => prev.filter((it) => it.status === 'queued' || it.status === 'uploading'))
  }, [])

  return { items, add, cancel, overwrite, clearFinished }
}

function line(it: UploadItem): string {
  switch (it.status) {
    case 'queued':
      return t('uploadQueued')
    case 'uploading':
      return `${formatBytes(it.loaded)} / ${formatBytes(it.file.size)}`
    case 'done':
      return `${t('uploadDone')} · ${formatBytes(it.file.size)}`
    case 'cancelled':
      return t('uploadCancelled')
    case 'conflict':
      return it.error ?? t('uploadConflict')
    default:
      return it.error ?? ''
  }
}

export function UploadsPanel({ handle }: { handle: UploadsHandle }) {
  const { items } = handle
  if (items.length === 0) return null
  const busy = items.some((it) => it.status === 'queued' || it.status === 'uploading')
  return (
    <Card className="mb-4">
      <CardHeader
        title={t('uploadsTitle')}
        actions={
          <Button size="sm" variant="ghost" onClick={handle.clearFinished} disabled={items.every((it) => it.status === 'queued' || it.status === 'uploading')}>
            {t('uploadClear')}
          </Button>
        }
      />
      <ul className="flex max-h-72 flex-col gap-3 overflow-y-auto" aria-busy={busy}>
        {items.map((it) => {
          const failed = it.status === 'error' || it.status === 'conflict' || it.status === 'cancelled'
          return (
            <li key={it.id} className="flex items-start gap-3">
              <span className="mt-0.5 shrink-0">
                {it.status === 'queued' && <Clock className="size-4 text-faint" aria-hidden />}
                {it.status === 'uploading' && <Loader2 className="ms-spin size-4 text-accent" aria-hidden />}
                {it.status === 'done' && <CheckCircle2 className="size-4 text-success" aria-hidden />}
                {failed && <XCircle className={it.status === 'conflict' ? 'size-4 text-warning' : 'size-4 text-danger'} aria-hidden />}
              </span>
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm text-fg">{it.file.name}</p>
                <p className={failed ? 'break-words text-xs text-danger' : 'truncate text-xs text-muted'}>
                  {line(it)}
                  {!failed && <span className="text-faint"> · {t('uploadTo', { path: it.dir })}</span>}
                </p>
                {it.status === 'uploading' && (
                  <ProgressBar
                    value={it.file.size > 0 ? (it.loaded / it.file.size) * 100 : 0}
                    tone="accent"
                    label={it.file.name}
                    className="mt-1.5"
                  />
                )}
              </div>
              {it.status === 'conflict' && (
                <Button size="sm" onClick={() => handle.overwrite(it.id)}>
                  {t('uploadOverwrite')}
                </Button>
              )}
              {(it.status === 'queued' || it.status === 'uploading') && (
                <IconButton icon={X} label={t('uploadCancel', { name: it.file.name })} tone="danger" onClick={() => handle.cancel(it.id)} />
              )}
            </li>
          )
        })}
      </ul>
    </Card>
  )
}
