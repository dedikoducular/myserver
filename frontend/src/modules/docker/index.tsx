import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Boxes, Container as ContainerIcon, RefreshCw } from 'lucide-react'
import {
  Alert,
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorState,
  KeyValueList,
  PageHeader,
  Skeleton,
  Status,
  Tabs,
  tableClass,
} from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { cx, formatBytes } from '@/lib/format'
import { t } from './strings'
import { cpuText, portLabels, ramText, stateInfo } from './util'
import { useDockerLive, useReloadOn } from './live'
import { ContainersTab } from './ContainersTab'
import { ImagesTab } from './ImagesTab'
import { VolumesTab } from './VolumesTab'
import { NetworksTab } from './NetworksTab'
import type { Container, DockerInfo } from './types'

type TabId = 'containers' | 'images' | 'volumes' | 'networks'

const TAB_KEY = 'myserver.docker.tab'

function initialTab(): TabId {
  try {
    const v = sessionStorage.getItem(TAB_KEY)
    if (v === 'containers' || v === 'images' || v === 'volumes' || v === 'networks') return v
  } catch {
    // Storage may be unavailable; start on the first tab.
  }
  return 'containers'
}

export default function DockerPage() {
  const [tab, setTabState] = useState<TabId>(initialTab)
  const live = useDockerLive()

  const setTab = (id: TabId) => {
    setTabState(id)
    try {
      sessionStorage.setItem(TAB_KEY, id)
    } catch {
      // Not remembered; harmless.
    }
  }

  return (
    <div>
      <PageHeader
        title={t('title')}
        description={t('subtitle')}
        icon={ContainerIcon}
        actions={
          live.available === false ? (
            <Status tone="danger">{t('unavailableTitle')}</Status>
          ) : live.stream === 'open' && live.available ? (
            <Status tone="success" pulse>
              {t('live')}
            </Status>
          ) : (
            <Status tone="neutral">{t('liveOff')}</Status>
          )
        }
      />
      {live.available === false && (
        <Alert tone="danger" title={t('unavailableTitle')} className="mb-4">
          {t('unavailableBody')}
        </Alert>
      )}
      <Tabs<TabId>
        label={t('tabs')}
        value={tab}
        onChange={setTab}
        className="mb-4 w-fit"
        items={[
          { id: 'containers', label: t('tabContainers') },
          { id: 'images', label: t('tabImages') },
          { id: 'volumes', label: t('tabVolumes') },
          { id: 'networks', label: t('tabNetworks') },
        ]}
      />
      <div role="tabpanel">
        {tab === 'containers' && <ContainersTab live={live} />}
        {tab === 'images' && <ImagesTab live={live} />}
        {tab === 'volumes' && <VolumesTab live={live} />}
        {tab === 'networks' && <NetworksTab live={live} />}
      </div>
    </div>
  )
}

/* ---------- dashboard widget ---------- */

const WIDGET_ROWS = 6

function WidgetPorts({ c }: { c: Container }) {
  const labels = portLabels(c.ports)
  if (labels.length === 0) return <span className="text-faint">{t('none')}</span>
  return (
    <span className="font-mono text-xs" title={labels.join(', ')}>
      {labels[0]}
      {labels.length > 1 && <span className="text-faint"> {t('morePorts', { n: labels.length - 1 })}</span>}
    </span>
  )
}

