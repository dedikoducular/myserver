import { useId, useMemo, useState } from 'react'
import {
  Boxes,
  FileSearch,
  MoreVertical,
  Play,
  RotateCw,
  ScrollText,
  SearchX,
  Square,
  SquareTerminal,
  Trash2,
  Zap,
} from 'lucide-react'
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
  Menu,
  Modal,
  ProgressBar,
  Skeleton,
  Status,
  Switch,
  Tabs,
  tableClass,
  type MenuItem,
} from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { cx, formatDuration } from '@/lib/format'
import { t } from './strings'
import { cpuText, isActive, matches, portLabels, ramDetail, stateInfo } from './util'
import { useNow, useReloadOn, type DockerLive } from './live'
import { LogsModal } from './LogsModal'
import { InspectModal } from './InspectModal'
import { TerminalModal } from './TerminalModal'
import type { Container, ContainerStats } from './types'
import { usePersistentState } from '@/hooks/usePersistentState'

type Verb = 'start' | 'stop' | 'restart' | 'kill'
type Filter = 'all' | 'running' | 'stopped'
type Target = Pick<Container, 'id' | 'name'>

const doneKey = { start: 'doneStart', stop: 'doneStop', restart: 'doneRestart', kill: 'doneKill' } as const

function uptime(c: Container, now: number): string {
  if (c.started_at == null || (c.state !== 'running' && c.state !== 'paused')) return t('none')
  return formatDuration(Math.max(0, now - c.started_at))
}

function Ports({ c, max = 3 }: { c: Container; max?: number }) {
  const labels = portLabels(c.ports)
  if (labels.length === 0) return <span className="text-faint">{t('none')}</span>
  return (
    <span className="flex flex-wrap gap-1" title={labels.join(', ')}>
      {labels.slice(0, max).map((p) => (
        <Badge key={p} tone="cyan" className="font-mono">
          {p}
        </Badge>
      ))}
      {labels.length > max && <Badge>{t('morePorts', { n: labels.length - max })}</Badge>}
    </span>
  )
}

function Ips({ c }: { c: Container }) {
  if (c.addresses.length === 0) return <span className="text-faint">{t('none')}</span>
  return (
    <span className="flex flex-col font-mono text-xs" title={c.addresses.map((a) => `${a.network}: ${a.ip}`).join('\n')}>
      {c.addresses.slice(0, 2).map((a) => (
        <span key={a.network + a.ip} className="break-all">
          {a.ip}
        </span>
      ))}
      {c.addresses.length > 2 && <span className="text-faint">{t('morePorts', { n: c.addresses.length - 2 })}</span>}
    </span>
  )
}

function Usage({ s }: { s: ContainerStats | undefined }) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <span className="text-xs whitespace-nowrap">{ramDetail(s)}</span>
      {s && s.memory_limit > 0 && <ProgressBar value={s.memory_percent} label={t('colRam')} className="max-w-32" />}
    </div>
  )
}

function NameCell({ c }: { c: Container }) {
  return (
    <div className="min-w-0">
      <p className="truncate font-medium text-fg" title={c.name}>
        {c.name}
      </p>
      <p className="flex flex-wrap items-center gap-1.5 text-xs text-faint">
        <span className="font-mono">{c.short_id}</span>
        {c.app && <Badge tone="accent">{t('appBadge', { app: c.app })}</Badge>}
      </p>
    </div>
  )
}

interface ActionHandlers {
  busy: Verb | 'remove' | undefined
  run: (c: Container, verb: Verb) => void
  ask: (c: Container, verb: Exclude<Verb, 'start'>) => void
  remove: (c: Container) => void
  logs: (c: Container) => void
  inspect: (c: Container) => void
  terminal: (c: Container) => void
}

