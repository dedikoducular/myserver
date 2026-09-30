import { Blocks, Info, RefreshCw, ScrollText } from 'lucide-react'
import { Badge, Button, Card, CardHeader, EmptyState, ErrorState, KeyValueList, LoadingState, Status } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { cx, formatShortDateTime } from '@/lib/format'
import { messages } from '@/i18n'
import { useAuth } from '@/stores/auth'
import type { LogEntry, ModuleState } from '@/types/api'

const t = messages({
  tr: {
    aboutTitle: 'Hakkında',
    version: 'Panel sürümü',
    language: 'Dil',
    turkish: 'Türkçe',
    modulesTitle: 'Modüller',
    modulesSubtitle: 'Bir modül yüklenemese bile panelin diğer bölümleri çalışmaya devam eder',
    loaded: 'Çalışıyor',
    failed: 'Yüklenemedi',
    noModules: 'Yüklü modül yok',
    logsTitle: 'Panel Kayıtları',
    logsSubtitle: 'Panelin kendi son kayıtları. Parola ve anahtar gibi gizli değerler kayıtlara yazılmaz.',
    noLogs: 'Henüz kayıt yok',
    refresh: 'Yenile',
  },
})

const levelTone = { INFO: 'accent', WARN: 'warning', ERROR: 'danger' } as const

/** Attributes worth showing next to the message in the compact view. */
const SUMMARY_KEYS = ['user', 'action', 'target', 'module', 'path', 'code', 'device', 'unit', 'app', 'container']

function summarize(attrs: Record<string, string> | undefined): string {
  if (!attrs) return ''
  return SUMMARY_KEYS.map((k) => attrs[k])
    .filter((v): v is string => Boolean(v))
    .join(' · ')
}

export function LogList({ entries, compact = false }: { entries: LogEntry[]; compact?: boolean }) {
  return (
    <ul className={cx('flex flex-col', compact ? 'gap-1.5' : 'gap-2')}>
      {entries.map((e, i) => (
        <li key={`${e.time}-${i}`} className="flex items-start gap-2.5 text-xs">
          <span className="w-10 shrink-0 pt-0.5 font-mono text-faint" title={formatShortDateTime(e.time)}>
            {new Date(e.time).toLocaleTimeString('tr-TR', { hour: '2-digit', minute: '2-digit' })}
          </span>
          <Badge tone={levelTone[e.level] ?? 'neutral'} className="w-12 shrink-0 justify-center font-mono">
            {e.level}
          </Badge>
          <span className="min-w-0 flex-1 break-words text-muted">
            <span className="text-fg">{e.message}</span>
            {compact && summarize(e.attrs) && <span className="ml-1.5 text-faint">{summarize(e.attrs)}</span>}
            {!compact &&
              e.attrs &&
              Object.entries(e.attrs).map(([k, v]) => (
                <span key={k} className="ml-2 font-mono text-[11px] text-faint">
                  {k}={v}
                </span>
              ))}
          </span>
        </li>
      ))}
    </ul>
  )
}

export function AdvancedSection() {
  const version = useAuth((s) => s.status?.version)
  const modules = useQuery<ModuleState[]>('/modules')
  const logs = useQuery<LogEntry[]>('/logs', { query: { limit: 200 } })
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader title={t('aboutTitle')} icon={Info} />
        <KeyValueList
          items={[
            { label: t('version'), value: <span className="font-mono">{version}</span> },
            { label: t('language'), value: t('turkish') },
          ]}
        />
      </Card>
      <Card>
        <CardHeader title={t('modulesTitle')} subtitle={t('modulesSubtitle')} icon={Blocks} />
        {modules.loading ? (
          <LoadingState />
        ) : modules.error ? (
          <ErrorState message={modules.error} onRetry={() => void modules.reload()} />
        ) : !modules.data || modules.data.length === 0 ? (
          <EmptyState icon={Blocks} title={t('noModules')} />
        ) : (
          <ul className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
            {modules.data.map((m) => (
              <li key={m.name} className="flex items-center justify-between gap-3 rounded-xl border border-line bg-surface px-3 py-2.5">
                <span className="truncate font-mono text-sm text-fg">{m.name}</span>
                <Status tone={m.loaded ? 'success' : 'danger'}>{m.loaded ? t('loaded') : t('failed')}</Status>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <Card>
        <CardHeader
          title={t('logsTitle')}
          subtitle={t('logsSubtitle')}
          icon={ScrollText}
          actions={
            <Button size="sm" icon={RefreshCw} loading={logs.fetching} onClick={() => void logs.reload()}>
              {t('refresh')}
            </Button>
          }
        />
        {logs.loading ? (
          <LoadingState />
        ) : logs.error ? (
          <ErrorState message={logs.error} onRetry={() => void logs.reload()} />
        ) : !logs.data || logs.data.length === 0 ? (
          <EmptyState icon={ScrollText} title={t('noLogs')} />
        ) : (
          <div className="max-h-[480px] overflow-y-auto pr-1">
            <LogList entries={logs.data} />
          </div>
        )}
      </Card>
    </div>
  )
}
