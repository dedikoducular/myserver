import type { ReactNode } from 'react'
import { Alert, Badge, ErrorState, KeyValueList, LoadingState, Modal, Status } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { formatBytes, formatDateTime } from '@/lib/format'
import { t } from './strings'
import { portLabels, stateInfo } from './util'
import type { ContainerDetail } from './types'

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-2.5">
      <h3 className="text-xs font-semibold tracking-wide text-faint uppercase">{title}</h3>
      {children}
    </section>
  )
}

function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-xs break-all">{children}</span>
}

function Empty() {
  return <p className="text-xs text-faint">{t('emptySection')}</p>
}

function healthText(h: string | null): string {
  switch (h) {
    case 'healthy':
      return t('healthHealthy')
    case 'unhealthy':
      return t('healthUnhealthy')
    case 'starting':
      return t('healthStarting')
    default:
      return t('healthNone')
  }
}

function joinArgs(args: string[]): ReactNode {
  return args.length > 0 ? <Mono>{args.join(' ')}</Mono> : t('none')
}

function Detail({ d }: { d: ContainerDetail }) {
  const state = stateInfo({ state: d.state, exit_code: d.exit_code })
  const ports = portLabels(d.ports)
  const labels = Object.entries(d.labels).sort(([a], [b]) => a.localeCompare(b))
  return (
    <div className="flex flex-col gap-6">
      <Alert tone="accent">{t('maskedNote')}</Alert>

      <Section title={t('secGeneral')}>
        <KeyValueList
          items={[
            { label: t('fId'), value: <Mono>{d.id}</Mono> },
            { label: t('fImage'), value: <Mono>{d.image}</Mono> },
            ...(d.app ? [{ label: t('fApp'), value: <Badge tone="accent">{d.app}</Badge> }] : []),
            {
              label: t('fState'),
              value: (
                <Status tone={state.tone} pulse={state.pulse}>
                  {state.label}
                </Status>
              ),
            },
            ...(d.running ? [] : [{ label: t('fExitCode'), value: String(d.exit_code) }]),
            ...(d.oom_killed ? [{ label: t('fOom'), value: <Badge tone="danger">{t('yes')}</Badge> }] : []),
            { label: t('fHealth'), value: healthText(d.health) },
            { label: t('fCreated'), value: formatDateTime(d.created_at) },
            { label: t('fStarted'), value: formatDateTime(d.started_at) },
            ...(d.running ? [] : [{ label: t('fFinished'), value: formatDateTime(d.finished_at) }]),
            { label: t('fPlatform'), value: d.platform || t('none') },
          ]}
        />
      </Section>

      <Section title={t('secRuntime')}>
        <KeyValueList
          items={[
            { label: t('fEntrypoint'), value: joinArgs(d.entrypoint) },
            { label: t('fCommand'), value: joinArgs(d.command) },
            { label: t('fWorkdir'), value: d.working_dir ? <Mono>{d.working_dir}</Mono> : t('none') },
            { label: t('fUser'), value: d.user || 'root' },
            { label: t('fHostname'), value: d.hostname || t('none') },
            { label: t('fRestartPolicy'), value: d.restart_policy || 'no' },
            { label: t('fRestartCount'), value: String(d.restart_count) },
            { label: t('fNetworkMode'), value: d.network_mode || t('none') },
            { label: t('fMemoryLimit'), value: d.memory_limit > 0 ? formatBytes(d.memory_limit) : t('unlimited') },
            {
              label: t('fCpuLimit'),
              value:
                d.nano_cpus > 0
                  ? t('cpuCores', { n: (d.nano_cpus / 1e9).toLocaleString('tr-TR', { maximumFractionDigits: 2 }) })
                  : t('unlimited'),
            },
            {
              label: t('fPrivileged'),
              value: d.privileged ? <Badge tone="warning">{t('yes')}</Badge> : t('no'),
            },
            { label: t('fTty'), value: d.tty ? t('yes') : t('no') },
          ]}
        />
      </Section>

      <Section title={t('secNetworks')}>
        {d.networks.length === 0 ? (
          <Empty />
        ) : (
          <ul className="flex flex-col gap-2">
            {d.networks.map((n) => (
              <li key={n.network} className="rounded-xl border border-line bg-surface px-3 py-2 text-sm">
                <p className="font-medium text-fg">{n.network}</p>
                <p className="mt-0.5 text-xs text-muted">
                  <Mono>{[n.ip_address, n.ipv6_address].filter(Boolean).join(' · ') || t('none')}</Mono>
                  {n.gateway && (
                    <>
                      {' · '}
                      {t('gateway')}: <Mono>{n.gateway}</Mono>
                    </>
                  )}
                  {n.mac_address && (
                    <>
                      {' · '}
                      <Mono>{n.mac_address}</Mono>
                    </>
                  )}
                </p>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title={t('secPorts')}>
        {ports.length === 0 ? (
          <Empty />
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {ports.map((p) => (
              <Badge key={p} tone="cyan" className="font-mono">
                {p}
              </Badge>
            ))}
          </div>
        )}
      </Section>

      <Section title={t('secMounts')}>
        {d.mounts.length === 0 ? (
          <Empty />
        ) : (
          <ul className="flex flex-col gap-2">
            {d.mounts.map((m) => (
              <li key={m.destination} className="rounded-xl border border-line bg-surface px-3 py-2">
                <div className="flex flex-wrap items-center gap-1.5">
                  <Badge>{m.type}</Badge>
                  {m.read_only && <Badge tone="warning">{t('readOnly')}</Badge>}
                  <Mono>{m.destination}</Mono>
                </div>
                <p className="mt-1 text-xs text-muted">
                  <Mono>{m.name || m.source || t('none')}</Mono>
                </p>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title={t('secEnv')}>
        {d.env.length === 0 ? (
          <Empty />
        ) : (
          <ul className="flex flex-col divide-y divide-line/60 rounded-xl border border-line bg-surface">
            {d.env.map((e) => (
              <li key={e.name} className="flex flex-col gap-0.5 px-3 py-2 sm:flex-row sm:items-baseline sm:gap-3">
                <span className="font-mono text-xs font-medium break-all text-fg sm:w-2/5 sm:shrink-0">{e.name}</span>
                <span className="min-w-0 font-mono text-xs break-all text-muted">
                  {e.masked && (
                    <Badge tone="warning" className="mr-1.5 font-sans">
                      {t('maskedBadge')}
                    </Badge>
                  )}
                  {e.value || t('none')}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section title={t('secLabels')}>
        {labels.length === 0 ? (
          <Empty />
        ) : (
          <ul className="flex flex-col divide-y divide-line/60 rounded-xl border border-line bg-surface">
            {labels.map(([k, v]) => (
              <li key={k} className="flex flex-col gap-0.5 px-3 py-2 sm:flex-row sm:items-baseline sm:gap-3">
                <span className="font-mono text-xs font-medium break-all text-fg sm:w-2/5 sm:shrink-0">{k}</span>
                <span className="min-w-0 font-mono text-xs break-all text-muted">{v || t('none')}</span>
              </li>
            ))}
          </ul>
        )}
      </Section>
    </div>
  )
}

export function InspectModal({ container, onClose }: { container: { id: string; name: string }; onClose: () => void }) {
  const q = useQuery<ContainerDetail>(`/docker/containers/${encodeURIComponent(container.id)}`)
  return (
    <Modal open onClose={onClose} title={t('inspectTitle', { name: container.name })} size="lg">
      {q.loading ? (
        <LoadingState />
      ) : q.error ? (
        <ErrorState message={q.error} onRetry={() => void q.reload()} />
      ) : q.data ? (
        <Detail d={q.data} />
      ) : null}
    </Modal>
  )
}
