import { useState } from 'react'
import { ChevronLeft, ChevronRight, LogOut, MonitorSmartphone, Save, ScrollText, ShieldCheck } from 'lucide-react'
import {
  Badge,
  Button,
  Card,
  CardHeader,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Field,
  Input,
  LoadingState,
  TableWrap,
  tableClass,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { formatRelative, formatShortDateTime } from '@/lib/format'
import { messages } from '@/i18n'
import { api } from '@/services/api'
import { toast } from '@/stores/ui'
import type { AuditEntry, SessionInfo } from '@/types/api'
import { useSettings } from './useSettings'

const t = messages({
  tr: {
    sessionTitle: 'Oturum Güvenliği',
    sessionSubtitle: 'Oturumların ne kadar süre açık kalacağını belirleyin',
    sessionHours: 'Oturum süresi (saat)',
    sessionHoursHint: '1 ile 720 saat arasında. Yeni açılan oturumlara uygulanır.',
    save: 'Kaydet',
    sessionsTitle: 'Açık Oturumlarım',
    sessionsSubtitle: 'Hesabınızla açılmış oturumlar',
    current: 'Bu oturum',
    lastSeen: 'Son etkinlik: {when}',
    expires: 'Bitiş: {when}',
    logoutOthers: 'Diğer Oturumları Kapat',
    logoutOthersTitle: 'Diğer oturumlar kapatılsın mı?',
    logoutOthersText: 'Bu cihaz dışındaki tüm oturumlarınız kapatılacak.',
    loggedOut: 'Diğer oturumlar kapatıldı.',
    noSessions: 'Açık oturum yok',
    auditTitle: 'Denetim Kayıtları',
    auditSubtitle: 'Kim, ne zaman, hangi adresten, hangi işlemi yaptı',
    noAudit: 'Henüz denetim kaydı yok',
    time: 'Zaman',
    user: 'Kullanıcı',
    ip: 'IP Adresi',
    action: 'İşlem',
    target: 'Hedef',
    result: 'Sonuç',
    ok: 'Başarılı',
    failed: 'Başarısız',
    page: '{from}-{to} / {total}',
    prev: 'Önceki sayfa',
    next: 'Sonraki sayfa',
    unknownDevice: 'Bilinmeyen cihaz',
  },
})

const PAGE = 25

/** Short, readable device name from a User-Agent string. */
function describeAgent(ua: string): string {
  if (!ua) return t('unknownDevice')
  const browser = /Edg\//.test(ua) ? 'Edge' : /OPR\//.test(ua) ? 'Opera' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : ''
  const os = /Android/.test(ua)
    ? 'Android'
    : /iPhone|iPad/.test(ua)
      ? 'iOS'
      : /Windows/.test(ua)
        ? 'Windows'
        : /Mac OS X/.test(ua)
          ? 'macOS'
          : /Linux/.test(ua)
            ? 'Linux'
            : ''
  const text = [browser, os].filter(Boolean).join(' · ')
  return text || ua.slice(0, 60)
}

function SessionPolicy() {
  const form = useSettings()
  const key = 'security.session_hours'
  const value = form.values[key] ?? ''
  const n = Number(value)
  const valid = Number.isInteger(n) && n >= 1 && n <= 720
  if (form.loading) return <LoadingState />
  if (form.error) return <ErrorState message={form.error} onRetry={() => void form.reload()} />
  return (
    <form
      className="flex max-w-sm flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault()
        if (valid) void form.save([key])
      }}
    >
      <Field label={t('sessionHours')} htmlFor="sec-hours" hint={t('sessionHoursHint')}>
        <Input id="sec-hours" type="number" min={1} max={720} inputMode="numeric" value={value} invalid={!valid} onChange={(e) => form.set(key, e.target.value)} />
      </Field>
      <div>
        <Button type="submit" variant="primary" icon={Save} loading={form.saving} disabled={!valid || !form.dirty([key])}>
          {t('save')}
        </Button>
      </div>
    </form>
  )
}

