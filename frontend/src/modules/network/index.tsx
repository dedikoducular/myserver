import { Link, useSearchParams } from 'react-router-dom'
import { ArrowRight, Network, Shield } from 'lucide-react'
import { Card, CardHeader, ErrorState, KeyValueList, PageHeader, Skeleton, Status, Tabs } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { FirewallTab } from './FirewallTab'
import { NetworkTab } from './NetworkTab'
import { t } from './strings'
import type { FirewallState, Overview } from './types'

type TabId = 'network' | 'firewall'

/** The /network page. The firewall tab is addressed as /network?tab=firewall. */
export default function NetworkPage() {
  const [params, setParams] = useSearchParams()
  const tab: TabId = params.get('tab') === 'firewall' ? 'firewall' : 'network'
  return (
    <div>
      <PageHeader title={t('pageTitle')} description={t('pageDescription')} icon={Network} />
      <Tabs<TabId>
        label={t('tabsLabel')}
        className="mb-4 w-fit"
        value={tab}
        onChange={(id) => setParams(id === 'firewall' ? { tab: 'firewall' } : {}, { replace: true })}
        items={[
          { id: 'network', label: t('tabNetwork') },
          { id: 'firewall', label: t('tabFirewall') },
        ]}
      />
      {tab === 'network' ? <NetworkTab /> : <FirewallTab />}
    </div>
  )
}

const linkClass =
  'inline-flex min-h-10 items-center gap-1.5 rounded-lg text-sm font-medium text-accent transition-colors hover:text-cyan'

/** Read-only network summary for the settings page. */
export function NetworkSettingsSection() {
  const q = useQuery<Overview>('/network/overview')
  return (
    <Card>
      <CardHeader title={t('setNetworkTitle')} icon={Network} />
      {q.loading ? (
        <Skeleton className="h-24" />
      ) : q.error || !q.data ? (
        <ErrorState className="py-4" message={q.error ?? t('unknown')} onRetry={() => void q.reload()} />
      ) : (
        <KeyValueList
          items={[
            { label: t('hostname'), value: q.data.hostname || <span className="text-faint">{t('unknown')}</span> },
            {
              label: t('manager'),
              value: q.data.manager.kind === 'unknown' ? <span className="text-faint">{t('unknown')}</span> : q.data.manager.label,
            },
            {
              label: t('dnsServers'),
              value:
                q.data.dns.servers.length === 0 ? (
                  <span className="text-faint">{t('dnsNone')}</span>
                ) : (
                  <span className="font-mono text-xs">{q.data.dns.servers.join(', ')}</span>
                ),
            },
          ]}
        />
      )}
      <Link to="/network" className={`${linkClass} mt-3`}>
        {t('setNetworkLink')}
        <ArrowRight className="size-4" aria-hidden />
      </Link>
    </Card>
  )
}

/** Firewall summary for the settings page's security section. */
export function FirewallSettingsSection() {
  const q = useQuery<FirewallState>('/firewall/status')
  const st = q.data
  return (
    <Card>
      <CardHeader title={t('setFirewallTitle')} icon={Shield} />
      {q.loading ? (
        <Skeleton className="h-24" />
      ) : q.error || !st ? (
        <ErrorState className="py-4" message={q.error ?? t('unknown')} onRetry={() => void q.reload()} />
      ) : !st.installed ? (
        <div className="flex flex-col gap-2 text-sm">
          <Status tone="neutral">{t('fwNotInstalled')}</Status>
          <code className="w-fit max-w-full overflow-x-auto rounded-lg border border-line bg-surface px-2 py-1.5 font-mono text-xs text-fg">
            {st.install_hint}
          </code>
        </div>
      ) : (
        <KeyValueList
          items={[
            {
              label: t('setFirewallStatus'),
              value: <Status tone={st.active ? 'success' : 'warning'}>{st.active ? t('fwActive') : t('fwInactive')}</Status>,
            },
            {
              label: t('setFirewallIncoming'),
              value:
                st.defaults.incoming === 'allow'
                  ? t('policyAllow')
                  : st.defaults.incoming === 'deny'
                    ? t('policyDeny')
                    : st.defaults.incoming === 'reject'
                      ? t('policyReject')
                      : t('unknown'),
            },
            { label: t('setFirewallRules'), value: st.rules.length.toLocaleString('tr-TR') },
          ]}
        />
      )}
      <Link to="/network?tab=firewall" className={`${linkClass} mt-3`}>
        {t('setFirewallLink')}
        <ArrowRight className="size-4" aria-hidden />
      </Link>
    </Card>
  )
}
