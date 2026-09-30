import { useEffect, useId, useState } from 'react'
import { RefreshCw, Save } from 'lucide-react'
import { Alert, Button, Card, CardHeader, ErrorState, Field, Input, LoadingState, Select, Switch } from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { t } from './strings'
import { refreshSummary } from './summary'

const K = {
  interval: 'updates.check_interval_hours',
  apt: 'updates.apt_check_enabled',
  docker: 'updates.docker_check_enabled',
  self: 'updates.self_check_enabled',
  source: 'updates.source',
  repo: 'updates.github_repo',
  url: 'updates.manifest_url',
} as const

type Values = Record<string, string>

interface Form {
  interval: string
  apt: boolean
  docker: boolean
  self: boolean
  source: string
  repo: string
  url: string
}

function toForm(v: Values): Form {
  return {
    interval: v[K.interval] ?? '24',
    apt: v[K.apt] !== 'false',
    docker: v[K.docker] !== 'false',
    self: v[K.self] !== 'false',
    source: v[K.source] || 'none',
    repo: v[K.repo] ?? '',
    url: v[K.url] ?? '',
  }
}

function validate(f: Form): string | null {
  const n = Number(f.interval)
  if (!/^\d+$/.test(f.interval.trim()) || n < 1 || n > 720) return t('intervalInvalid')
  if (f.source === 'github' && !/^[A-Za-z0-9][A-Za-z0-9-]{0,38}\/[A-Za-z0-9._-]{1,100}$/.test(f.repo.trim())) return t('repoRequired')
  if (f.source === 'url' && !/^https:\/\/[^\s/]+/.test(f.url.trim())) return t('urlRequired')
  return null
}

export function UpdatesSettingsSection() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const settings = useQuery<Values>('/settings', { enabled: isAdmin })
  const [form, setForm] = useState<Form | null>(null)
  const [problem, setProblem] = useState<string | null>(null)
  const ids = { interval: useId(), source: useId(), repo: useId(), url: useId() }

  useEffect(() => {
    if (settings.data) setForm(toForm(settings.data))
  }, [settings.data])

  const save = useAction(
    (f: Form) =>
      api.put<Values>('/settings', {
        [K.interval]: f.interval.trim(),
        [K.apt]: String(f.apt),
        [K.docker]: String(f.docker),
        [K.self]: String(f.self),
        [K.source]: f.source,
        [K.repo]: f.repo.trim(),
        [K.url]: f.url.trim(),
      }),
    {
      onSuccess: (v) => {
        setForm(toForm(v))
        toast.success(t('saved'))
        refreshSummary()
      },
      onError: (m) => toast.error(m),
    },
  )

  if (!isAdmin) return null
  const set = <F extends keyof Form>(key: F, value: Form[F]) => {
    setProblem(null)
    setForm((f) => (f ? { ...f, [key]: value } : f))
  }

  const toggles: Array<{ key: 'apt' | 'docker' | 'self'; label: string }> = [
    { key: 'apt', label: t('autoApt') },
    { key: 'docker', label: t('autoDocker') },
    { key: 'self', label: t('autoSelf') },
  ]

  return (
    <Card>
      <CardHeader title={t('settingsTitle')} icon={RefreshCw} subtitle={t('settingsSubtitle')} />
      {settings.loading ? (
        <LoadingState />
      ) : settings.error || !form ? (
        <ErrorState message={settings.error ?? ''} onRetry={() => void settings.reload()} />
      ) : (
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            const p = validate(form)
            setProblem(p)
            if (!p) void save.run(form)
          }}
        >
          <Field label={t('intervalLabel')} htmlFor={ids.interval} hint={t('intervalHint')} className="max-w-xs">
            <Input
              id={ids.interval}
              type="number"
              inputMode="numeric"
              min={1}
              max={720}
              value={form.interval}
              onChange={(e) => set('interval', e.target.value)}
            />
          </Field>
          <div className="flex flex-col gap-3">
            {toggles.map(({ key, label }) => (
              <div key={key} className="flex min-h-10 items-center justify-between gap-3">
                <span className="text-sm text-fg">{label}</span>
                <Switch checked={form[key]} onChange={(v) => set(key, v)} label={label} />
              </div>
            ))}
            <p className="text-xs text-faint">{t('autoHint')}</p>
          </div>
          <Field label={t('sourceLabel')} htmlFor={ids.source} className="max-w-md">
            <Select id={ids.source} value={form.source} onChange={(e) => set('source', e.target.value)}>
              <option value="none">{t('sourceNone')}</option>
              <option value="github">{t('sourceGithub')}</option>
              <option value="url">{t('sourceUrl')}</option>
            </Select>
          </Field>
          {form.source === 'github' && (
            <Field label={t('repoLabel')} htmlFor={ids.repo} hint={t('repoHint')} className="max-w-md">
              <Input id={ids.repo} value={form.repo} autoComplete="off" spellCheck={false} onChange={(e) => set('repo', e.target.value)} />
            </Field>
          )}
          {form.source === 'url' && (
            <Field label={t('urlLabel')} htmlFor={ids.url} hint={t('urlHint')} className="max-w-md">
              <Input
                id={ids.url}
                type="url"
                inputMode="url"
                value={form.url}
                autoComplete="off"
                spellCheck={false}
                onChange={(e) => set('url', e.target.value)}
              />
            </Field>
          )}
          {problem && <Alert tone="danger">{problem}</Alert>}
          <div>
            <Button type="submit" variant="primary" icon={Save} loading={save.pending}>
              {t('save')}
            </Button>
          </div>
        </form>
      )}
    </Card>
  )
}
