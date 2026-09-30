import { useEffect, useId, useMemo, useState } from 'react'
import { FolderCog, HardDrive, Plus, Trash2 } from 'lucide-react'
import { Alert, Button, Card, CardHeader, ErrorState, Field, IconButton, Input, LoadingState } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { formatBytes } from '@/lib/format'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { t } from './strings'

const KEY_ROOTS = 'files.allowed_roots'
const KEY_UPLOAD = 'files.max_upload_mb'
const KEY_EXTRACT = 'files.max_extract_mb'
const MAX_ROOTS = 32
const UPLOAD_RANGE = { min: 1, max: 1048576 }
const EXTRACT_RANGE = { min: 1, max: 4194304 }

type Settings = Record<string, string>

function parseRoots(value: string | undefined): string[] {
  try {
    const parsed: unknown = JSON.parse(value ?? '[]')
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === 'string') : []
  } catch {
    return []
  }
}

function normalizeRoot(input: string): string | null {
  const v = input.trim()
  if (!v.startsWith('/') || v.includes('\u0000') || v.split('/').includes('..')) return null
  const cleaned = '/' + v.split('/').filter((s) => s && s !== '.').join('/')
  return cleaned === '/' ? null : cleaned
}

function inRange(value: string, range: { min: number; max: number }): boolean {
  if (!/^\d+$/.test(value.trim())) return false
  const n = Number(value)
  return n >= range.min && n <= range.max
}

function sizeOf(mb: string): string {
  return /^\d+$/.test(mb.trim()) ? formatBytes(Number(mb) * 1024 * 1024) : '—'
}

/** Settings section: allowed roots and size limits of the file manager. */
export function FilesSettingsSection() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const settings = useQuery<Settings>('/settings', { enabled: isAdmin })
  const [roots, setRoots] = useState<string[]>([])
  const [upload, setUpload] = useState('')
  const [extract, setExtract] = useState('')
  const [draft, setDraft] = useState('')
  const [draftError, setDraftError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const draftId = useId()
  const uploadId = useId()
  const extractId = useId()

  const saved = useMemo(
    () => ({
      roots: parseRoots(settings.data?.[KEY_ROOTS]),
      upload: settings.data?.[KEY_UPLOAD] ?? '',
      extract: settings.data?.[KEY_EXTRACT] ?? '',
    }),
    [settings.data],
  )

  useEffect(() => {
    setRoots(saved.roots)
    setUpload(saved.upload)
    setExtract(saved.extract)
    setError(null)
  }, [saved])

  if (!isAdmin) return null

  const dirty = JSON.stringify(roots) !== JSON.stringify(saved.roots) || upload.trim() !== saved.upload || extract.trim() !== saved.extract
  const uploadOk = inRange(upload, UPLOAD_RANGE)
  const extractOk = inRange(extract, EXTRACT_RANGE)

  const addRoot = () => {
    const path = normalizeRoot(draft)
    if (!path) return setDraftError(t('rootInvalid'))
    if (roots.includes(path)) return setDraftError(t('rootDuplicate'))
    if (roots.length >= MAX_ROOTS) return setDraftError(t('rootLimit'))
    setRoots([...roots, path])
    setDraft('')
    setDraftError(null)
  }

  const save = async () => {
    setSaving(true)
    setError(null)
    try {
      const body: Settings = {}
      if (JSON.stringify(roots) !== JSON.stringify(saved.roots)) body[KEY_ROOTS] = JSON.stringify(roots)
      if (upload.trim() !== saved.upload) body[KEY_UPLOAD] = upload.trim()
      if (extract.trim() !== saved.extract) body[KEY_EXTRACT] = extract.trim()
      await api.put<Settings>('/settings', body)
      toast.success(t('settingsSaved'))
      await settings.reload()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader title={t('settingsTitle')} subtitle={t('settingsSubtitle')} icon={FolderCog} />
      {settings.loading ? (
        <LoadingState />
      ) : settings.error && !settings.data ? (
        <ErrorState message={settings.error} onRetry={() => void settings.reload()} />
      ) : (
        <div className="flex flex-col gap-5">
          <section className="flex flex-col gap-3">
            <div>
              <h3 className="text-sm font-medium text-fg">{t('rootsLabel')}</h3>
              <p className="mt-1 text-xs text-muted">{t('rootsExplain')}</p>
            </div>
            <Alert tone="warning">{t('rootsWarning')}</Alert>
            {roots.length === 0 ? (
              <Alert tone="danger">{t('rootsEmpty')}</Alert>
            ) : (
              <ul className="flex flex-col gap-1.5">
                {roots.map((r) => (
                  <li key={r} className="flex min-h-12 items-center gap-3 rounded-xl border border-line bg-surface pl-3 pr-1">
                    <HardDrive className="size-4 shrink-0 text-muted" aria-hidden />
                    <span className="min-w-0 flex-1 truncate font-mono text-sm text-fg">{r}</span>
                    <IconButton
                      icon={Trash2}
                      tone="danger"
                      label={t('rootRemove', { path: r })}
                      onClick={() => setRoots(roots.filter((x) => x !== r))}
                    />
                  </li>
                ))}
              </ul>
            )}
            <form
              onSubmit={(e) => {
                e.preventDefault()
                addRoot()
              }}
            >
              <Field label={t('rootPath')} htmlFor={draftId} error={draftError}>
                <div className="flex gap-2">
                  <Input
                    id={draftId}
                    value={draft}
                    onChange={(e) => {
                      setDraft(e.target.value)
                      setDraftError(null)
                    }}
                    placeholder={t('rootPathPlaceholder')}
                    invalid={Boolean(draftError)}
                    className="font-mono"
                    autoComplete="off"
                    spellCheck={false}
                  />
                  <Button type="submit" icon={Plus} disabled={!draft.trim()}>
                    {t('rootAdd')}
                  </Button>
                </div>
              </Field>
            </form>
          </section>

          <section className="grid gap-4 border-t border-line pt-5 sm:grid-cols-2">
            <Field
              label={t('maxUpload')}
              htmlFor={uploadId}
              hint={t('maxUploadHint', { size: sizeOf(upload) })}
              error={uploadOk ? null : t('numberInvalid', UPLOAD_RANGE)}
            >
              <Input id={uploadId} value={upload} onChange={(e) => setUpload(e.target.value)} inputMode="numeric" invalid={!uploadOk} />
            </Field>
            <Field
              label={t('maxExtract')}
              htmlFor={extractId}
              hint={t('maxExtractHint', { size: sizeOf(extract) })}
              error={extractOk ? null : t('numberInvalid', EXTRACT_RANGE)}
            >
              <Input id={extractId} value={extract} onChange={(e) => setExtract(e.target.value)} inputMode="numeric" invalid={!extractOk} />
            </Field>
          </section>

          {error && <Alert tone="danger">{error}</Alert>}

          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-line pt-4">
            {dirty && <span className="mr-auto text-xs text-warning">{t('settingsUnsaved')}</span>}
            <Button
              variant="ghost"
              disabled={!dirty || saving}
              onClick={() => {
                setRoots(saved.roots)
                setUpload(saved.upload)
                setExtract(saved.extract)
                setError(null)
              }}
            >
              {t('settingsReset')}
            </Button>
            <Button variant="primary" onClick={() => void save()} loading={saving} disabled={!dirty || !uploadOk || !extractOk}>
              {t('save')}
            </Button>
          </div>
        </div>
      )}
    </Card>
  )
}
