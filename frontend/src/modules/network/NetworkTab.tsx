import { useState, type ReactNode } from 'react'
import {
  Cable,
  ChevronDown,
  ChevronRight,
  FileCode2,
  Globe,
  Layers,
  Network,
  RefreshCw,
  Router,
  Server,
  Wifi,
  type LucideIcon,
} from 'lucide-react'
import {
  Alert,
  Badge,
  Button,
  Card,
  CardHeader,
  EmptyState,
  ErrorState,
  IconTile,
  KeyValueList,
  LoadingState,
  Skeleton,
  Status,
  TableWrap,
  tableClass,
  type Tone,
} from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { cx, formatBytes, formatDuration, formatShortDateTime } from '@/lib/format'
import { useAuth } from '@/stores/auth'
import { t } from './strings'
import type { Gateway, InterfaceKind, Listener, NetInterface, NetplanView, Overview } from './types'

const kindLabel: Record<InterfaceKind, () => string> = {
  ethernet: () => t('kindEthernet'),
  wifi: () => t('kindWifi'),
  bridge: () => t('kindBridge'),
  bond: () => t('kindBond'),
  vlan: () => t('kindVlan'),
  virtual: () => t('kindVirtual'),
  loopback: () => t('kindLoopback'),
}

const kindIcon: Record<InterfaceKind, LucideIcon> = {
  ethernet: Cable,
  wifi: Wifi,
  bridge: Network,
  bond: Layers,
  vlan: Layers,
  virtual: Layers,
  loopback: Server,
}

function stateView(it: NetInterface): { tone: Tone; label: string } {
  if (!it.admin_up) return { tone: 'neutral', label: t('stateDisabled') }
  switch (it.state) {
    case 'up':
      return { tone: 'success', label: t('stateUp') }
    case 'down':
      return { tone: 'danger', label: t('stateDown') }
    case 'dormant':
      return { tone: 'warning', label: t('stateDormant') }
    case 'lowerlayerdown':
      return { tone: 'warning', label: t('stateLower') }
    default:
      return { tone: 'neutral', label: t('stateUnknown') }
  }
}

function count(n: number): string {
  return n.toLocaleString('tr-TR')
}

function gatewayText(g: Gateway): string {
  return g.address ? t('gatewayVia', { address: g.address, iface: g.interface }) : `${g.interface} (${t('gatewayOnLink')})`
}

function scopeLabel(scope: NetInterface['ipv6'][number]['scope']): string {
  switch (scope) {
    case 'global':
      return t('scopeGlobal')
    case 'unique_local':
      return t('scopeUla')
    case 'link':
      return t('scopeLink')
    default:
      return t('scopeHost')
  }
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-3 text-xs">
      <dt className="shrink-0 text-muted">{label}</dt>
      <dd className="min-w-0 break-all text-right text-fg">{children}</dd>
    </div>
  )
}

