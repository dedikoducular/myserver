import { useMemo, useState } from 'react'
import { Network, Trash2 } from 'lucide-react'
import { Badge, Card, ConfirmDialog, IconButton, TableWrap, tableClass } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { cx } from '@/lib/format'
import { t } from './strings'
import { matches } from './util'
import { useReloadOn, type DockerLive } from './live'
import { ListBody, ListToolbar, SearchBox, UsedBy } from './parts'
import type { DockerNetwork } from './types'

function Subnets({ n }: { n: DockerNetwork }) {
  if (n.subnets.length === 0) return <span className="text-faint">{t('none')}</span>
  return (
    <span className="flex flex-col gap-0.5 font-mono text-xs">
      {n.subnets.map((s) => (
        <span key={s.subnet + s.gateway} className="break-all">
          {s.subnet || t('none')}
          {s.gateway && <span className="text-faint"> → {s.gateway}</span>}
        </span>
      ))}
    </span>
  )
}

function attached(n: DockerNetwork): string[] {
  return n.containers.map((c) => (c.ip ? `${c.name} (${c.ip})` : c.name))
}

export function NetworksTab({ live }: { live: DockerLive }) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const q = useQuery<DockerNetwork[]>('/docker/networks')
  useReloadOn(live.revision.network + live.revision.container, q.reload)

  const [search, setSearch] = useState('')
  const [removing, setRemoving] = useState<DockerNetwork | null>(null)
  const [error, setError] = useState<string | null>(null)

  const all = useMemo(() => q.data ?? [], [q.data])
  const shown = useMemo(
    () => all.filter((n) => matches(search, n.name, n.driver, n.short_id, n.app, ...n.subnets.map((s) => s.subnet))),
    [all, search],
  )

  const badges = (n: DockerNetwork) => (
    <span className="flex flex-wrap gap-1">
      {n.builtin && <Badge tone="purple">{t('builtin')}</Badge>}
      {n.internal && <Badge tone="warning">{t('internal')}</Badge>}
      {n.app && <Badge tone="accent">{t('appBadge', { app: n.app })}</Badge>}
    </span>
  )

  const removeButton = (n: DockerNetwork) => {
    const blocked = n.builtin ? t('removeNetworkBuiltin') : n.in_use ? t('removeNetworkInUse') : null
    return (
      <IconButton
        icon={Trash2}
        tone="danger"
        label={blocked ?? t('actRemove')}
        disabled={blocked !== null}
        onClick={() => {
          setError(null)
          setRemoving(n)
        }}
      />
    )
  }

  return (
    <Card padded={false}>
      <ListToolbar>
        <SearchBox value={search} onChange={setSearch} label={t('searchNetworks')} />
      </ListToolbar>

      <ListBody
        query={q}
        total={all.length}
        shown={shown.length}
        emptyIcon={Network}
        emptyTitle={t('noNetworks')}
        emptyHint={t('noNetworksHint')}
      >
        <div className="hidden px-5 pb-3 md:block">
          <TableWrap>
            <table className={tableClass.table}>
              <thead>
                <tr>
                  <th className={tableClass.th}>{t('colName')}</th>
                  <th className={tableClass.th}>{t('colDriver')}</th>
                  <th className={tableClass.th}>{t('colScope')}</th>
                  <th className={tableClass.th}>{t('colSubnet')}</th>
                  <th className={tableClass.th}>{t('colContainers')}</th>
                  {isAdmin && <th className={cx(tableClass.th, 'text-right')}>{t('colActions')}</th>}
                </tr>
              </thead>
              <tbody>
                {shown.map((n) => (
                  <tr key={n.id} className={tableClass.row}>
                    <td className={tableClass.td}>
                      <div className="flex flex-col gap-1">
                        <span className="font-medium break-all">{n.name}</span>
                        <span className="font-mono text-xs text-faint">{n.short_id}</span>
                        {badges(n)}
                      </div>
                    </td>
                    <td className={cx(tableClass.td, 'text-muted')}>{n.driver || t('none')}</td>
                    <td className={cx(tableClass.td, 'text-muted')}>{n.scope}</td>
                    <td className={tableClass.td}>
                      <Subnets n={n} />
                    </td>
                    <td className={cx(tableClass.td, 'text-xs')}>
                      <UsedBy names={attached(n)} />
                    </td>
                    {isAdmin && <td className={cx(tableClass.td, 'text-right')}>{removeButton(n)}</td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
        </div>

        <ul className="flex flex-col gap-3 p-4 pt-0 md:hidden">
          {shown.map((n) => (
            <li key={n.id} className="flex items-start justify-between gap-2 rounded-xl border border-line bg-surface p-3">
              <div className="flex min-w-0 flex-col gap-1.5">
                <span className="font-medium break-all text-fg">{n.name}</span>
                {badges(n)}
                <p className="text-xs text-muted">
                  {n.driver || t('none')} · {n.scope}
                </p>
                <Subnets n={n} />
                {n.in_use && (
                  <p className="text-xs text-muted">
                    {t('colContainers')}: <UsedBy names={attached(n)} />
                  </p>
                )}
              </div>
              {isAdmin && removeButton(n)}
            </li>
          ))}
        </ul>
      </ListBody>

      {removing && (
        <ConfirmDialog
          open
          danger
          onClose={() => setRemoving(null)}
          title={t('removeNetworkTitle')}
          message={t('removeNetworkMessage', { name: removing.name })}
          confirmLabel={t('actRemove')}
          cancelLabel={t('cancel')}
          error={error}
          onConfirm={async () => {
            setError(null)
            try {
              await api.del(`/docker/networks/${encodeURIComponent(removing.id)}`)
              toast.success(t('removeNetworkDone'))
              setRemoving(null)
              void q.reload()
            } catch (e) {
              setError(errorMessage(e))
            }
          }}
        />
      )}
    </Card>
  )
}
