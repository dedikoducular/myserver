import { useState } from 'react'
import { AlertOctagon, AlertTriangle, Bell, CheckCheck, CheckCircle2, Info, Trash2, XCircle, type LucideIcon } from 'lucide-react'
import { Badge, Button, Card, CardHeader, ConfirmDialog, EmptyState, ErrorState, IconButton, LoadingState, Tabs, type Tone } from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { cx, formatShortDateTime } from '@/lib/format'
import { messages } from '@/i18n'
import { api } from '@/services/api'
import { toast } from '@/stores/ui'
import type { Notification, NotificationList, Severity } from '@/types/api'

const t = messages({
  tr: {
    title: 'Bildirim Merkezi',
    subtitle: 'Sistem olayları ve uyarıları',
    all: 'Tümü',
    unread: 'Okunmamış',
    markAll: 'Tümünü Okundu İşaretle',
    clear: 'Tümünü Sil',
    clearTitle: 'Tüm bildirimler silinsin mi?',
    clearText: 'Bildirim geçmişi kalıcı olarak silinecek.',
    cleared: 'Bildirimler silindi.',
    empty: 'Bildirim yok',
    emptyUnread: 'Okunmamış bildirim yok',
    remove: 'Bildirimi sil',
    markRead: 'Okundu işaretle',
    INFO: 'Bilgi',
    SUCCESS: 'Başarılı',
    WARNING: 'Uyarı',
    ERROR: 'Hata',
    CRITICAL: 'Kritik',
  },
})

const style: Record<Severity, { icon: LucideIcon; tone: Tone; text: string }> = {
  INFO: { icon: Info, tone: 'accent', text: 'text-accent' },
  SUCCESS: { icon: CheckCircle2, tone: 'success', text: 'text-success' },
  WARNING: { icon: AlertTriangle, tone: 'warning', text: 'text-warning' },
  ERROR: { icon: XCircle, tone: 'danger', text: 'text-danger' },
  CRITICAL: { icon: AlertOctagon, tone: 'danger', text: 'text-danger' },
}

export function NotificationsSection() {
  const [filter, setFilter] = useState<'all' | 'unread'>('all')
  const { data, error, loading, reload } = useQuery<NotificationList>('/notifications', {
    query: { limit: 200, unread: filter === 'unread' ? true : undefined },
  })
  const [confirm, setConfirm] = useState(false)

  const onError = (m: string) => toast.error(m)
  const markAll = useAction(() => api.post('/notifications/read-all'), { onSuccess: () => void reload(), onError })
  const markOne = useAction((n: Notification) => api.post(`/notifications/${n.id}/read`), { onSuccess: () => void reload(), onError })
  const removeOne = useAction((n: Notification) => api.del(`/notifications/${n.id}`), { onSuccess: () => void reload(), onError })
  const clear = useAction(() => api.del('/notifications'), {
    onSuccess: () => {
      toast.success(t('cleared'))
      setConfirm(false)
      void reload()
    },
  })

  const items = data?.items ?? []
  return (
    <Card>
      <CardHeader
        title={t('title')}
        subtitle={t('subtitle')}
        icon={Bell}
        actions={
          <>
            <Button size="sm" icon={CheckCheck} disabled={!data || data.unread === 0} loading={markAll.pending} onClick={() => void markAll.run()}>
              <span className="hidden sm:inline">{t('markAll')}</span>
            </Button>
            <Button size="sm" variant="danger" icon={Trash2} disabled={items.length === 0} onClick={() => setConfirm(true)}>
              <span className="hidden sm:inline">{t('clear')}</span>
            </Button>
          </>
        }
      />
      <Tabs
        label={t('title')}
        className="mb-3 w-fit"
        value={filter}
        onChange={setFilter}
        items={[
          { id: 'all', label: t('all') },
          { id: 'unread', label: t('unread'), count: data?.unread },
        ]}
      />
      {loading ? (
        <LoadingState />
      ) : error ? (
        <ErrorState message={error} onRetry={() => void reload()} />
      ) : items.length === 0 ? (
        <EmptyState icon={Bell} title={filter === 'unread' ? t('emptyUnread') : t('empty')} />
      ) : (
        <ul className="flex flex-col divide-y divide-line">
          {items.map((n) => {
            const s = style[n.severity] ?? style.INFO
            return (
              <li key={n.id} className="flex items-start gap-3 py-3">
                <s.icon className={cx('mt-0.5 size-5 shrink-0', s.text)} aria-hidden />
                <div className="min-w-0 flex-1">
                  <p className="flex flex-wrap items-center gap-1.5">
                    <span className={cx('break-words text-sm', n.read ? 'text-muted' : 'font-medium text-fg')}>{n.title}</span>
                    <Badge tone={s.tone}>{t(n.severity)}</Badge>
                  </p>
                  {n.message && <p className="mt-0.5 break-words text-xs text-muted">{n.message}</p>}
                  <p className="mt-1 text-[11px] text-faint">
                    {formatShortDateTime(n.created_at)} · {n.source}
                  </p>
                </div>
                {!n.read && <IconButton icon={CheckCheck} size="sm" label={t('markRead')} onClick={() => void markOne.run(n)} />}
                <IconButton icon={Trash2} size="sm" tone="danger" label={t('remove')} onClick={() => void removeOne.run(n)} />
              </li>
            )
          })}
        </ul>
      )}
      <ConfirmDialog
        open={confirm}
        onClose={() => setConfirm(false)}
        title={t('clearTitle')}
        message={t('clearText')}
        confirmLabel={t('clear')}
        danger
        error={clear.error}
        onConfirm={async () => {
          await clear.run()
        }}
      />
    </Card>
  )
}