function InterfaceCard({ it, dns }: { it: NetInterface; dns: string[] }) {
  const state = stateView(it)
  const speed =
    it.speed_mbps != null
      ? t('speedValue', { speed: count(it.speed_mbps) }) +
        (it.duplex ? ` · ${it.duplex === 'full' ? t('duplexFull') : t('duplexHalf')}` : '')
      : t('speedUnknown')
  return (
    <Card className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <IconTile icon={kindIcon[it.kind] ?? Network} tone={it.state === 'up' ? 'accent' : 'neutral'} />
          <div className="min-w-0">
            <p className="truncate font-mono text-sm font-semibold text-fg">{it.name}</p>
            <p className="text-xs text-muted">{(kindLabel[it.kind] ?? kindLabel.virtual)()}</p>
          </div>
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1">
          <Status tone={state.tone}>{state.label}</Status>
          {it.default_route && <Badge tone="accent">{t('defaultRoute')}</Badge>}
        </div>
      </div>

      <dl className="flex flex-col gap-1.5">
        <Row label={t('ipv4')}>
          {it.ipv4.length === 0 ? (
            <span className="text-faint">{t('noAddress')}</span>
          ) : (
            it.ipv4.map((a) => (
              <span key={a.address} className="block font-mono">
                {a.address}/{a.prefix}
              </span>
            ))
          )}
        </Row>
        <Row label={t('ipv6')}>
          {it.ipv6.length === 0 ? (
            <span className="text-faint">{t('noAddress')}</span>
          ) : (
            it.ipv6.map((a) => (
              <span key={a.address} className="block font-mono">
                {a.address}/{a.prefix} <span className="font-sans text-faint">({scopeLabel(a.scope)})</span>
              </span>
            ))
          )}
        </Row>
        {it.default_route && (
          <>
            <Row label={t('gateway')}>
              {it.gateways.map((g) => (
                <span key={g.family + g.address} className="block font-mono">
                  {g.address || t('gatewayOnLink')}
                </span>
              ))}
            </Row>
            <Row label={t('dnsServers')}>
              {dns.length === 0 ? (
                <span className="text-faint">{t('none')}</span>
              ) : (
                dns.map((d) => (
                  <span key={d} className="block font-mono">
                    {d}
                  </span>
                ))
              )}
            </Row>
          </>
        )}
        <Row label={t('mac')}>{it.mac ? <span className="font-mono">{it.mac}</span> : <span className="text-faint">{t('none')}</span>}</Row>
        <Row label={t('speed')}>{it.speed_mbps != null ? speed : <span className="text-faint">{speed}</span>}</Row>
        <Row label={t('carrier')}>
          {it.carrier == null ? <span className="text-faint">{t('unknown')}</span> : it.carrier ? t('carrierYes') : t('carrierNo')}
        </Row>
        <Row label={t('linkUptime')}>
          {it.link_up_seconds != null ? formatDuration(it.link_up_seconds) : <span className="text-faint">{t('linkUptimeUnknown')}</span>}
        </Row>
        <Row label={t('mtu')}>{count(it.mtu)}</Row>
        <Row label={t('driver')}>{it.driver ?? <span className="text-faint">{t('unknown')}</span>}</Row>
        {it.master && <Row label={t('master')}>{it.master}</Row>}
      </dl>

      {it.stats ? (
        <div className="grid grid-cols-2 gap-2 border-t border-line pt-3 text-xs">
          <div>
            <p className="text-muted">{t('rx')}</p>
            <p className="text-sm font-semibold text-fg">{formatBytes(it.stats.rx_bytes)}</p>
            <p className="text-faint">{t('packets', { count: count(it.stats.rx_packets) })}</p>
          </div>
          <div>
            <p className="text-muted">{t('tx')}</p>
            <p className="text-sm font-semibold text-fg">{formatBytes(it.stats.tx_bytes)}</p>
            <p className="text-faint">{t('packets', { count: count(it.stats.tx_packets) })}</p>
          </div>
          <p className={cx('col-span-2', it.stats.rx_errors + it.stats.tx_errors > 0 ? 'text-warning' : 'text-faint')}>
            {t('errors')}: {t('errorsValue', { rx: count(it.stats.rx_errors), tx: count(it.stats.tx_errors) })}
          </p>
          <p className={cx('col-span-2', it.stats.rx_dropped + it.stats.tx_dropped > 0 ? 'text-warning' : 'text-faint')}>
            {t('dropped')}: {t('errorsValue', { rx: count(it.stats.rx_dropped), tx: count(it.stats.tx_dropped) })}
          </p>
        </div>
      ) : (
        <p className="border-t border-line pt-3 text-xs text-faint">{t('statsUnavailable')}</p>
      )}
    </Card>
  )
}

function NetplanViewer({ fileNames }: { fileNames: string[] }) {
  const [open, setOpen] = useState(false)
  const q = useQuery<NetplanView>('/network/netplan', { enabled: open })
  return (
    <Card>
      <CardHeader
        title={t('netplanTitle')}
        subtitle={t('netplanSubtitle')}
        icon={FileCode2}
        actions={
          open ? (
            <Button size="sm" icon={RefreshCw} onClick={() => void q.reload()} loading={q.fetching}>
              {t('refresh')}
            </Button>
          ) : undefined
        }
      />
      {!open ? (
        <div className="flex flex-col items-start gap-3">
          {fileNames.length > 0 && <p className="break-all font-mono text-xs text-muted">{fileNames.join(', ')}</p>}
          <Button icon={FileCode2} onClick={() => setOpen(true)}>
            {t('netplanShow')}
          </Button>
        </div>
      ) : q.loading ? (
        <LoadingState />
      ) : q.error ? (
        <ErrorState message={q.error} onRetry={() => void q.reload()} />
      ) : !q.data || !q.data.present ? (
        <EmptyState icon={FileCode2} title={t('netplanAbsent')} description={t('netplanAbsentDesc')} />
      ) : q.data.files.length === 0 ? (
        <EmptyState icon={FileCode2} title={t('netplanEmpty')} description={t('netplanEmptyDesc')} />
      ) : (
        <div className="flex flex-col gap-4">
          <p className="text-xs text-muted">{t('netplanReadOnly')}</p>
          {q.data.files.map((f) => (
            <div key={f.name} className="min-w-0">
              <div className="mb-1.5 flex flex-wrap items-center gap-2">
                <span className="break-all font-mono text-sm font-medium text-fg">/etc/netplan/{f.name}</span>
                {f.redacted > 0 && <Badge tone="warning">{t('netplanRedacted', { count: f.redacted })}</Badge>}
              </div>
              {f.error ? (
                <Alert tone="warning">{f.error}</Alert>
              ) : (
                <>
                  <p className="mb-1.5 text-xs text-faint">
                    {t('netplanMeta', { size: formatBytes(f.size), mode: f.mode, date: formatShortDateTime(f.modified) })}
                  </p>
                  <pre className="max-h-96 overflow-auto rounded-xl border border-line bg-surface p-3 font-mono text-xs leading-relaxed text-fg">
                    {f.content}
                  </pre>
                  {f.truncated && (
                    <Alert tone="warning" className="mt-2">
                      {t('netplanTruncated')}
                    </Alert>
                  )}
                </>
              )}
            </div>
          ))}
        </div>
      )}
    </Card>
  )
}

