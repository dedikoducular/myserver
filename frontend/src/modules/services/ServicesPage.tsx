import { useMemo, useState } from 'react'
import { RefreshCw, Search, SearchX, ServerCog, Star } from 'lucide-react'
import {
  Badge,
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorState,
  Input,
  LoadingState,
  PageHeader,
  Status,
  Tabs,
} from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { cx, formatBytes, formatDuration } from '@/lib/format'
import { useAuth } from '@/stores/auth'
import { ServiceActions, useServiceControl, type ServiceControl } from './control'
import { LogsModal } from './LogsModal'
import { t } from './strings'
import type { Service, ServiceList } from './types'
import { bootLabel, stateLabel, stateTone, uptimeSeconds } from './util'

type Filter = 'all' | 'running' | 'stopped' | 'failed' | 'disabled'

const PAGE = 100
const GRID = 'xl:grid xl:grid-cols-[minmax(0,1fr)_7.5rem_6.5rem_5.5rem_10rem_14.5rem] xl:items-center xl:gap-3'

function matches(svc: Service, filter: Filter): boolean {
  return filter === 'all' || svc.state === filter
}

function StateBadge({ svc }: { svc: Service }) {
  const busy = svc.state === 'activating' || svc.state === 'deactivating'
  return (
    <Status tone={stateTone(svc)} pulse={busy}>
      {stateLabel(svc.state)}
    </Status>
  )
}

function Meta({ label, children }: { label: string; children: string }) {
  return (
    <span className="text-xs text-muted xl:text-sm">
      <span className="text-faint xl:hidden">{label}: </span>
      {children}
    </span>
  )
}

