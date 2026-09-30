import { useEffect, useId, useMemo, useState } from 'react'
import { Database, Eraser, Trash2 } from 'lucide-react'
import {
  Alert,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  IconButton,
  Input,
  LoadingState,
  Modal,
  TableWrap,
  tableClass,
} from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { cx, formatRelative } from '@/lib/format'
import { t } from './strings'
import { matches } from './util'
import { useReloadOn, type DockerLive } from './live'
import { ListBody, ListToolbar, SearchBox, UsedBy } from './parts'
import type { DockerVolume, VolumePruneResult } from './types'

function shortName(v: DockerVolume): string {
  return v.anonymous ? v.name.slice(0, 12) + '…' : v.name
}

/** Lists the unused volumes and deletes only the ones the user selected and
 *  confirmed by typing. */
function PruneModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const q = useQuery<DockerVolume[]>('/docker/volumes/unused')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [typed, setTyped] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [failed, setFailed] = useState<VolumePruneResult['failed']>([])
  const inputId = useId()
  const word = t('pruneVolumesWord')

  const list = useMemo(() => q.data ?? [], [q.data])
  // Nothing is preselected: every volume to delete is a deliberate choice.
  useEffect(() => {
    setSelected((prev) => new Set([...prev].filter((n) => list.some((v) => v.name === n))))
  }, [list])

  const toggle = (name: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(name)) next.delete(name)
      else next.add(name)
      return next
    })

  const allowed = selected.size > 0 && typed.trim().toLocaleLowerCase('tr-TR') === word && !pending

  const confirm = async () => {
    if (!allowed) return
    setPending(true)
    setError(null)
    setFailed([])
    try {
      const res = await api.post<VolumePruneResult>('/docker/volumes/prune', { names: [...selected], confirm: true })
      if (res.failed.length === 0) {
        toast.success(t('pruneVolumesDone', { n: res.removed.length }))
        onDone()
        return
      }
      toast.warning(t('pruneVolumesPartial', { ok: res.removed.length, fail: res.failed.length }))
      setFailed(res.failed)
      setTyped('')
      await q.reload()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={t('pruneVolumesTitle')}
      size="md"
      busy={pending}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={pending} data-autofocus>
            {t('cancel')}
          </Button>
          <Button variant="danger" icon={Trash2} onClick={() => void confirm()} loading={pending} disabled={!allowed}>
            {t('pruneVolumesConfirm')}
          </Button>
        </>
      }
    >
      {q.loading ? (
        <LoadingState />
      ) : q.error ? (
        <ErrorState message={q.error} onRetry={() => void q.reload()} />
      ) : list.length === 0 ? (
        <EmptyState icon={Database} title={t('pruneVolumesNone')} />
      ) : (
        <div className="flex flex-col gap-3 text-sm">
          <p className="text-fg">{t('pruneVolumesIntro')}</p>
          <Alert tone="danger">{t('pruneVolumesWarning')}</Alert>
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted">
            <span>{t('pruneVolumesSelected', { n: selected.size })}</span>
            <span className="flex gap-2">
              <Button size="sm" variant="ghost" onClick={() => setSelected(new Set(list.map((v) => v.name)))} disabled={pending}>
                {t('pruneVolumesSelectAll')}
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setSelected(new Set())} disabled={pending || selected.size === 0}>
                {t('pruneVolumesSelectNone')}
              </Button>
            </span>
          </div>
          <ul className="flex max-h-64 flex-col divide-y divide-line/60 overflow-y-auto rounded-xl border border-line bg-surface">
            {list.map((v) => (
              <li key={v.name}>
                <label className="flex min-h-11 cursor-pointer items-center gap-3 px-3 py-2">
                  <input
                    type="checkbox"
                    className="size-4 shrink-0 accent-(--ms-danger)"
                    checked={selected.has(v.name)}
                    onChange={() => toggle(v.name)}
                    disabled={pending}
                    aria-label={t('selectVolume', { name: v.name })}
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block font-mono text-xs break-all text-fg">{v.name}</span>
                    <span className="block text-xs text-faint">
                      {v.driver}
                      {v.created_at != null && ' · ' + formatRelative(v.created_at)}
                      {v.app && ' · ' + t('appBadge', { app: v.app })}
                    </span>
                  </span>
                </label>
              </li>
            ))}
          </ul>
          {failed.length > 0 && (
            <Alert tone="danger">
              <ul className="flex flex-col gap-0.5">
                {failed.map((f) => (
                  <li key={f.name}>
                    <span className="font-mono break-all">{f.name}</span>: {f.message}
                  </li>
                ))}
              </ul>
            </Alert>
          )}
          <div className="flex flex-col gap-1.5">
            <label htmlFor={inputId} className="text-xs text-muted">
              {t('pruneVolumesType', { word })}
            </label>
            <Input
              id={inputId}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              disabled={pending}
            />
          </div>
          {error && <Alert tone="danger">{error}</Alert>}
        </div>
      )}
    </Modal>
  )
}