/** "Docker Konteynerleri" table of the dashboard. */
export function ContainersWidget() {
  const live = useDockerLive()
  const q = useQuery<Container[]>('/docker/containers')
  useReloadOn(live.revision.container, q.reload)

  const all = q.data ?? []
  const rows = all.slice(0, WIDGET_ROWS)

  let body
  if (q.loading) {
    body = (
      <div className="flex flex-col gap-2.5" aria-hidden>
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-8 w-full" />
        ))}
      </div>
    )
  } else if (q.error && all.length === 0) {
    body = <ErrorState className="py-4" message={q.error} onRetry={() => void q.reload()} />
  } else if (all.length === 0) {
    body = <EmptyState className="py-6" icon={Boxes} title={t('widgetEmpty')} description={t('widgetEmptyHint')} />
  } else {
    body = (
      <>
        <table className="hidden w-full table-fixed border-collapse text-left text-sm sm:table">
          <colgroup>
            <col className="w-[22%]" />
            <col />
            <col className="w-[20%]" />
            <col className="w-[11%]" />
            <col className="w-[13%]" />
            <col className="w-[16%]" />
          </colgroup>
          <thead>
            <tr>
              <th className={tableClass.th}>{t('colName')}</th>
              <th className={tableClass.th}>{t('colImage')}</th>
              <th className={tableClass.th}>{t('colState')}</th>
              <th className={tableClass.th}>{t('colCpu')}</th>
              <th className={tableClass.th}>{t('colRam')}</th>
              <th className={tableClass.th}>{t('colPorts')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => {
              const st = stateInfo(c)
              const s = live.stats.get(c.id)
              return (
                <tr key={c.id} className={tableClass.row}>
                  <td className={cx(tableClass.td, 'font-medium')}>
                    <span className="block truncate" title={c.name}>
                      {c.name}
                    </span>
                  </td>
                  <td className={cx(tableClass.td, 'text-muted')}>
                    <span className="block truncate" title={c.image}>
                      {c.image}
                    </span>
                  </td>
                  <td className={tableClass.td} title={c.status}>
                    <Status tone={st.tone} pulse={st.pulse}>
                      <span className="truncate">{st.label}</span>
                    </Status>
                  </td>
                  <td className={cx(tableClass.td, 'text-xs whitespace-nowrap')}>{cpuText(s)}</td>
                  <td className={cx(tableClass.td, 'text-xs whitespace-nowrap')}>{ramText(s)}</td>
                  <td className={cx(tableClass.td, 'truncate')}>
                    <WidgetPorts c={c} />
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>

        <ul className="flex flex-col divide-y divide-line/60 sm:hidden">
          {rows.map((c) => {
            const st = stateInfo(c)
            const s = live.stats.get(c.id)
            return (
              <li key={c.id} className="flex flex-col gap-1 py-2.5 first:pt-0 last:pb-0">
                <div className="flex items-center justify-between gap-2">
                  <span className="min-w-0 truncate text-sm font-medium text-fg">{c.name}</span>
                  <Status tone={st.tone} pulse={st.pulse}>
                    {st.label}
                  </Status>
                </div>
                <p className="truncate text-xs text-muted">{c.image}</p>
                <p className="flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted">
                  <span>
                    {t('colCpu')} <span className="text-fg">{cpuText(s)}</span>
                  </span>
                  <span>
                    {t('colRam')} <span className="text-fg">{ramText(s)}</span>
                  </span>
                  <span>
                    {t('colPorts')} <WidgetPorts c={c} />
                  </span>
                </p>
              </li>
            )
          })}
        </ul>

        {all.length > rows.length && (
          <p className="mt-3 text-xs text-faint">{t('widgetMore', { n: all.length - rows.length })}</p>
        )}
      </>
    )
  }

  return (
    <Card>
      <CardHeader
        title={t('widgetTitle')}
        actions={
          <Link to="/docker" className="inline-flex min-h-10 items-center text-xs font-medium text-accent hover:underline sm:min-h-0">
            {t('widgetAll')}
          </Link>
        }
      />
      {body}
    </Card>
  )
}

/* ---------- settings section ---------- */

/** Read-only information about the Docker engine. */
export function DockerSettingsSection() {
  const q = useQuery<DockerInfo>('/docker/info')
  const d = q.data

  return (
    <Card>
      <CardHeader
        title={t('settingsTitle')}
        subtitle={t('settingsSubtitle')}
        icon={ContainerIcon}
        actions={
          <Button size="md" variant="ghost" icon={RefreshCw} onClick={() => void q.reload()} loading={q.fetching && !q.loading}>
            {t('refresh')}
          </Button>
        }
      />
      {q.loading ? (
        <div className="flex flex-col gap-2.5" aria-hidden>
          {[0, 1, 2, 3, 4].map((i) => (
            <Skeleton key={i} className="h-5 w-full" />
          ))}
        </div>
      ) : q.error ? (
        <ErrorState className="py-4" message={q.error} onRetry={() => void q.reload()} />
      ) : d ? (
        <KeyValueList
          items={[
            { label: t('sVersion'), value: d.version || t('none') },
            { label: t('sApi'), value: d.api_version || t('none') },
            { label: t('sStorage'), value: d.storage_driver || t('none') },
            { label: t('sRoot'), value: <span className="font-mono text-xs break-all">{d.root_dir || t('none')}</span> },
            {
              label: t('sContainers'),
              value:
                t('sContainersValue', { total: d.containers, running: d.containers_running, stopped: d.containers_stopped }) +
                (d.containers_paused > 0 ? t('sContainersPaused', { paused: d.containers_paused }) : ''),
            },
            { label: t('sImages'), value: String(d.images) },
            { label: t('sLogging'), value: d.logging_driver || t('none') },
            {
              label: t('sCgroup'),
              value: [d.cgroup_driver, d.cgroup_version && 'v' + d.cgroup_version].filter(Boolean).join(' · ') || t('none'),
            },
            { label: t('sOs'), value: d.operating_system || t('none') },
            { label: t('sKernel'), value: d.kernel_version || t('none') },
            {
              label: t('sArch'),
              value: [d.architecture, d.cpus > 0 && `${d.cpus} CPU`, d.memory_total > 0 && formatBytes(d.memory_total)]
                .filter(Boolean)
                .join(' · '),
            },
          ]}
        />
      ) : null}
    </Card>
  )
}
