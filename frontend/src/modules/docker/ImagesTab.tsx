import { useEffect, useId, useMemo, useState } from 'react'
import { Download, Eraser, Layers, Trash2 } from 'lucide-react'
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  Field,
  IconButton,
  Input,
  Modal,
  ProgressBar,
  Switch,
  TableWrap,
  tableClass,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { useEventSource } from '@/hooks/useStream'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { cx, formatBytes, formatPercent, formatRelative } from '@/lib/format'
import { t } from './strings'
import { layerStatusText, matches, validImageRef } from './util'
import { useReloadOn, type DockerLive } from './live'
import { ListBody, ListToolbar, SearchBox, UsedBy } from './parts'
import type { DockerImage, PullState } from './types'

const PULL_EVENTS = ['progress', 'done']

function isPullState(v: unknown): v is PullState {
  return typeof v === 'object' && v !== null && Array.isArray((v as PullState).layers)
}

function imageName(im: DockerImage): string {
  return im.tags[0] ?? im.short_id
}

function PullModal({ onClose, onPulled }: { onClose: () => void; onPulled: () => void }) {
  const [ref, setRef] = useState('')
  const [invalid, setInvalid] = useState(false)
  const [job, setJob] = useState<string | null>(null)
  const [state, setState] = useState<PullState | null>(null)
  const [lost, setLost] = useState(false)
  const inputId = useId()

  const start = useAction((image: string) => api.post<{ job_id: string; image: string }>('/docker/images/pull', { image }), {
    onSuccess: (res) => {
      setState(null)
      setLost(false)
      setJob(res.job_id)
    },
  })

  const done = state?.done === true
  const stream = useEventSource(
    job ? `/docker/images/pull/${job}/stream` : null,
    (_event, data) => {
      if (!isPullState(data)) return
      setState(data)
      if (data.done) {
        if (data.success) {
          toast.success(data.message)
          onPulled()
        } else {
          toast.error(data.message)
        }
      }
    },
    { events: PULL_EVENTS, enabled: job !== null && !done && !lost },
  )

  useEffect(() => {
    if (job && !done && stream === 'closed' && !document.hidden) setLost(true)
  }, [job, done, stream])

  const running = job !== null && !done && !lost

  const submit = () => {
    const image = ref.trim()
    if (!validImageRef(image)) {
      setInvalid(true)
      return
    }
    setInvalid(false)
    void start.run(image)
  }

  const again = () => {
    setJob(null)
    setState(null)
    setLost(false)
    setRef('')
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={t('pullTitle')}
      description={running ? t('pullBackground') : undefined}
      size="md"
      footer={
        job === null ? (
          <>
            <Button variant="ghost" onClick={onClose}>
              {t('cancel')}
            </Button>
            <Button variant="primary" icon={Download} onClick={submit} loading={start.pending}>
              {t('pullStart')}
            </Button>
          </>
        ) : (
          <>
            {!running && (
              <Button icon={Download} onClick={again}>
                {t('pullAnother')}
              </Button>
            )}
            <Button variant="ghost" onClick={onClose}>
              {t('close')}
            </Button>
          </>
        )
      }
    >
      {job === null ? (
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
        >
          <Field label={t('pullLabel')} htmlFor={inputId} hint={t('pullHint')} error={invalid ? t('pullInvalid') : null}>
            <Input
              id={inputId}
              value={ref}
              onChange={(e) => {
                setRef(e.target.value)
                setInvalid(false)
              }}
              placeholder={t('pullPlaceholder')}
              invalid={invalid}
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              data-autofocus
            />
          </Field>
          {start.error && <Alert tone="danger">{start.error}</Alert>}
        </form>
      ) : (
        <div className="flex flex-col gap-4">
          <div>
            <div className="mb-1.5 flex items-center justify-between gap-3 text-sm">
              <span className="min-w-0 truncate font-mono text-xs text-fg">{state?.image ?? ref.trim()}</span>
              <span className="shrink-0 text-xs text-muted">{formatPercent(state?.percent ?? 0)}</span>
            </div>
            <ProgressBar
              value={state?.percent ?? 0}
              tone={done ? (state?.success ? 'success' : 'danger') : 'accent'}
              label={t('pullRunning')}
              className="h-2"
            />
          </div>
          {done && state && <Alert tone={state.success ? 'success' : 'danger'}>{state.message}</Alert>}
          {lost && <Alert tone="warning">{t('pullLost')}</Alert>}
          {state && state.layers.length > 0 && (
            <div>
              <h3 className="mb-2 text-xs font-semibold tracking-wide text-faint uppercase">{t('pullLayers')}</h3>
              <ul className="flex max-h-64 flex-col gap-1.5 overflow-y-auto pr-1">
                {state.layers.map((l) => (
                  <li key={l.id} className="flex items-center gap-3 text-xs">
                    <span className="w-24 shrink-0 font-mono text-faint">{l.id}</span>
                    <span className="min-w-0 flex-1 truncate text-muted">{layerStatusText(l.status)}</span>
                    {l.total > 0 && (l.status === 'Downloading' || l.status === 'Extracting') && (
                      <span className="shrink-0 text-faint">
                        {formatBytes(l.current)} / {formatBytes(l.total)}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      )}
    </Modal>
  )
}

function RemoveImageDialog({ image, onClose, onDone }: { image: DockerImage; onClose: () => void; onDone: () => void }) {
  const [force, setForce] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const multi = image.tags.length > 1
  return (
    <ConfirmDialog
      open
      danger
      onClose={onClose}
      title={t('removeImageTitle')}
      confirmLabel={t('actRemove')}
      cancelLabel={t('cancel')}
      error={error}
      message={
        <div className="flex flex-col gap-3">
          <p>{t('removeImageMessage', { name: imageName(image) })}</p>
          {multi && (
            <div className="flex items-center justify-between gap-3 text-xs text-muted">
              <span>{t('removeImageForce')}</span>
              <Switch checked={force} onChange={setForce} label={t('removeImageForce')} />
            </div>
          )}
        </div>
      }
      onConfirm={async () => {
        setError(null)
        try {
          await api.del(`/docker/images/${encodeURIComponent(image.id)}`, undefined, { query: { force } })
          toast.success(t('removeImageDone'))
          onDone()
        } catch (e) {
          setError(errorMessage(e))
        }
      }}
    />
  )
}

export function ImagesTab({ live }: { live: DockerLive }) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const q = useQuery<DockerImage[]>('/docker/images')
  useReloadOn(live.revision.image + live.revision.container, q.reload)

  const [search, setSearch] = useState('')
  const [pulling, setPulling] = useState(false)
  const [pruning, setPruning] = useState(false)
  const [pruneError, setPruneError] = useState<string | null>(null)
  const [removing, setRemoving] = useState<DockerImage | null>(null)

  const all = useMemo(() => q.data ?? [], [q.data])
  const shown = useMemo(() => all.filter((im) => matches(search, im.short_id, ...im.tags)), [all, search])
  const dangling = useMemo(() => all.filter((im) => im.dangling && !im.in_use).length, [all])

  const usage = (im: DockerImage) =>
    im.in_use ? <Badge tone="success">{t('inUse')}</Badge> : <Badge>{t('unused')}</Badge>

  const tags = (im: DockerImage) =>
    im.tags.length === 0 ? (
      <Badge tone="warning">{t('untagged')}</Badge>
    ) : (
      <span className="flex flex-col gap-0.5">
        {im.tags.map((tag) => (
          <span key={tag} className="font-mono text-xs break-all">
            {tag}
          </span>
        ))}
      </span>
    )

  const removeButton = (im: DockerImage) => (
    <IconButton
      icon={Trash2}
      tone="danger"
      label={im.in_use ? t('removeImageInUse', { names: im.containers.join(', ') }) : t('actRemove')}
      disabled={im.in_use}
      onClick={() => setRemoving(im)}
    />
  )

  return (
    <Card padded={false}>
      <ListToolbar
        actions={
          isAdmin && (
            <>
              <Button
                icon={Eraser}
                onClick={() => {
                  setPruneError(null)
                  setPruning(true)
                }}
                disabled={dangling === 0}
                title={dangling === 0 ? t('pruneImagesNone') : undefined}
              >
                {t('pruneImages')}
              </Button>
              <Button variant="primary" icon={Download} onClick={() => setPulling(true)}>
                {t('pullImage')}
              </Button>
            </>
          )
        }
      >
        <SearchBox value={search} onChange={setSearch} label={t('searchImages')} />
      </ListToolbar>

      <ListBody
        query={q}
        total={all.length}
        shown={shown.length}
        emptyIcon={Layers}
        emptyTitle={t('noImages')}
        emptyHint={t('noImagesHint')}
      >
        <div className="hidden px-5 pb-3 sm:block">
          <TableWrap>
            <table className={tableClass.table}>
              <thead>
                <tr>
                  <th className={tableClass.th}>{t('colTags')}</th>
                  <th className={tableClass.th}>{t('colId')}</th>
                  <th className={tableClass.th}>{t('colSize')}</th>
                  <th className={tableClass.th}>{t('colCreated')}</th>
                  <th className={tableClass.th}>{t('colUsage')}</th>
                  {isAdmin && <th className={cx(tableClass.th, 'text-right')}>{t('colActions')}</th>}
                </tr>
              </thead>
              <tbody>
                {shown.map((im) => (
                  <tr key={im.id} className={tableClass.row}>
                    <td className={tableClass.td}>{tags(im)}</td>
                    <td className={cx(tableClass.td, 'font-mono text-xs text-muted')}>{im.short_id}</td>
                    <td className={cx(tableClass.td, 'whitespace-nowrap')}>{formatBytes(im.size)}</td>
                    <td className={cx(tableClass.td, 'text-xs whitespace-nowrap text-muted')}>{formatRelative(im.created_at)}</td>
                    <td className={tableClass.td}>
                      <div className="flex flex-col gap-0.5">
                        <span>{usage(im)}</span>
                        {im.in_use && (
                          <span className="text-xs text-muted">
                            <UsedBy names={im.containers} />
                          </span>
                        )}
                      </div>
                    </td>
                    {isAdmin && <td className={cx(tableClass.td, 'text-right')}>{removeButton(im)}</td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </div>

        <ul className="flex flex-col gap-3 p-4 pt-0 sm:hidden">
          {shown.map((im) => (
            <li key={im.id} className="flex items-start justify-between gap-2 rounded-xl border border-line bg-surface p-3">
              <div className="flex min-w-0 flex-col gap-1.5">
                {tags(im)}
                <p className="text-xs text-muted">
                  <span className="font-mono">{im.short_id}</span> · {formatBytes(im.size)} · {formatRelative(im.created_at)}
                </p>
                <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted">
                  {usage(im)}
                  {im.in_use && <UsedBy names={im.containers} />}
                </div>
              </div>
              {isAdmin && removeButton(im)}
            </li>
          ))}
        </ul>
      </ListBody>

      {pulling && <PullModal onClose={() => setPulling(false)} onPulled={() => void q.reload()} />}
      {removing && (
        <RemoveImageDialog
          image={removing}
          onClose={() => setRemoving(null)}
          onDone={() => {
            setRemoving(null)
            void q.reload()
          }}
        />
      )}
      <ConfirmDialog
        open={pruning}
        danger
        onClose={() => setPruning(false)}
        title={t('pruneImagesTitle')}
        message={t('pruneImagesMessage', { n: dangling })}
        confirmLabel={t('pruneImages')}
        cancelLabel={t('cancel')}
        error={pruneError}
        onConfirm={async () => {
          setPruneError(null)
          try {
            const res = await api.post<{ deleted: number; space_reclaimed: number }>('/docker/images/prune')
            toast.success(t('pruneImagesDone', { size: formatBytes(res.space_reclaimed) }))
            setPruning(false)
            void q.reload()
          } catch (e) {
            setPruneError(errorMessage(e))
          }
        }}
      />
    </Card>
  )
}