function ServiceRow({
  svc,
  now,
  isAdmin,
  control,
  onLogs,
}: {
  svc: Service
  now: number
  isAdmin: boolean
  control: ServiceControl
  onLogs: (svc: Service) => void
}) {
  const uptime = uptimeSeconds(svc, now)
  return (
    <li className={cx('flex flex-col gap-2.5 border-b border-line/60 py-3 last:border-b-0', GRID)}>
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="truncate text-sm font-medium text-fg">{svc.name}</span>
          {svc.risk === 'critical' && <Badge tone="warning">{t('critical')}</Badge>}
          {svc.risk === 'protected' && <Badge tone="purple">{t('protected')}</Badge>}
        </div>
        <p className="truncate font-mono text-[11px] text-faint">
          {svc.unit}
          {svc.main_pid != null && ` · ${t('pid', { pid: svc.main_pid })}`}
        </p>
        {svc.description && <p className="truncate text-xs text-muted">{svc.description}</p>}
        {svc.triggered_by.length > 0 && (
          <p className="truncate text-xs text-faint">{t('socket', { names: svc.triggered_by.join(', ') })}</p>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 xl:contents">
        <StateBadge svc={svc} />
        <Meta label={t('col_boot')}>{svc.installed ? bootLabel(svc.unit_file_state) : t('b_unknown')}</Meta>
        <Meta label={t('col_memory')}>{formatBytes(svc.memory_bytes)}</Meta>
        <Meta label={t('col_uptime')}>{formatDuration(uptime)}</Meta>
      </div>
      {isAdmin && svc.installed ? <ServiceActions svc={svc} control={control} onLogs={onLogs} /> : <span className="hidden xl:block" />}
    </li>
  )
}

function ListHeader() {
  return (
    <div className={cx('hidden border-b border-line pb-2 text-[11px] font-medium uppercase tracking-wide text-faint', GRID)} aria-hidden>
      <span>{t('col_service')}</span>
      <span>{t('col_state')}</span>
      <span>{t('col_boot')}</span>
      <span>{t('col_memory')}</span>
      <span>{t('col_uptime')}</span>
      <span>{t('col_actions')}</span>
    </div>
  )
}

export default function ServicesPage() {
  const isAdmin = useAuth((s) => s.isAdmin)
  // Service state changes slowly and the server caches the list.
  const list = useQuery<ServiceList>('/services', { refetchInterval: 15000 })
  const [search, setSearch] = useState('')
  const [filter, setFilter] = useState<Filter>('all')
  const [limit, setLimit] = useState(PAGE)
  const [logsFor, setLogsFor] = useState<Service | null>(null)
  const control = useServiceControl(() => void list.reload())

  const services = list.data?.services
  const counts = useMemo(() => {
    const c: Record<Filter, number> = { all: 0, running: 0, stopped: 0, failed: 0, disabled: 0 }
    for (const s of services ?? []) {
      c.all++
      if (s.state === 'running' || s.state === 'stopped' || s.state === 'failed' || s.state === 'disabled') c[s.state]++
    }
    return c
  }, [services])

  const visible = useMemo(() => {
    const q = search.trim().toLocaleLowerCase('tr-TR')
    return (services ?? []).filter(
      (s) =>
        matches(s, filter) &&
        (!q ||
          s.unit.toLowerCase().includes(q.toLowerCase()) ||
          s.name.toLocaleLowerCase('tr-TR').includes(q) ||
          s.description.toLocaleLowerCase('tr-TR').includes(q)),
    )
  }, [services, search, filter])

  const now = Math.floor(Date.now() / 1000)
  const header = (
    <PageHeader
      title={t('title')}
      description={t('subtitle')}
      icon={ServerCog}
      actions={
        <Button icon={RefreshCw} onClick={() => void list.reload()} loading={list.fetching && !list.loading}>
          {t('refresh')}
        </Button>
      }
    />
  )

  if (list.loading) {
    return (
      <>
        {header}
        <Card>
          <LoadingState />
        </Card>
      </>
    )
  }
  if (!list.data) {
    return (
      <>
        {header}
        <Card>
          <ErrorState message={list.error ?? ''} onRetry={() => void list.reload()} />
        </Card>
      </>
    )
  }

  const featured = list.data.featured
  return (
    <>
      {header}
      {list.error && (
        <Card className="mb-4" padded={false}>
          <ErrorState className="py-4" message={list.error} onRetry={() => void list.reload()} />
        </Card>
      )}

      <Card className="mb-4">
        <CardHeader title={t('featured')} icon={Star} />
        {featured.length === 0 ? (
          <EmptyState icon={Star} title={t('featured_empty')} description={t('featured_empty_hint')} />
        ) : (
          <>
            <ListHeader />
            <ul>
              {featured.map((s) => (
                <ServiceRow key={s.unit} svc={s} now={now} isAdmin={isAdmin} control={control} onLogs={setLogsFor} />
              ))}
            </ul>
          </>
        )}
      </Card>

      <Card>
        <CardHeader title={t('all')} icon={ServerCog} subtitle={t('count', { n: counts.all })} />
        <div className="mb-4 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
          <div className="relative w-full lg:max-w-xs">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-faint" aria-hidden />
            <Input
              type="search"
              aria-label={t('search')}
              placeholder={t('search_placeholder')}
              className="pl-9"
              value={search}
              onChange={(e) => {
                setSearch(e.target.value)
                setLimit(PAGE)
              }}
            />
          </div>
          <Tabs<Filter>
            label={t('filter')}
            value={filter}
            onChange={(f) => {
              setFilter(f)
              setLimit(PAGE)
            }}
            items={[
              { id: 'all', label: t('f_all'), count: counts.all },
              { id: 'running', label: t('f_running'), count: counts.running },
              { id: 'stopped', label: t('f_stopped'), count: counts.stopped },
              { id: 'failed', label: t('f_failed'), count: counts.failed },
              { id: 'disabled', label: t('f_disabled'), count: counts.disabled },
            ]}
          />
        </div>

        {counts.all === 0 ? (
          <EmptyState icon={ServerCog} title={t('empty_title')} description={t('empty_hint')} />
        ) : visible.length === 0 ? (
          <EmptyState icon={SearchX} title={t('none_title')} description={t('none_hint')} />
        ) : (
          <>
            <ListHeader />
            <ul>
              {visible.slice(0, limit).map((s) => (
                <ServiceRow key={s.unit} svc={s} now={now} isAdmin={isAdmin} control={control} onLogs={setLogsFor} />
              ))}
            </ul>
            {visible.length > limit && (
              <div className="mt-4 flex justify-center">
                <Button onClick={() => setLimit(limit + PAGE)}>{t('more', { n: visible.length - limit })}</Button>
              </div>
            )}
          </>
        )}
      </Card>

      {control.dialog}
      {logsFor && <LogsModal key={logsFor.unit} svc={logsFor} onClose={() => setLogsFor(null)} />}
    </>
  )
}
