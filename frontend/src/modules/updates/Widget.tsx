import { Link, useNavigate } from 'react-router-dom'
import { Box, Container, ListChecks, Package, type LucideIcon } from 'lucide-react'
import { Badge, Button, Card, CardHeader, ErrorState, IconTile, Skeleton, type Tone } from '@/components/ui'
import { cx } from '@/lib/format'
import { t } from './strings'
import { useSummary } from './summary'
import type { Summary } from './types'

interface Row {
  icon: LucideIcon
  iconTone: Tone
  name: string
  text: string
  tone: 'success' | 'warning' | 'muted'
}

const textTone = { success: 'text-success', warning: 'text-warning', muted: 'text-muted' }

function areaText(a: { count: number; checked_at: number | null; error: unknown }): Pick<Row, 'text' | 'tone'> {
  if (a.count > 0) return { text: t('updatesCount', { count: a.count }), tone: 'warning' }
  if (a.checked_at === null) return { text: t('notChecked'), tone: 'muted' }
  if (a.error) return { text: t('checkFailed'), tone: 'muted' }
  return { text: t('upToDate'), tone: 'success' }
}

function rows(s: Summary): Row[] {
  let self: Pick<Row, 'text' | 'tone'>
  const v = s.self.installed_version
  if (s.self.count > 0) self = { text: t('vAvailable', { version: s.self.latest_version }), tone: 'warning' }
  else if (s.self.configured && s.self.checked_at !== null && !s.self.error) self = { text: t('vCurrent', { version: v }), tone: 'success' }
  else self = { text: t('vOnly', { version: v }), tone: 'muted' }
  return [
    { icon: Package, iconTone: 'warning', name: t('aptTitle'), ...areaText(s.apt) },
    { icon: Container, iconTone: 'accent', name: t('widgetDocker'), ...areaText(s.docker) },
    { icon: Box, iconTone: 'cyan', name: t('selfWidgetName'), ...self },
  ]
}

export function UpdatesWidget() {
  const { data, error, loading, reload } = useSummary()
  const navigate = useNavigate()

  return (
    <Card>
      <CardHeader
        title={
          <span className="inline-flex items-center gap-2">
            {t('widgetTitle')}
            {data && data.total > 0 && <Badge tone="success">{data.total}</Badge>}
          </span>
        }
        actions={
          <Link to="/updates" className="text-xs font-medium text-accent hover:underline">
            {t('viewAll')}
          </Link>
        }
      />
      {loading ? (
        <div className="flex flex-col gap-2">
          <Skeleton className="h-11" />
          <Skeleton className="h-11" />
          <Skeleton className="h-11" />
        </div>
      ) : !data ? (
        <ErrorState className="py-4" message={error ?? ''} onRetry={() => void reload()} />
      ) : (
        <div className="flex flex-col gap-2">
          {rows(data).map((r) => (
            <div key={r.name} className="flex items-center gap-3 rounded-xl border border-line bg-surface px-3 py-2">
              <IconTile icon={r.icon} tone={r.iconTone} size="sm" />
              <span className="min-w-0 flex-1 truncate text-sm text-fg">{r.name}</span>
              <span className={cx('shrink-0 text-xs font-medium', textTone[r.tone])}>{r.text}</span>
            </div>
          ))}
          {data.apt.reboot_required && <p className="text-xs text-warning">{t('rebootShort')}</p>}
          <Button
            variant="primary"
            block
            icon={ListChecks}
            className="mt-1"
            disabled={data.total === 0}
            onClick={() => navigate('/updates')}
          >
            {data.total === 0 ? t('nothingToUpdate') : t('review')}
          </Button>
        </div>
      )}
    </Card>
  )
}