export function VolumesTab({ live }: { live: DockerLive }) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const q = useQuery<DockerVolume[]>('/docker/volumes')
  useReloadOn(live.revision.volume + live.revision.container, q.reload)

  const [search, setSearch] = useState('')
  const [removing, setRemoving] = useState<DockerVolume | null>(null)
  const [removeError, setRemoveError] = useState<string | null>(null)
  const [pruning, setPruning] = useState(false)

  const all = useMemo(() => q.data ?? [], [q.data])
  const shown = useMemo(() => all.filter((v) => matches(search, v.name, v.driver, v.app, ...v.containers.map((c) => c.name))), [all, search])
  const unused = useMemo(() => all.filter((v) => !v.in_use).length, [all])

  const badges = (v: DockerVolume) => (
    <span className="flex flex-wrap gap-1">
      {v.in_use ? <Badge tone="success">{t('inUse')}</Badge> : <Badge>{t('unused')}</Badge>}
      {v.anonymous && <Badge tone="warning">{t('anonymous')}</Badge>}
      {v.app && <Badge tone="accent">{t('appBadge', { app: v.app })}</Badge>}
    </span>
  )

  const removeButton = (v: DockerVolume) => (
    <IconButton
      icon={Trash2}
      tone="danger"
      label={v.in_use ? t('removeVolumeInUse') : t('actRemove')}
      disabled={v.in_use}
      onClick={() => {
        setRemoveError(null)
        setRemoving(v)
      }}
    />
  )

  return (
    <Card padded={false}>
      <ListToolbar
        actions={
          isAdmin && (
            <Button icon={Eraser} variant="danger" onClick={() => setPruning(true)} disabled={unused === 0} title={unused === 0 ? t('pruneVolumesNone') : undefined}>
              {t('pruneVolumes')}
            </Button>
          )
        }
      >
        <SearchBox value={search} onChange={setSearch} label={t('searchVolumes')} />
      </ListToolbar>

      <ListBody
        query={q}
        total={all.length}
        shown={shown.length}
        emptyIcon={Database}
        emptyTitle={t('noVolumes')}
        emptyHint={t('noVolumesHint')}
      >
        <div className="hidden px-5 pb-3 md:block">
          <TableWrap>
            <table className={tableClass.table}>
              <thead>
                <tr>
                  <th className={tableClass.th}>{t('colName')}</th>
                  <th className={tableClass.th}>{t('colDriver')}</th>
                  <th className={tableClass.th}>{t('colMountpoint')}</th>
                  <th className={tableClass.th}>{t('colContainers')}</th>
                  <th className={tableClass.th}>{t('colCreated')}</th>
                  {isAdmin && <th className={cx(tableClass.th, 'text-right')}>{t('colActions')}</th>}
                </tr>
              </thead>
              <tbody>
                {shown.map((v) => (
                  <tr key={v.name} className={tableClass.row}>
                    <td className={tableClass.td}>
                      <div className="flex flex-col gap-1">
                        <span className="font-mono text-xs break-all" title={v.name}>
                          {shortName(v)}
                        </span>
                        {badges(v)}
                      </div>
                    </td>
                    <td className={cx(tableClass.td, 'text-muted')}>{v.driver}</td>
                    <td className={cx(tableClass.td, 'max-w-64 font-mono text-xs break-all text-muted')}>{v.mountpoint || t('none')}</td>
                    <td className={cx(tableClass.td, 'text-xs')}>
                      <UsedBy names={v.containers.map((c) => c.name)} />
                    </td>
                    <td className={cx(tableClass.td, 'text-xs whitespace-nowrap text-muted')}>{formatRelative(v.created_at)}</td>
                    {isAdmin && <td className={cx(tableClass.td, 'text-right')}>{removeButton(v)}</td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </div>

        <ul className="flex flex-col gap-3 p-4 pt-0 md:hidden">
          {shown.map((v) => (
            <li key={v.name} className="flex items-start justify-between gap-2 rounded-xl border border-line bg-surface p-3">
              <div className="flex min-w-0 flex-col gap-1.5">
                <span className="font-mono text-xs font-medium break-all text-fg">{shortName(v)}</span>
                {badges(v)}
                <p className="font-mono text-xs break-all text-muted">{v.mountpoint || t('none')}</p>
                <p className="text-xs text-muted">
                  {v.driver}
                  {v.created_at != null && ' · ' + formatRelative(v.created_at)}
                </p>
                {v.in_use && (
                  <p className="text-xs text-muted">
                    {t('colContainers')}: <UsedBy names={v.containers.map((c) => c.name)} />
                  </p>
                )}
              </div>
              {isAdmin && removeButton(v)}
            </li>
          ))}
        </ul>
      </ListBody>

      {removing && (
        <ConfirmDialog
          open
          danger
          onClose={() => setRemoving(null)}
          title={t('removeVolumeTitle')}
          message={<span className="break-all">{t('removeVolumeMessage', { name: removing.name })}</span>}
          warning={t('removeVolumeWarning')}
          // Anonymous names are 64 characters; typing them is not reasonable.
          requireText={removing.name.length <= 40 ? removing.name : t('pruneVolumesWord')}
          confirmLabel={t('actRemove')}
          cancelLabel={t('cancel')}
          error={removeError}
          onConfirm={async () => {
            setRemoveError(null)
            try {
              await api.del(`/docker/volumes/${encodeURIComponent(removing.name)}`)
              toast.success(t('removeVolumeDone'))
              setRemoving(null)
              void q.reload()
            } catch (e) {
              setRemoveError(errorMessage(e))
            }
          }}
        />
      )}
      {pruning && (
        <PruneModal
          onClose={() => setPruning(false)}
          onDone={() => {
            setPruning(false)
            void q.reload()
          }}
        />
      )}
    </Card>
  )
}