function Sessions() {
  const { data, error, loading, reload } = useQuery<SessionInfo[]>('/auth/sessions')
  const [confirm, setConfirm] = useState(false)
  const logout = useAction(() => api.post('/auth/logout-all'), {
    onSuccess: () => {
      toast.success(t('loggedOut'))
      setConfirm(false)
      void reload()
    },
  })
  const others = (data ?? []).filter((s) => !s.current).length
  return (
    <Card>
      <CardHeader
        title={t('sessionsTitle')}
        subtitle={t('sessionsSubtitle')}
        icon={MonitorSmartphone}
        actions={
          <Button size="sm" variant="danger" icon={LogOut} disabled={others === 0} onClick={() => setConfirm(true)}>
            {t('logoutOthers')}
          </Button>
        }
      />
      {loading ? (
        <LoadingState />
      ) : error ? (
        <ErrorState message={error} onRetry={() => void reload()} />
      ) : !data || data.length === 0 ? (
        <EmptyState icon={MonitorSmartphone} title={t('noSessions')} />
      ) : (
        <ul className="flex flex-col divide-y divide-line">
          {data.map((s) => (
            <li key={`${s.created_at}-${s.ip}-${s.user_agent}`} className="flex flex-wrap items-center gap-x-4 gap-y-1 py-3">
              <div className="min-w-0 flex-1">
                <p className="flex flex-wrap items-center gap-1.5 text-sm font-medium text-fg">
                  {describeAgent(s.user_agent)}
                  {s.current && <Badge tone="success">{t('current')}</Badge>}
                </p>
                <p className="mt-0.5 font-mono text-xs text-muted">{s.ip}</p>
              </div>
              <div className="text-xs text-muted sm:text-right">
                <p>{t('lastSeen', { when: formatRelative(s.last_seen_at) })}</p>
                <p className="text-faint">{t('expires', { when: formatShortDateTime(s.expires_at) })}</p>
              </div>
            </li>
          ))}
        </ul>
      )}
      <ConfirmDialog
        open={confirm}
        onClose={() => setConfirm(false)}
        title={t('logoutOthersTitle')}
        message={t('logoutOthersText')}
        confirmLabel={t('logoutOthers')}
        danger
        error={logout.error}
        onConfirm={async () => {
          await logout.run()
        }}
      />
    </Card>
  )
}

function AuditLog() {
  const [offset, setOffset] = useState(0)
  const { data, error, loading, fetching, reload } = useQuery<{ items: AuditEntry[]; total: number }>('/audit', {
    query: { limit: PAGE, offset },
  })
  const total = data?.total ?? 0
  return (
    <Card>
      <CardHeader title={t('auditTitle')} subtitle={t('auditSubtitle')} icon={ScrollText} />
      {loading ? (
        <LoadingState />
      ) : error ? (
        <ErrorState message={error} onRetry={() => void reload()} />
      ) : !data || data.items.length === 0 ? (
        <EmptyState icon={ScrollText} title={t('noAudit')} />
      ) : (
        <>
          <TableWrap>
            <table className={tableClass.table}>
              <thead>
                <tr>
                  <th className={tableClass.th}>{t('time')}</th>
                  <th className={tableClass.th}>{t('user')}</th>
                  <th className={tableClass.th}>{t('ip')}</th>
                  <th className={tableClass.th}>{t('action')}</th>
                  <th className={tableClass.th}>{t('target')}</th>
                  <th className={tableClass.th}>{t('result')}</th>
                </tr>
              </thead>
              <tbody>
                {data.items.map((e) => (
                  <tr key={e.id} className={tableClass.row}>
                    <td className={`${tableClass.td} whitespace-nowrap text-muted`}>{formatShortDateTime(e.created_at)}</td>
                    <td className={tableClass.td}>{e.username}</td>
                    <td className={`${tableClass.td} font-mono text-xs text-muted`}>{e.ip}</td>
                    <td className={`${tableClass.td} font-mono text-xs`}>{e.action}</td>
                    <td className={`${tableClass.td} max-w-64`}>
                      <span className="block truncate" title={e.detail ? `${e.target} — ${e.detail}` : e.target}>
                        {e.target || '—'}
                      </span>
                      {e.detail && <span className="block truncate text-xs text-faint">{e.detail}</span>}
                    </td>
                    <td className={tableClass.td}>
                      <Badge tone={e.success ? 'success' : 'danger'}>{e.success ? t('ok') : t('failed')}</Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </TableWrap>
          <div className="mt-3 flex items-center justify-between gap-2 text-xs text-muted">
            <span>{t('page', { from: offset + 1, to: Math.min(offset + PAGE, total), total })}</span>
            <span className="flex gap-1">
              <Button size="sm" icon={ChevronLeft} aria-label={t('prev')} disabled={offset === 0 || fetching} onClick={() => setOffset(Math.max(0, offset - PAGE))} />
              <Button size="sm" icon={ChevronRight} aria-label={t('next')} disabled={offset + PAGE >= total || fetching} onClick={() => setOffset(offset + PAGE)} />
            </span>
          </div>
        </>
      )}
    </Card>
  )
}

export function SecuritySection() {
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader title={t('sessionTitle')} subtitle={t('sessionSubtitle')} icon={ShieldCheck} />
        <SessionPolicy />
      </Card>
      <Sessions />
      <AuditLog />
    </div>
  )
}
