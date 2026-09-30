import { useId, type ReactNode } from 'react'
import { SearchX, type LucideIcon } from 'lucide-react'
import { Alert, EmptyState, ErrorState, Input, Skeleton } from '@/components/ui'
import type { QueryState } from '@/hooks/useApi'
import { t } from './strings'

export function SearchBox({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  const id = useId()
  return (
    <div className="w-full sm:w-72">
      <label htmlFor={id} className="sr-only">
        {label}
      </label>
      <Input id={id} type="search" value={value} onChange={(e) => onChange(e.target.value)} placeholder={label} autoComplete="off" />
    </div>
  )
}

/** Toolbar of a list card: search on the left, actions on the right. */
export function ListToolbar({ children, actions }: { children: ReactNode; actions?: ReactNode }) {
  return (
    <div className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between sm:p-5">
      {children}
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

/** Renders the loading, error, empty and no-match states of a list, or the
 *  list itself. */
export function ListBody<T>({
  query,
  total,
  shown,
  emptyIcon,
  emptyTitle,
  emptyHint,
  children,
}: {
  query: QueryState<T>
  total: number
  shown: number
  emptyIcon: LucideIcon
  emptyTitle: string
  emptyHint: string
  children: ReactNode
}) {
  if (query.loading) {
    return (
      <div className="flex flex-col gap-3 p-4 pt-0 sm:p-5 sm:pt-0" aria-hidden>
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} className="h-12 w-full" />
        ))}
      </div>
    )
  }
  if (query.error && total === 0) return <ErrorState message={query.error} onRetry={() => void query.reload()} />
  if (total === 0) return <EmptyState icon={emptyIcon} title={emptyTitle} description={emptyHint} />
  return (
    <>
      {query.error && (
        <div className="px-4 pb-3 sm:px-5">
          <Alert tone="danger">{query.error}</Alert>
        </div>
      )}
      {shown === 0 ? <EmptyState icon={SearchX} title={t('noMatch')} description={t('noMatchHint')} /> : children}
    </>
  )
}

/** Names of the containers that use an object, shortened. */
export function UsedBy({ names }: { names: string[] }) {
  if (names.length === 0) return <span className="text-faint">{t('none')}</span>
  const head = names.slice(0, 3).join(', ')
  return (
    <span className="break-words" title={names.join(', ')}>
      {head}
      {names.length > 3 && <span className="text-faint"> {t('morePorts', { n: names.length - 3 })}</span>}
    </span>
  )
}