function reachView(l: Listener): { tone: Tone; label: string } {
  switch (l.scope) {
    case 'all':
      return { tone: 'warning', label: t('reachAll') }
    case 'loopback':
      return { tone: 'neutral', label: t('reachLoopback') }
    default:
      return { tone: 'accent', label: t('reachSpecific') }
  }
}

function ListeningPorts({ items }: { items: Listener[] }) {
  return (
    <Card>
      <CardHeader title={t('portsTitle')} subtitle={t('portsSubtitle')} icon={Router} />
      {items.length === 0 ? (
        <EmptyState icon={Router} title={t('portsEmpty')} description={t('portsEmptyDesc')} />
      ) : (
        <>
          <TableWrap>
            <table className={tableClass.table}>
              <thead>
                <tr>
                  <th className={tableClass.th}>{t('port')}</th>
                  <th className={tableClass.th}>{t('protocol')}</th>
                  <th className={tableClass.th}>{t('bindAddress')}</th>
                  <th className={tableClass.th}>{t('reach')}</th>
                  <th className={tableClass.th}>{t('owner')}</th>
                  <th className={tableClass.th}>{t('process')}</th>
                </tr>
              </thead>
              <tbody>
                {items.map((l) => {
                  const reach = reachView(l)
                  return (
                    <tr key={`${l.protocol}-${l.address}-${l.port}`} className={tableClass.row}>
                      <td className={cx(tableClass.td, 'font-mono font-semibold')}>{l.port}</td>
                      <td className={tableClass.td}>
                        <Badge tone={l.protocol === 'tcp' ? 'accent' : 'purple'}>{l.protocol.toUpperCase()}</Badge>
                      </td>
                      <td className={cx(tableClass.td, 'font-mono text-xs break-all')}>{l.address}</td>
                      <td className={tableClass.td}>
                        <Status tone={reach.tone}>{reach.label}</Status>
                      </td>
                      <td className={tableClass.td}>{l.user ?? (l.uid >= 0 ? `uid ${l.uid}` : <span className="text-faint">{t('unknown')}</span>)}</td>
                      <td className={tableClass.td}>{l.process ?? <span className="text-faint">{t('processUnknown')}</span>}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </TableWrap>
          <p className="mt-3 text-xs text-faint">{t('portsNote')}</p>
        </>
      )}
    </Card>
  )
}

export function NetworkTab() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const q = useQuery<Overview>('/network/overview')
  const [showVirtual, setShowVirtual] = useState(false)

  if (q.loading) {
    return (
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <Skeleton className="h-56" />
        <Skeleton className="h-56" />
        <Skeleton className="h-56" />
      </div>
    )
  }
  if (q.error || !q.data) {
    return (
      <Card>
        <ErrorState message={q.error ?? t('unknown')} onRetry={() => void q.reload()} />
      </Card>
    )
  }

  const d = q.data
  const primary = d.interfaces.filter((i) => i.group === 'primary')
  const virtual = d.interfaces.filter((i) => i.group !== 'primary')
  const v4 = d.gateways.filter((g) => g.family === 'ipv4')
  const v6 = d.gateways.filter((g) => g.family === 'ipv6')
  const usesNetplan = d.manager.kind === 'netplan-networkd' || d.manager.kind === 'netplan-networkmanager'

  return (
    <div className="flex flex-col gap-4">
      <div className="flex justify-end">
        <Button size="sm" icon={RefreshCw} onClick={() => void q.reload()} loading={q.fetching}>
          {t('refresh')}
        </Button>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader title={t('summaryTitle')} icon={Server} />
          <KeyValueList
            items={[
              { label: t('hostname'), value: d.hostname || <span className="text-faint">{t('unknown')}</span> },
              {
                label: t('manager'),
                value: d.manager.kind === 'unknown' ? <span className="text-faint">{t('unknown')}</span> : d.manager.label,
              },
              {
                label: t('managerEvidence'),
                value:
                  d.manager.evidence.length === 0 ? (
                    <span className="text-faint">{t('none')}</span>
                  ) : (
                    <ul className="flex flex-col gap-0.5 text-xs text-muted">
                      {d.manager.evidence.map((e) => (
                        <li key={e}>{e}</li>
                      ))}
                    </ul>
                  ),
              },
              {
                label: t('subnets'),
                value:
                  d.private_subnets.length === 0 ? (
                    <span className="text-faint">{t('subnetsNone')}</span>
                  ) : (
                    d.private_subnets.map((s) => (
                      <span key={s.cidr} className="block font-mono text-xs">
                        {s.cidr} <span className="font-sans text-faint">({s.interface})</span>
                      </span>
                    ))
                  ),
              },
            ]}
          />
        </Card>

        <Card>
          <CardHeader title={t('gatewayTitle')} icon={Globe} />
          <KeyValueList
            items={[
              {
                label: t('gatewayV4'),
                value:
                  v4.length === 0 ? (
                    <span className="text-faint">{t('gatewayNone')}</span>
                  ) : (
                    v4.map((g) => (
                      <span key={g.interface + g.address} className="block font-mono text-xs">
                        {gatewayText(g)}
                      </span>
                    ))
                  ),
              },
              {
                label: t('gatewayV6'),
                value:
                  v6.length === 0 ? (
                    <span className="text-faint">{t('gatewayNone')}</span>
                  ) : (
                    v6.map((g) => (
                      <span key={g.interface + g.address} className="block font-mono text-xs">
                        {gatewayText(g)}
                      </span>
                    ))
                  ),
              },
              {
                label: t('dnsServers'),
                value:
                  d.dns.servers.length === 0 ? (
                    <span className="text-faint">{t('dnsNone')}</span>
                  ) : (
                    d.dns.servers.map((s) => (
                      <span key={s} className="block font-mono text-xs">
                        {s}
                      </span>
                    ))
                  ),
              },
              {
                label: t('dnsSearch'),
                value: d.dns.search.length === 0 ? <span className="text-faint">{t('none')}</span> : d.dns.search.join(', '),
              },
              {
                label: t('dnsSource'),
                value: d.dns.source ? (
                  <span className="text-xs">
                    <span className="font-mono">{d.dns.source}</span>
                    {d.dns.systemd_resolved && <span className="block text-faint">{t('dnsResolved')}</span>}
                  </span>
                ) : (
                  <span className="text-faint">{t('unknown')}</span>
                ),
              },
            ]}
          />
        </Card>
      </div>

      <section aria-label={t('interfacesTitle')} className="flex flex-col gap-3">
        <h2 className="text-[15px] font-semibold text-fg">{t('interfacesTitle')}</h2>
        {d.interfaces.length === 0 ? (
          <Card>
            <EmptyState icon={Network} title={t('interfacesEmpty')} description={t('interfacesEmptyDesc')} />
          </Card>
        ) : (
          <>
            {primary.length > 0 && (
              <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
                {primary.map((it) => (
                  <InterfaceCard key={it.name} it={it} dns={d.dns.servers} />
                ))}
              </div>
            )}
            {virtual.length > 0 && (
              <>
                <button
                  type="button"
                  aria-expanded={showVirtual}
                  onClick={() => setShowVirtual((v) => !v)}
                  className="flex min-h-11 w-full items-center gap-2 rounded-xl border border-line bg-surface px-3 text-left text-sm text-muted transition-colors hover:border-line-strong hover:text-fg"
                >
                  {showVirtual ? <ChevronDown className="size-4 shrink-0" aria-hidden /> : <ChevronRight className="size-4 shrink-0" aria-hidden />}
                  <span className="min-w-0">
                    <span className="block font-medium">
                      {showVirtual ? t('virtualHide', { count: virtual.length }) : t('virtualShow', { count: virtual.length })}
                    </span>
                    <span className="block truncate text-xs text-faint">{t('virtualHint')}</span>
                  </span>
                </button>
                {showVirtual && (
                  <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
                    {virtual.map((it) => (
                      <InterfaceCard key={it.name} it={it} dns={d.dns.servers} />
                    ))}
                  </div>
                )}
              </>
            )}
          </>
        )}
      </section>

      {isAdmin && usesNetplan && <NetplanViewer fileNames={d.manager.netplan_files} />}

      <ListeningPorts items={d.listening} />
    </div>
  )
}
