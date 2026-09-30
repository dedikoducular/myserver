import { Link } from 'react-router-dom'
import { ChevronRight, ServerCog } from 'lucide-react'
import { Card, CardHeader, EmptyState, ErrorState, Skeleton, StatusDot } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { cx } from '@/lib/format'
import { t } from './strings'
import type { Service } from './types'
import { stateLabel, stateTone } from './util'

const toneText = {
  neutral: 'text-muted',
  accent: 'text-accent',
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
  purple: 'text-purple',
  cyan: 'text-cyan',
} as const

/** The "Servis Durumları" dashboard card. */
export function ServicesWidget() {
  const q = useQuery<Service[]>('/services/featured', { refetchInterval: 30000 })
  return (
    <Card>
      <CardHeader
        title={t('widget_title')}
        actions={
          <Link to="/services" className="text-xs font-medium text-accent hover:text-cyan">
            {t('widget_all')}
          </Link>
        }
      />
      {q.loading ? (
        <div className="flex flex-col gap-3" role="status" aria-label={t('widget_title')}>
          {[0, 1, 2, 3, 4].map((i) => (
            <Skeleton key={i} className="h-5 w-full" />
          ))}
        </div>
      ) : !q.data ? (
        <ErrorState className="py-4" message={q.error ?? ''} onRetry={() => void q.reload()} />
      ) : q.data.length === 0 ? (
        <EmptyState className="py-4" icon={ServerCog} title={t('featured_empty')} description={t('featured_empty_hint')} />
      ) : (
        <ul className="-mx-2 flex flex-col">
          {q.data.map((s) => {
            const tone = stateTone(s)
            const label = stateLabel(s.state)
            return (
              <li key={s.unit}>
                <Link
                  to="/services"
                  aria-label={t('widget_open', { name: s.name, state: label })}
                  className="flex min-h-10 items-center gap-2.5 rounded-lg px-2 text-sm transition-colors hover:bg-raised"
                >
                  <StatusDot tone={tone} pulse={s.state === 'activating'} />
                  <span className="min-w-0 flex-1 truncate text-fg">{s.name}</span>
                  <span className={cx('shrink-0 text-xs font-medium', toneText[tone])}>{label}</span>
                  <ChevronRight className="size-4 shrink-0 text-faint" aria-hidden />
                </Link>
              </li>
            )
          })}
        </ul>
      )}
    </Card>
  )
}
