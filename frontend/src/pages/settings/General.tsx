import { useMemo, useState } from 'react'
import { Save } from 'lucide-react'
import { Alert, Button, Card, CardHeader, ConfirmDialog, ErrorState, Field, Input, LoadingState, Select } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { messages } from '@/i18n'
import { useSettings } from './useSettings'

const t = messages({
  tr: {
    title: 'Genel',
    subtitle: 'Sunucunun adı, saat dilimi ve panel dili',
    hostname: 'Sunucu adı',
    hostnameHint: 'Harf, rakam ve "-" kullanın. En fazla 63 karakter.',
    hostnameInvalid: 'Sunucu adı geçersiz.',
    timezone: 'Saat dilimi',
    timezoneSearch: 'Saat dilimi ara',
    language: 'Dil',
    languageHint: 'Şu anda yalnızca Türkçe kullanılabilir.',
    turkish: 'Türkçe',
    save: 'Kaydet',
    saved: 'Genel ayarlar kaydedildi.',
    confirmTitle: 'Sunucu adı değiştirilsin mi?',
    confirmText: 'Sunucu adı "{name}" olarak değiştirilecek.',
    confirmWarning: 'Bu ada göre yapılandırılmış ağ paylaşımları veya diğer cihazlardaki kısayollar yeni adla güncellenmelidir.',
    confirm: 'Değiştir',
  },
})

const KEYS = ['general.hostname', 'general.timezone', 'general.language']
const hostnameRe = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

export function GeneralSection() {
  const form = useSettings()
  const zones = useQuery<string[]>('/settings/timezones')
  const [filter, setFilter] = useState('')
  const [confirm, setConfirm] = useState(false)

  const hostname = form.values['general.hostname'] ?? ''
  const timezone = form.values['general.timezone'] ?? ''
  const hostnameValid = hostnameRe.test(hostname)

  const options = useMemo(() => {
    const all = zones.data ?? []
    const q = filter.trim().toLowerCase().replace(/\s+/g, '_')
    const list = q ? all.filter((z) => z.toLowerCase().includes(q)) : all
    return timezone && !list.includes(timezone) ? [timezone, ...list] : list
  }, [zones.data, filter, timezone])

  if (form.loading) return <LoadingState />
  if (form.error) return <ErrorState message={form.error} onRetry={() => void form.reload()} />

  const submit = () => {
    if (!hostnameValid) return
    if (form.dirty(['general.hostname'])) setConfirm(true)
    else void form.save(KEYS, t('saved'))
  }

  return (
    <Card>
      <CardHeader title={t('title')} subtitle={t('subtitle')} />
      <form
        className="flex max-w-xl flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        <Field label={t('hostname')} htmlFor="set-hostname" hint={t('hostnameHint')} error={hostnameValid ? null : t('hostnameInvalid')}>
          <Input
            id="set-hostname"
            value={hostname}
            maxLength={63}
            spellCheck={false}
            autoCapitalize="none"
            invalid={!hostnameValid}
            onChange={(e) => form.set('general.hostname', e.target.value.toLowerCase())}
          />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label={t('timezoneSearch')} htmlFor="set-zone-filter">
            <Input id="set-zone-filter" value={filter} spellCheck={false} placeholder="Istanbul" onChange={(e) => setFilter(e.target.value)} />
          </Field>
          <Field label={t('timezone')} htmlFor="set-zone" error={zones.error}>
            <Select id="set-zone" value={timezone} disabled={zones.loading} onChange={(e) => form.set('general.timezone', e.target.value)}>
              {options.map((z) => (
                <option key={z} value={z}>
                  {z.replace(/_/g, ' ')}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        <Field label={t('language')} htmlFor="set-language" hint={t('languageHint')}>
          <Select id="set-language" value={form.values['general.language'] ?? 'tr'} onChange={(e) => form.set('general.language', e.target.value)}>
            <option value="tr">{t('turkish')}</option>
          </Select>
        </Field>
        <div>
          <Button type="submit" variant="primary" icon={Save} loading={form.saving} disabled={!form.dirty(KEYS) || !hostnameValid}>
            {t('save')}
          </Button>
        </div>
      </form>
      <ConfirmDialog
        open={confirm}
        onClose={() => setConfirm(false)}
        title={t('confirmTitle')}
        message={t('confirmText', { name: hostname })}
        warning={<Alert tone="warning">{t('confirmWarning')}</Alert>}
        confirmLabel={t('confirm')}
        onConfirm={async () => {
          await form.save(KEYS, t('saved'))
          setConfirm(false)
        }}
      />
    </Card>
  )
}
