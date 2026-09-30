import { useEffect, useState, type FormEvent } from 'react'
import { HeartPulse, Save } from 'lucide-react'
import { Alert, Button, Card, CardHeader, ErrorState, Field, Input, LoadingState } from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { t } from './strings'

type Settings = Record<string, string>

interface FieldSpec {
  key: string
  label: () => string
  min: number
  max: number
}

// Ranges match the validators registered by the backend module.
const FIELDS: FieldSpec[] = [
  { key: 'system.disk_warning_percent', label: () => t('diskWarning'), min: 50, max: 99 },
  { key: 'system.disk_critical_percent', label: () => t('diskCritical'), min: 51, max: 100 },
  { key: 'system.temp_warning_celsius', label: () => t('tempWarning'), min: 40, max: 110 },
  { key: 'system.temp_critical_celsius', label: () => t('tempCritical'), min: 41, max: 120 },
]

const PAIRS: Array<[string, string]> = [
  ['system.disk_warning_percent', 'system.disk_critical_percent'],
  ['system.temp_warning_celsius', 'system.temp_critical_celsius'],
]

function validate(values: Settings): Record<string, string> {
  const errors: Record<string, string> = {}
  for (const f of FIELDS) {
    const raw = (values[f.key] ?? '').trim()
    const n = Number(raw)
    if (!/^\d+$/.test(raw) || n < f.min || n > f.max) {
      errors[f.key] = t('errRange', { min: f.min, max: f.max })
    }
  }
  for (const [warn, crit] of PAIRS) {
    if (!errors[warn] && !errors[crit] && Number(values[crit]) <= Number(values[warn])) {
      errors[crit] = t('errOrder')
    }
  }
  return errors
}

export function SystemSettingsSection() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const { data, error, loading, reload } = useQuery<Settings>('/settings', { enabled: isAdmin })
  const [values, setValues] = useState<Settings>({})
  const [errors, setErrors] = useState<Record<string, string>>({})

  useEffect(() => {
    if (!data) return
    const next: Settings = {}
    for (const f of FIELDS) next[f.key] = data[f.key] ?? ''
    setValues(next)
  }, [data])

  const save = useAction((payload: Settings) => api.put<Settings>('/settings', payload), {
    onSuccess: () => {
      toast.success(t('saved'))
      void reload()
    },
    onError: (message) => toast.error(message),
  })

  const changed = data !== undefined && FIELDS.some((f) => (values[f.key] ?? '').trim() !== (data[f.key] ?? ''))

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const found = validate(values)
    setErrors(found)
    if (Object.keys(found).length > 0 || !data) return
    const payload: Settings = {}
    for (const f of FIELDS) {
      const v = String(Number(values[f.key]))
      if (v !== data[f.key]) payload[f.key] = v
    }
    if (Object.keys(payload).length > 0) void save.run(payload)
  }

  let body
  if (!isAdmin) {
    body = <Alert tone="warning">{t('adminOnly')}</Alert>
  } else if (loading) {
    body = <LoadingState />
  } else if (error || !data) {
    body = <ErrorState message={error ?? t('infoError')} onRetry={() => void reload()} className="py-4" />
  } else {
    body = (
      <form onSubmit={submit} noValidate className="flex flex-col gap-4">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          {FIELDS.map((f) => {
            const id = 'system-setting-' + f.key.replace(/[^a-z]/g, '-')
            return (
              <Field key={f.key} label={f.label()} htmlFor={id} error={errors[f.key]} hint={t('hintRange', { min: f.min, max: f.max })}>
                <Input
                  id={id}
                  type="number"
                  inputMode="numeric"
                  min={f.min}
                  max={f.max}
                  step={1}
                  value={values[f.key] ?? ''}
                  invalid={Boolean(errors[f.key])}
                  onChange={(e) => {
                    setValues((v) => ({ ...v, [f.key]: e.target.value }))
                    setErrors({})
                  }}
                />
              </Field>
            )
          })}
        </div>
        <div className="flex justify-end">
          <Button type="submit" variant="primary" icon={Save} loading={save.pending} disabled={!changed}>
            {t('save')}
          </Button>
        </div>
      </form>
    )
  }

  return (
    <Card>
      <CardHeader title={t('settingsTitle')} subtitle={t('settingsSubtitle')} icon={HeartPulse} />
      {body}
    </Card>
  )
}
