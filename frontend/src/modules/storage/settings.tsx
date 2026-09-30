import { useEffect, useId, useState } from 'react'
import { HardDrive, Save } from 'lucide-react'
import { Alert, Button, Card, CardHeader, ErrorState, Field, Input, LoadingState, Switch } from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { t } from './strings'

const KEY_INTERVAL = 'storage.smart_interval_minutes'
const KEY_TEMP = 'storage.temp_warning_celsius'
const KEY_NOTIFY = 'storage.notify_removable'

type Settings = Record<string, string>

function inRange(value: string, min: number, max: number): boolean {
  if (!/^\d+$/.test(value.trim())) return false
  const n = Number(value)
  return n >= min && n <= max
}

export function StorageSettingsSection() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const query = useQuery<Settings>(isAdmin ? '/settings' : null)
  const [interval, setIntervalValue] = useState('')
  const [temp, setTemp] = useState('')
  const [notify, setNotify] = useState(true)
  const intervalId = useId()
  const tempId = useId()

  const data = query.data
  useEffect(() => {
    if (!data) return
    setIntervalValue(data[KEY_INTERVAL] ?? '60')
    setTemp(data[KEY_TEMP] ?? '55')
    setNotify((data[KEY_NOTIFY] ?? 'true') === 'true')
  }, [data])

  const save = useAction(
    () =>
      api.put<Settings>('/settings', {
        [KEY_INTERVAL]: interval.trim(),
        [KEY_TEMP]: temp.trim(),
        [KEY_NOTIFY]: notify ? 'true' : 'false',
      }),
    {
      onSuccess: () => {
        toast.success(t('settingsSaved'))
        void query.reload()
      },
    },
  )

  const intervalOk = inRange(interval, 10, 1440)
  const tempOk = inRange(temp, 30, 90)
  const dirty =
    data !== undefined &&
    (interval.trim() !== (data[KEY_INTERVAL] ?? '60') ||
      temp.trim() !== (data[KEY_TEMP] ?? '55') ||
      notify !== ((data[KEY_NOTIFY] ?? 'true') === 'true'))

  return (
    <Card>
      <CardHeader title={t('settingsTitle')} subtitle={t('settingsSubtitle')} icon={HardDrive} />
      {!isAdmin ? (
        <Alert tone="neutral">{t('settingsAdminOnly')}</Alert>
      ) : query.loading ? (
        <LoadingState />
      ) : query.error && !data ? (
        <ErrorState message={query.error} onRetry={() => void query.reload()} />
      ) : (
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (intervalOk && tempOk) void save.run()
          }}
        >
          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              label={t('settingsInterval')}
              htmlFor={intervalId}
              hint={t('settingsIntervalHint')}
              error={intervalOk ? null : t('settingsInvalid', { min: 10, max: 1440 })}
            >
              <Input
                id={intervalId}
                type="number"
                inputMode="numeric"
                min={10}
                max={1440}
                value={interval}
                invalid={!intervalOk}
                onChange={(e) => setIntervalValue(e.target.value)}
              />
            </Field>
            <Field
              label={t('settingsTemp')}
              htmlFor={tempId}
              hint={t('settingsTempHint')}
              error={tempOk ? null : t('settingsInvalid', { min: 30, max: 90 })}
            >
              <Input
                id={tempId}
                type="number"
                inputMode="numeric"
                min={30}
                max={90}
                value={temp}
                invalid={!tempOk}
                onChange={(e) => setTemp(e.target.value)}
              />
            </Field>
          </div>
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <p className="text-sm font-medium text-fg">{t('settingsNotify')}</p>
              <p className="mt-0.5 text-xs text-muted">{t('settingsNotifyHint')}</p>
            </div>
            <Switch checked={notify} onChange={setNotify} label={t('settingsNotify')} />
          </div>
          {save.error && <Alert tone="danger">{save.error}</Alert>}
          <div className="flex justify-end">
            <Button type="submit" variant="primary" icon={Save} loading={save.pending} disabled={!dirty || !intervalOk || !tempOk}>
              {t('settingsSave')}
            </Button>
          </div>
        </form>
      )}
    </Card>
  )
}
