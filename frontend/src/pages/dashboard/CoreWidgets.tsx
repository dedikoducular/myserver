import { Link } from 'react-router-dom'
import { Container, Download, FolderOpen, Plus, RefreshCw, ScrollText, SquareTerminal, type LucideIcon } from 'lucide-react'
import { Card, CardHeader, EmptyState, ErrorState, IconTile, LoadingState, type Tone } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { messages } from '@/i18n'
import { useAuth } from '@/stores/auth'
import type { LogEntry } from '@/types/api'
import { LogList } from '../settings/Advanced'

const t = messages({
  tr: {
    quick: 'Hızlı İşlemler',
    installApp: 'Uygulama Kur',
    docker: 'Docker Yönetimi',
    files: 'Dosya Yöneticisi',
    terminal: 'Terminal Aç',
    update: 'Sistem Güncelle',
    backup: 'Yedekleme',
    logs: 'Son Loglar',
    seeAll: 'Tümünü Gör',
    noLogs: 'Henüz kayıt yok',
  },
})

interface QuickAction {
  label: string
  to: string
  icon: LucideIcon
  tone: Tone
  adminOnly?: boolean
}

const actions: QuickAction[] = [
  { label: t('installApp'), to: '/apps?view=store', icon: Plus, tone: 'accent' },
  { label: t('docker'), to: '/docker', icon: Container, tone: 'cyan' },
  { label: t('files'), to: '/files', icon: FolderOpen, tone: 'warning' },
  { label: t('terminal'), to: '/terminal', icon: SquareTerminal, tone: 'neutral', adminOnly: true },
  { label: t('update'), to: '/updates', icon: RefreshCw, tone: 'success' },
  { label: t('backup'), to: '/backup', icon: Download, tone: 'success', adminOnly: true },
]

export function seeAllClass(): string {
  return 'text-xs font-medium text-accent hover:underline'
}

export function QuickActionsWidget() {
  const isAdmin = useAuth((s) => s.isAdmin)
  return (
    <Card>
      <CardHeader title={t('quick')} />
      <ul className="grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-1">
        {actions
          .filter((a) => !a.adminOnly || isAdmin)
          .map((a) => (
            <li key={a.to}>
              <Link
                to={a.to}
                className="flex min-h-14 items-center gap-3 rounded-xl border border-line bg-surface px-3 py-2 text-sm font-medium text-fg transition-colors hover:border-line-strong hover:bg-raised"
              >
                <IconTile icon={a.icon} tone={a.tone} size="sm" />
                <span className="min-w-0 flex-1 truncate">{a.label}</span>
              </Link>
            </li>
          ))}
      </ul>
    </Card>
  )
}

/** The panel's own recent log lines; admins only, since the endpoint is. */
export function RecentLogsWidget() {
  const { data, error, loading, reload } = useQuery<LogEntry[]>('/logs', { query: { limit: 8 }, refetchInterval: 30_000 })
  return (
    <Card>
      <CardHeader
        title={t('logs')}
        actions={
          <Link to="/settings?section=advanced" className={seeAllClass()}>
            {t('seeAll')}
          </Link>
        }
      />
      {loading ? (
        <LoadingState className="py-6" />
      ) : error ? (
        <ErrorState message={error} onRetry={() => void reload()} className="py-4" />
      ) : !data || data.length === 0 ? (
        <EmptyState icon={ScrollText} title={t('noLogs')} className="py-6" />
      ) : (
        <LogList entries={data} compact />
      )}
    </Card>
  )
}
