import { useMemo, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import {
  Activity,
  Clock,
  Cpu,
  Gauge,
  HardDrive,
  MemoryStick,
  Monitor,
  Network,
  Server,
  Tag,
  Terminal,
  Thermometer,
  Timer,
  type LucideIcon,
} from 'lucide-react'
import {
  AreaChart,
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorState,
  IconTile,
  KeyValueList,
  ProgressBar,
  Select,
  Skeleton,
  Sparkline,
  Status,
  StatusDot,
  usageTone,
  type Tone,
} from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { cx, formatBytes, formatDateTime, formatDuration, formatPercent, formatRate, formatTemperature, formatTime } from '@/lib/format'
import { useAuth } from '@/stores/auth'
import { useSystemInfo, useSystemMetrics } from './store'
import { t } from './strings'
import type { HealthReport, HistoryRange, NetPoint, NetworkHistory, SystemInfo } from './types'

const linkClass = 'text-xs font-medium text-accent hover:underline'

function formatLoad(values: number[]): string {
  return values.map((v) => v.toLocaleString('tr-TR', { minimumFractionDigits: 2, maximumFractionDigits: 2 })).join(' ')
}

/** Server time in the server's own timezone. */
function formatServerTime(unix: number, timezone: string | undefined): string {
  if (timezone) {
    try {
      return new Intl.DateTimeFormat('tr-TR', {
        day: 'numeric',
        month: 'long',
        year: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
        timeZone: timezone,
      }).format(new Date(unix * 1000))
    } catch {
      // Unknown timezone name: fall back to the browser's zone.
    }
  }
  return formatDateTime(unix)
}

/* ---------- Header ---------- */

export function ServerStatusHeader() {
  const { snapshot, stream } = useSystemMetrics()
  const { info } = useSystemInfo()
  const online = stream === 'open' && snapshot !== null
  const tone: Tone = online ? 'success' : stream === 'closed' ? 'danger' : 'warning'
  const label = online ? t('online') : stream === 'closed' ? t('offline') : t('connecting')
  const uptime = snapshot?.uptime ?? info?.uptime
  const time = snapshot?.time ?? info?.time

  return (
    <header className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
      <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1">
        <h1 className="text-xl font-semibold text-fg sm:text-2xl">{t('serverStatus')}</h1>
        <Status tone={tone}>{label}</Status>
        {uptime !== undefined && (
          <span className="text-sm text-muted" title={t('uptimeLabel', { value: formatDuration(uptime) })}>
            {formatDuration(uptime)}
          </span>
        )}
      </div>
      {time !== undefined && (
        <time className="text-sm text-muted" title={t('serverTimeLabel')} dateTime={new Date(time * 1000).toISOString()}>
          {formatServerTime(time, info?.timezone)}
        </time>
      )}
    </header>
  )
}

/* ---------- Metric cards ---------- */

interface MetricCardProps {
  icon: LucideIcon
  tone: Tone
  title: string
  value: string
  secondary: ReactNode
  series: number[]
  max?: number
  /** Shown instead of the value and sparkline. */
  unavailable?: { title: string; hint: string }
}

function MetricCard({ icon, tone, title, value, secondary, series, max, unavailable }: MetricCardProps) {
  return (
    <Card className="flex min-w-0 items-start gap-3">
      <IconTile icon={icon} tone={unavailable ? 'neutral' : tone} />
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium text-muted">{title}</p>
        {unavailable ? (
          <>
            <p className="mt-1 text-sm font-semibold text-fg">{unavailable.title}</p>
            <p className="mt-1 text-xs text-faint">{unavailable.hint}</p>
          </>
        ) : (
          <>
            <div className="flex items-end justify-between gap-2">
              <p className="text-2xl font-semibold leading-tight text-fg sm:text-3xl">{value}</p>
              <Sparkline values={series} tone={tone} max={max} label={t('trendOf', { name: title })} className="h-9 w-20 shrink-0 sm:w-24" />
            </div>
            <p className="mt-1 truncate text-xs text-muted">{secondary}</p>
          </>
        )}
      </div>
    </Card>
  )
}

const cardGrid = 'grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4'

export function MetricCards() {
  const { snapshot, history, stream, reconnect } = useSystemMetrics()

  if (!snapshot) {
    if (stream === 'closed') {
      return (
        <Card>
          <ErrorState title={t('metricsTitle')} message={t('streamError')} onRetry={reconnect} className="py-4" />
        </Card>
      )
    }
    return (
      <div className={cardGrid} role="status" aria-label={t('measuring')}>
        {[0, 1, 2, 3].map((i) => (
          <Card key={i} className="flex items-start gap-3">
            <Skeleton className="size-10 rounded-xl" />
            <div className="flex-1 space-y-2">
              <Skeleton className="h-4 w-16" />
              <Skeleton className="h-8 w-24" />
              <Skeleton className="h-3 w-32" />
            </div>
          </Card>
        ))}
      </div>
    )
  }

  const { cpu, memory, root_disk: root, temperature } = snapshot
  const nvme = temperature.nvme
  const hottestNvme = nvme.length > 0 ? Math.max(...nvme.map((n) => n.celsius)) : null
  const mainTemp = temperature.cpu ?? hottestNvme
  const tempParts: string[] = []
  if (temperature.cpu !== null) tempParts.push(t('cpuTemp', { value: formatTemperature(temperature.cpu) }))
  if (hottestNvme !== null) tempParts.push(t('nvmeTemp', { value: formatTemperature(hottestNvme) }))
  const tempSeries = history.map((p) => p.temperature).filter((v): v is number => v !== null)
  const diskSeries = history.map((p) => p.disk).filter((v): v is number => v !== null)

  return (
    <div className={cardGrid}>
      <MetricCard
        icon={Cpu}
        tone="success"
        title={t('cpu')}
        value={cpu.percent === null ? '—' : formatPercent(cpu.percent)}
        secondary={<span title={t('loadAverage', { value: formatLoad([cpu.load1, cpu.load5, cpu.load15]) })}>{formatLoad([cpu.load1, cpu.load5, cpu.load15])}</span>}
        series={history.map((p) => p.cpu)}
        max={100}
      />
      <MetricCard
        icon={MemoryStick}
        tone="purple"
        title={t('ram')}
        value={formatPercent(memory.percent)}
        secondary={t('usedOfTotal', { used: formatBytes(memory.used), total: formatBytes(memory.total) })}
        series={history.map((p) => p.memory)}
        max={100}
      />
      <MetricCard
        icon={HardDrive}
        tone="accent"
        title={t('disk')}
        value={root ? formatPercent(root.percent) : '—'}
        secondary={root ? t('usedOfTotal', { used: formatBytes(root.used), total: formatBytes(root.total) }) : t('noRootDisk')}
        series={diskSeries}
        max={100}
      />
      <MetricCard
        icon={Thermometer}
        tone="warning"
        title={t('temperature')}
        value={formatTemperature(mainTemp)}
        secondary={tempParts.join(' · ')}
        series={tempSeries}
        max={100}
        unavailable={mainTemp === null ? { title: t('noSensor'), hint: t('noSensorHint') } : undefined}
      />
    </div>
  )
}

/* ---------- System information ---------- */

function cpuText(info: SystemInfo): string {
  const model = info.cpu_model || t('unknown')
  if (info.cpu_threads <= 0) return model
  return `${model} (${t('coresThreads', { cores: info.cpu_cores, threads: info.cpu_threads })})`
}

export function SystemInfoWidget() {
  const { info, error, loading, reload } = useSystemInfo()
  const { snapshot } = useSystemMetrics()
  const isAdmin = useAuth((s) => s.isAdmin)

  let body: ReactNode
  if (loading) {
    body = (
      <div className="space-y-3" role="status" aria-label={t('measuring')}>
        {[0, 1, 2, 3, 4, 5].map((i) => (
          <Skeleton key={i} className="h-4 w-full" />
        ))}
      </div>
    )
  } else if (error || !info) {
    body = <ErrorState message={error ?? t('infoError')} onRetry={reload} className="py-4" />
  } else {
    const load = snapshot ? [snapshot.cpu.load1, snapshot.cpu.load5, snapshot.cpu.load15] : info.load
    body = (
      <KeyValueList
        items={[
          { label: t('os'), value: info.os || t('unknown'), icon: Monitor },
          { label: t('kernel'), value: info.kernel || t('unknown'), icon: Terminal },
          { label: t('processor'), value: cpuText(info), icon: Cpu },
          { label: t('memory'), value: formatBytes(info.memory_total), icon: MemoryStick },
          { label: t('hostname'), value: info.hostname || t('unknown'), icon: Tag },
          { label: t('localIp'), value: info.ip || t('unknown'), icon: Network },
          { label: t('uptime'), value: formatDuration(snapshot?.uptime ?? info.uptime), icon: Timer },
          { label: t('load'), value: formatLoad([...load]), icon: Gauge },
          { label: t('time'), value: formatServerTime(snapshot?.time ?? info.time, info.timezone), icon: Clock },
        ]}
      />
    )
  }

  return (
    <Card>
      <CardHeader
        title={t('systemInfo')}
        actions={
          isAdmin ? (
            <Link
              to="/settings"
              className="inline-flex h-10 items-center rounded-lg border border-line bg-raised px-3 text-xs font-medium text-fg transition-colors hover:border-line-strong sm:h-8"
            >
              {t('edit')}
            </Link>
          ) : undefined
        }
      />
      {body}
    </Card>
  )
}

/* ---------- Network traffic ---------- */

const RANGES: Array<{ id: HistoryRange; seconds: number; label: () => string }> = [
  { id: '5m', seconds: 300, label: () => t('range5m') },
  { id: '15m', seconds: 900, label: () => t('range15m') },
  { id: '30m', seconds: 1800, label: () => t('range30m') },
  { id: '1h', seconds: 3600, label: () => t('range1h') },
]

export function TrafficWidget() {
  const [range, setRange] = useState<HistoryRange>('1h')
  const { netLive, snapshot } = useSystemMetrics()
  const { data, error, loading, reload } = useQuery<NetworkHistory>('/system/network/history', { query: { range } })

  const points = useMemo<NetPoint[]>(() => {
    // Wait for the selected range: never draw another range's points.
    if (!data || data.range !== range) return []
    const base = data.points
    const last = base[base.length - 1]?.t ?? 0
    const merged = [...base, ...netLive.filter((p) => p.t > last)]
    const seconds = RANGES.find((r) => r.id === range)?.seconds ?? 3600
    const newest = merged[merged.length - 1]?.t ?? 0
    return merged.filter((p) => p.t >= newest - seconds)
  }, [data, netLive, range])

  let body: ReactNode
  if (loading || (data && data.range !== range && !error)) {
    body = <Skeleton className="h-52 w-full" />
  } else if (error && !data) {
    body = <ErrorState message={error} onRetry={() => void reload()} className="py-4" />
  } else {
    body = (
      <>
        <AreaChart
          label={t('trafficChart')}
          emptyText={t('trafficEmpty')}
          formatValue={formatRate}
          labels={points.map((p) => formatTime(p.t))}
          series={[
            { id: 'rx', label: t('download'), tone: 'accent', values: points.map((p) => p.rx_rate) },
            { id: 'tx', label: t('upload'), tone: 'success', values: points.map((p) => p.tx_rate) },
          ]}
        />
        {snapshot && (
          <p className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted">
            <span>
              {t('download')}: <span className="text-fg">{formatRate(snapshot.network.rx_rate)}</span>
            </span>
            <span>
              {t('upload')}: <span className="text-fg">{formatRate(snapshot.network.tx_rate)}</span>
            </span>
          </p>
        )}
      </>
    )
  }

  return (
    <Card>
      <CardHeader
        title={t('traffic')}
        actions={
          <Select
            aria-label={t('range')}
            value={range}
            onChange={(e) => setRange(e.target.value as HistoryRange)}
            className="h-10 w-auto text-xs sm:h-9"
          >
            {RANGES.map((r) => (
              <option key={r.id} value={r.id}>
                {r.label()}
              </option>
            ))}
          </Select>
        }
      />
      {body}
    </Card>
  )
}

/* ---------- Storage ---------- */

export function StorageUsageWidget() {
  const { snapshot, stream, reconnect } = useSystemMetrics()
  const disks = snapshot?.disks.filter((d) => !d.system) ?? []

  let body: ReactNode
  if (!snapshot) {
    body =
      stream === 'closed' ? (
        <ErrorState message={t('streamError')} onRetry={reconnect} className="py-4" />
      ) : (
        <div className="space-y-4" role="status" aria-label={t('measuring')}>
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-9 w-full" />
          ))}
        </div>
      )
  } else if (disks.length === 0) {
    body = <EmptyState icon={HardDrive} title={t('noDisks')} description={t('noDisksHint')} className="py-6" />
  } else {
    body = (
      <ul className="flex flex-col gap-4">
        {disks.map((d) => (
          <li key={d.mount} className="flex items-center gap-3">
            <IconTile icon={HardDrive} tone={usageTone(d.percent)} size="sm" />
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline justify-between gap-2 text-xs">
                <span className="truncate text-sm font-medium text-fg" title={`${d.device} (${d.fstype})`}>
                  {d.mount}
                </span>
                <span className="shrink-0 text-muted">
                  {t('usedOfTotal', { used: formatBytes(d.used), total: formatBytes(d.total) })}
                  <span className="ml-2 text-fg">{formatPercent(d.percent)}</span>
                </span>
              </div>
              <ProgressBar value={d.percent} label={t('diskUsage', { mount: d.mount })} className="mt-1.5" />
            </div>
          </li>
        ))}
      </ul>
    )
  }

  return (
    <Card>
      <CardHeader
        title={t('storage')}
        actions={
          <Link to="/storage" className={cx(linkClass, 'inline-flex h-10 items-center sm:h-auto')}>
            {t('seeAll')}
          </Link>
        }
      />
      {body}
    </Card>
  )
}