function RowActions({ c, h }: { c: Container; h: ActionHandlers }) {
  const active = isActive(c)
  const running = c.state === 'running'
  const busy = h.busy !== undefined
  const items: MenuItem[] = [
    { label: t('actInspect'), icon: FileSearch, onSelect: () => h.inspect(c) },
    { label: t('actLogs'), icon: ScrollText, onSelect: () => h.logs(c) },
    { label: t('actTerminal'), icon: SquareTerminal, onSelect: () => h.terminal(c), disabled: !running },
    { label: t('actRestart'), icon: RotateCw, onSelect: () => h.ask(c, 'restart'), disabled: busy, separated: true },
    { label: t('actKill'), icon: Zap, onSelect: () => h.ask(c, 'kill'), disabled: busy || !active, danger: true },
    { label: t('actRemove'), icon: Trash2, onSelect: () => h.remove(c), disabled: busy, danger: true, separated: true },
  ]
  return (
    <div className="flex items-center justify-end gap-0.5">
      {active ? (
        <IconButton icon={Square} label={t('actStop')} tone="warning" loading={h.busy === 'stop'} disabled={busy} onClick={() => h.ask(c, 'stop')} />
      ) : (
        <IconButton icon={Play} label={t('actStart')} tone="success" loading={h.busy === 'start'} disabled={busy} onClick={() => h.run(c, 'start')} />
      )}
      <IconButton icon={RotateCw} label={t('actRestart')} loading={h.busy === 'restart'} disabled={busy} onClick={() => h.ask(c, 'restart')} />
      <IconButton icon={ScrollText} label={t('actLogs')} onClick={() => h.logs(c)} />
      <IconButton icon={SquareTerminal} label={t('actTerminal')} disabled={!running} onClick={() => h.terminal(c)} />
      <Menu
        label={t('actMore', { name: c.name })}
        items={items}
        trigger={
          <span className="inline-flex size-10 items-center justify-center">
            <MoreVertical className="size-[18px]" aria-hidden />
          </span>
        }
      />
    </div>
  )
}

