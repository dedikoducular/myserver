import { useEffect, useId, useState } from 'react'
import { Save, SquareTerminal } from 'lucide-react'
import {
  Alert,
  Button,
  Card,
  CardHeader,
  ErrorState,
  Field,
  Input,
  LoadingState,
  Select,
  Switch,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { t } from './strings'
import { SETTING_ENABLED, SETTING_TIMEOUT, SETTING_USER, type SystemUser } from './types'

type Settings = Record<string, string>

export function TerminalSettingsSection() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const settings = useQuery<Settings>('/settings', { enabled: isAdmin })
  const users = useQuery<SystemUser[]>('/terminal/users', { enabled: isAdmin })
  const userId = useId()
  const timeoutId = useId()

  const [enabled, setEnabled] = useState(true)
  const [user, setUser] = useState('')
  const [timeout, setTimeoutValue] = useState('15')

  const stored = settings.data
  useEffect(() => {
    if (!stored) return
    setEnabled(stored[SETTING_ENABLED] === 'true')
    setUser(stored[SETTING_USER] ?? '')
    setTimeoutValue(stored[SETTING_TIMEOUT] ?? '15')
  }, [stored])

  const save = useAction((changes: Settings) => api.put<Settings>('/settings', changes), {
    onSuccess: () => {
      toast.success(t('saved'))
      void settings.reload()
    },
    onError: (message) => toast.error(message),
  })

  let body
  if (!isAdmin) {
    body = <Alert tone="neutral">{t('settingsAdminOnly')}</Alert>
  } else if (settings.loading) {
    body = <LoadingState />
  } else if (settings.error || !stored) {
    body = <ErrorState message={settings.error ?? ''} onRetry={() => void settings.reload()} />
  } else {
    const minutes = Number(timeout)
    const timeoutValid = /^\d{1,3}$/.test(timeout.trim()) && minutes >= 1 && minutes <= 240

    const changes: Settings = {}
    if (String(enabled) !== stored[SETTING_ENABLED]) changes[SETTING_ENABLED] = String(enabled)
    if (user !== (stored[SETTING_USER] ?? '')) changes[SETTING_USER] = user
    if (timeoutValid && String(minutes) !== stored[SETTING_TIMEOUT]) changes[SETTING_TIMEOUT] = String(minutes)
    const dirty = Object.keys(changes).length > 0

    const list = users.data ?? []
    const known = user === '' || list.some((u) => u.username === user)

    body = (
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault()
          if (dirty && timeoutValid) void save.run(changes)
        }}
      >
        <div className="flex items-center justify-between gap-4 rounded-xl border border-line bg-surface px-3 py-3">
          <div className="min-w-0">
            <p className="text-sm font-medium text-fg">{t('enabledLabel')}</p>
            <p className="mt-0.5 text-xs text-muted">{enabled ? t('enabledOn') : t('enabledOff')}</p>
          </div>
          <Switch checked={enabled} onChange={setEnabled} label={t('enabledLabel')} disabled={save.pending} />
        </div>

        <Field
          label={t('userLabel')}
          htmlFor={userId}
          hint={t('userHint')}
          error={users.error ? t('usersError', { message: users.error }) : null}
        >
          <Select id={userId} value={user} disabled={save.pending || users.loading} onChange={(e) => setUser(e.target.value)}>
            <option value="">{t('userNone')}</option>
            {!known && <option value={user}>{t('userMissing', { user })}</option>}
            {list.map((u) => (
              <option key={u.username} value={u.username}>
                {t('userOption', { user: u.full_name ? `${u.username} (${u.full_name})` : u.username, shell: u.shell })}
              </option>
            ))}
          </Select>
        </Field>
        {!users.loading && !users.error && list.length === 0 && <Alert tone="warning">{t('usersEmpty')}</Alert>}

        <Field
          label={t('timeoutLabel')}
          htmlFor={timeoutId}
          hint={t('timeoutHint')}
          error={timeoutValid ? null : t('timeoutInvalid')}
          className="max-w-xs"
        >
          <Input
            id={timeoutId}
            type="number"
            inputMode="numeric"
            min={1}
            max={240}
            step={1}
            value={timeout}
            invalid={!timeoutValid}
            disabled={save.pending}
            onChange={(e) => setTimeoutValue(e.target.value)}
          />
        </Field>

        <div className="flex items-center justify-end gap-3">
          {!dirty && <span className="text-xs text-faint">{t('noChanges')}</span>}
          <Button type="submit" variant="primary" icon={Save} loading={save.pending} disabled={!dirty || !timeoutValid}>
            {t('save')}
          </Button>
        </div>
      </form>
    )
  }

  return (
    <Card>
      <CardHeader title={t('settingsTitle')} subtitle={t('settingsSubtitle')} icon={SquareTerminal} />
      {body}
    </Card>
  )
}