/* ---------- Sidebar summary ---------- */

function healthView(report: HealthReport | undefined): { tone: Tone; label: string } {
  switch (report?.status) {
    case 'HEALTHY':
      return { tone: 'success', label: t('statusRunning') }
    case 'WARNING':
      return { tone: 'warning', label: t('statusWarning') }
    case 'CRITICAL':
      return { tone: 'danger', label: t('statusCritical') }
    default:
      return { tone: 'neutral', label: t('statusUnknown') }
  }
}

export function SidebarSystemSummary({ collapsed = false }: { collapsed?: boolean }) {
  const { info, error, loading, reload } = useSystemInfo()
  // Health changes slowly and is cached by the server; one request a minute.
  const health = useQuery<HealthReport>('/health', { refetchInterval: 60_000 })
  const status = healthView(health.data)

  const rows: Array<{ icon: LucideIcon; label: string; value: string }> = info
    ? [
        { icon: Monitor, label: t('os'), value: info.os || t('unknown') },
        { icon: Terminal, label: t('kernel'), value: info.kernel || t('unknown') },
        { icon: Cpu, label: t('processor'), value: info.cpu_model || t('unknown') },
        { icon: MemoryStick, label: t('memory'), value: t('ramShort', { value: formatBytes(info.memory_total, 0) }) },
      ]
    : []

  if (collapsed) {
    return (
      <section aria-label={t('systemSummary')} className="flex flex-col items-center gap-3 border-t border-line py-3">
        <span className="relative inline-flex" title={`${t('system')}: ${status.label}`}>
          <Server className="size-4 text-muted" aria-hidden />
          <span className="absolute -right-1 -top-1">
            <StatusDot tone={status.tone} />
          </span>
          <span className="sr-only">{`${t('system')}: ${status.label}`}</span>
        </span>
        {rows.map(({ icon: Icon, label, value }) => (
          <span key={label} title={`${label}: ${value}`} className="inline-flex">
            <Icon className="size-4 text-faint" aria-hidden />
            <span className="sr-only">{`${label}: ${value}`}</span>
          </span>
        ))}
      </section>
    )
  }

  return (
    <section aria-label={t('systemSummary')} className="border-t border-line px-3 py-3">
      <div className="mb-2.5 flex items-center gap-2 text-sm">
        <Server className="size-4 shrink-0 text-muted" aria-hidden />
        <span className="font-medium text-fg">{t('system')}</span>
        <Status tone={status.tone}>{status.label}</Status>
      </div>
      {loading ? (
        <div className="space-y-2" role="status" aria-label={t('measuring')}>
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="h-4 w-full" />
          ))}
        </div>
      ) : error || !info ? (
        <div className="space-y-2 text-xs text-muted">
          <p role="alert">{error ?? t('infoError')}</p>
          <Button size="sm" icon={Activity} onClick={reload}>
            {t('retry')}
          </Button>
        </div>
      ) : (
        <ul className="flex flex-col gap-2 text-xs text-muted">
          {rows.map(({ icon: Icon, label, value }) => (
            <li key={label} className="flex items-center gap-2" title={`${label}: ${value}`}>
              <Icon className="size-4 shrink-0 text-faint" aria-hidden />
              <span className="truncate">{value}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