function RemoveDialog({ target, running, onClose, onDone }: { target: Target; running: boolean; onClose: () => void; onDone: () => void }) {
  const [force, setForce] = useState(false)
  const [volumes, setVolumes] = useState(false)
  const [typed, setTyped] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const inputId = useId()

  const allowed = (!running || force) && (!volumes || typed.trim() === target.name)

  const confirm = async () => {
    if (!allowed || pending) return
    setPending(true)
    setError(null)
    try {
      await api.post(`/docker/containers/${encodeURIComponent(target.id)}/remove`, { force, remove_volumes: volumes })
      toast.success(t('doneRemove', { name: target.name }))
      onDone()
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
      title={t('removeTitle')}
      size="sm"
      busy={pending}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={pending} data-autofocus>
            {t('cancel')}
          </Button>
          <Button variant="danger" icon={Trash2} onClick={() => void confirm()} loading={pending} disabled={!allowed}>
            {t('actRemove')}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4 text-sm">
        <p className="text-fg">{t('removeMessage', { name: target.name })}</p>
        {running && !force && <Alert tone="warning">{t('removeRunningNote')}</Alert>}
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="font-medium text-fg">{t('removeForce')}</p>
            <p className="text-xs text-muted">{t('removeForceHint')}</p>
          </div>
          <Switch checked={force} onChange={setForce} label={t('removeForce')} disabled={pending} />
        </div>
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="font-medium text-fg">{t('removeVolumes')}</p>
            <p className="text-xs text-muted">{t('removeVolumesHint')}</p>
          </div>
          <Switch checked={volumes} onChange={setVolumes} label={t('removeVolumes')} disabled={pending} />
        </div>
        {volumes && (
          <>
            <Alert tone="danger">{t('removeVolumesWarning')}</Alert>
            <div className="flex flex-col gap-1.5">
              <label htmlFor={inputId} className="text-xs text-muted">
                {t('removeTypeName')}: <span className="font-mono font-semibold text-fg">{target.name}</span>
              </label>
              <Input
                id={inputId}
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
              />
            </div>
          </>
        )}
        {error && <Alert tone="danger">{error}</Alert>}
      </div>
    </Modal>
  )
}

function ListSkeleton() {
  return (
    <div className="flex flex-col gap-3 p-4 sm:p-5" aria-hidden>
      {[0, 1, 2, 3].map((i) => (
        <Skeleton key={i} className="h-14 w-full" />
      ))}
    </div>
  )
}

export function ContainersTab({ live }: { live: DockerLive }) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const q = useQuery<Container[]>('/docker/containers')
  useReloadOn(live.revision.container, q.reload)
  const now = useNow()

  const [search, setSearch] = useState('')
  const [filter, setFilter] = usePersistentState<Filter>('docker.filter', 'all', ['all', 'running', 'stopped'])
  const [busy, setBusy] = useState<Record<string, Verb | 'remove'>>({})
  const [confirm, setConfirm] = useState<{ c: Container; verb: Exclude<Verb, 'start'> } | null>(null)
  const [removing, setRemoving] = useState<Container | null>(null)
  const [logs, setLogs] = useState<Target | null>(null)
  const [inspect, setInspect] = useState<Target | null>(null)
  const [terminal, setTerminal] = useState<Target | null>(null)
  const searchId = useId()

  const all = useMemo(() => q.data ?? [], [q.data])
  const counts = useMemo(() => {
    const running = all.filter(isActive).length
    return { all: all.length, running, stopped: all.length - running }
  }, [all])
  const shown = useMemo(
    () =>
      all.filter((c) => {
        if (filter === 'running' && !isActive(c)) return false
        if (filter === 'stopped' && isActive(c)) return false
        return matches(search, c.name, c.image, c.short_id, c.app)
      }),
    [all, filter, search],
  )

  const run = async (c: Container, verb: Verb) => {
    setBusy((b) => ({ ...b, [c.id]: verb }))
    try {
      await api.post(`/docker/containers/${encodeURIComponent(c.id)}/${verb}`)
      toast.success(t(doneKey[verb], { name: c.name }))
      await q.reload()
    } catch (e) {
      toast.error(errorMessage(e))
      throw e
    } finally {
      setBusy((b) => {
        const next = { ...b }
        delete next[c.id]
        return next
      })
    }
  }

  const handlers = (c: Container): ActionHandlers => ({
    busy: busy[c.id],
    run: (x, verb) => void run(x, verb).catch(() => undefined),
    ask: (x, verb) => setConfirm({ c: x, verb }),
    remove: setRemoving,
    logs: setLogs,
    inspect: setInspect,
    terminal: setTerminal,
  })

  let body
  if (q.loading) {
    body = <ListSkeleton />
  } else if (q.error && all.length === 0) {
    body = <ErrorState message={q.error} onRetry={() => void q.reload()} />
  } else if (all.length === 0) {
    body = <EmptyState icon={Boxes} title={t('noContainers')} description={t('noContainersHint')} />
  } else if (shown.length === 0) {
    body = <EmptyState icon={SearchX} title={t('noMatch')} description={t('noMatchHint')} />
  } else {
    body = (
      <>
        {/* Wide screens: table. No scroll wrapper, so row menus are not clipped. */}
        <div className="hidden px-5 pb-3 xl:block">
          <table className="w-full table-fixed border-collapse text-left text-sm">
            <colgroup>
              <col className="w-[17%]" />
              <col className="w-[17%]" />
              <col className="w-[11%]" />
              <col className="w-[10%]" />
              <col className="w-[6%]" />
              <col className="w-[11%]" />
              <col className="w-[9%]" />
              <col />
              {isAdmin && <col className="w-[216px]" />}
            </colgroup>
            <thead>
              <tr>
                <th className={tableClass.th}>{t('colName')}</th>
                <th className={tableClass.th}>{t('colImage')}</th>
                <th className={tableClass.th}>{t('colState')}</th>
                <th className={tableClass.th}>{t('colUptime')}</th>
                <th className={tableClass.th}>{t('colCpu')}</th>
                <th className={tableClass.th}>{t('colRam')}</th>
                <th className={tableClass.th}>{t('colIp')}</th>
                <th className={tableClass.th}>{t('colPorts')}</th>
                {isAdmin && <th className={cx(tableClass.th, 'text-right')}>{t('colActions')}</th>}
              </tr>
            </thead>
            <tbody>
              {shown.map((c) => {
                const st = stateInfo(c)
                const s = live.stats.get(c.id)
                return (
                  <tr key={c.id} className={tableClass.row}>
                    <td className={tableClass.td}>
                      <NameCell c={c} />
                    </td>
                    <td className={cx(tableClass.td, 'text-muted')}>
                      <span className="block truncate" title={c.image}>
                        {c.image}
                      </span>
                    </td>
                    <td className={tableClass.td} title={c.status}>
                      <Status tone={st.tone} pulse={st.pulse}>
                        {st.label}
                      </Status>
                    </td>
                    <td className={cx(tableClass.td, 'text-xs text-muted')}>{uptime(c, now)}</td>
                    <td className={cx(tableClass.td, 'text-xs whitespace-nowrap')}>{cpuText(s)}</td>
                    <td className={tableClass.td}>
                      <Usage s={s} />
                    </td>
                    <td className={tableClass.td}>
                      <Ips c={c} />
                    </td>
                    <td className={tableClass.td}>
                      <Ports c={c} />
                    </td>
                    {isAdmin && (
                      <td className={cx(tableClass.td, 'px-0')}>
                        <RowActions c={c} h={handlers(c)} />
                      </td>
                    )}
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>

        {/* Phones and tablets: stacked cards. */}
        <ul className="grid gap-3 p-4 pt-0 sm:grid-cols-2 sm:p-5 sm:pt-0 xl:hidden">
          {shown.map((c) => {
            const st = stateInfo(c)
            const s = live.stats.get(c.id)
            return (
              <li key={c.id} className="flex min-w-0 flex-col gap-3 rounded-xl border border-line bg-surface p-3">
                <div className="flex items-start justify-between gap-2">
                  <NameCell c={c} />
                  <Status tone={st.tone} pulse={st.pulse}>
                    {st.label}
                  </Status>
                </div>
                <p className="truncate text-xs text-muted" title={c.image}>
                  {c.image}
                </p>
                <dl className="grid grid-cols-2 gap-x-3 gap-y-2 text-xs">
                  <div>
                    <dt className="text-faint">{t('colCpu')}</dt>
                    <dd className="text-fg">{cpuText(s)}</dd>
                  </div>
                  <div>
                    <dt className="text-faint">{t('colRam')}</dt>
                    <dd className="text-fg">
                      <Usage s={s} />
                    </dd>
                  </div>
                  <div>
                    <dt className="text-faint">{t('colUptime')}</dt>
                    <dd className="text-fg">{uptime(c, now)}</dd>
                  </div>
                  <div>
                    <dt className="text-faint">{t('colIp')}</dt>
                    <dd className="text-fg">
                      <Ips c={c} />
                    </dd>
                  </div>
                  <div className="col-span-2">
                    <dt className="mb-0.5 text-faint">{t('colPorts')}</dt>
                    <dd>
                      <Ports c={c} max={6} />
                    </dd>
                  </div>
                </dl>
                {isAdmin && (
                  <div className="-mx-1 -mb-1 border-t border-line pt-1">
                    <RowActions c={c} h={handlers(c)} />
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      </>
    )
  }

  const verb = confirm?.verb
  return (
    <Card padded={false}>
      <div className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between sm:p-5">
        <Tabs<Filter>
          label={t('filterLabel')}
          value={filter}
          onChange={setFilter}
          items={[
            { id: 'all', label: t('filterAll'), count: counts.all },
            { id: 'running', label: t('filterRunning'), count: counts.running },
            { id: 'stopped', label: t('filterStopped'), count: counts.stopped },
          ]}
        />
        <div className="sm:w-72">
          <label htmlFor={searchId} className="sr-only">
            {t('searchContainers')}
          </label>
          <Input
            id={searchId}
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('searchContainers')}
            autoComplete="off"
          />
        </div>
      </div>
      {q.error && all.length > 0 && (
        <div className="px-4 pb-3 sm:px-5">
          <Alert tone="danger">{q.error}</Alert>
        </div>
      )}
      {body}

      {confirm && verb && (
        <ConfirmDialog
          open
          danger={verb === 'kill'}
          onClose={() => setConfirm(null)}
          title={t(verb === 'stop' ? 'stopTitle' : verb === 'restart' ? 'restartTitle' : 'killTitle')}
          message={t(verb === 'stop' ? 'stopMessage' : verb === 'restart' ? 'restartMessage' : 'killMessage', {
            name: confirm.c.name,
          })}
          warning={verb === 'kill' ? t('killWarning') : undefined}
          confirmLabel={t(verb === 'stop' ? 'actStop' : verb === 'restart' ? 'actRestart' : 'actKill')}
          cancelLabel={t('cancel')}
          onConfirm={async () => {
            try {
              await run(confirm.c, verb)
              setConfirm(null)
            } catch {
              // The toast already explained the failure; keep the dialog open.
            }
          }}
        />
      )}
      {removing && (
        <RemoveDialog
          target={removing}
          running={isActive(removing)}
          onClose={() => setRemoving(null)}
          onDone={() => {
            setRemoving(null)
            void q.reload()
          }}
        />
      )}
      {logs && <LogsModal container={logs} onClose={() => setLogs(null)} />}
      {inspect && <InspectModal container={inspect} onClose={() => setInspect(null)} />}
      {terminal && <TerminalModal container={terminal} onClose={() => setTerminal(null)} />}
    </Card>
  )
}
