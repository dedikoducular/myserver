import { useEffect, useId, useState } from 'react'
import { Archive, KeyRound, Save, ShieldOff } from 'lucide-react'
import {
  Alert,
  Button,
  Card,
  CardHeader,
  ConfirmDialog,
  ErrorState,
  Field,
  Input,
  KeyValueList,
  LoadingState,
  ProgressBar,
  Select,
  Status,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { formatBytes, formatDateTime } from '@/lib/format'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { ToggleRow } from './dialogs'
import { t } from './strings'
import type { EncryptionInfo, Overview } from './types'

const K = {
  retention: 'backup.default_retention',
  consistency: 'backup.default_consistency',
  binds: 'backup.default_include_binds',
  minFree: 'backup.min_free_gb',
} as const

type Values = Record<string, string>

interface Form {
  retention: string
  consistency: string
  binds: boolean
  minFree: string
}

function toForm(v: Values): Form {
  return {
    retention: v[K.retention] ?? '7',
    consistency: v[K.consistency] === 'live' ? 'live' : 'stopped',
    binds: v[K.binds] !== 'false',
    minFree: v[K.minFree] ?? '5',
  }
}

function EncryptionPanel({ info, onChanged }: { info: EncryptionInfo; onChanged: () => void }) {
  const [p1, setP1] = useState('')
  const [p2, setP2] = useState('')
  const [problem, setProblem] = useState<string | null>(null)
  const [disable, setDisable] = useState(false)
  const id1 = useId()
  const id2 = useId()

  const save = useAction((passphrase: string) => api.put<EncryptionInfo>('/backup/encryption', { passphrase }), {
    onSuccess: () => {
      setP1('')
      setP2('')
      toast.success(t('setEncSaved'))
      onChanged()
    },
  })
  const clear = useAction(() => api.del<EncryptionInfo>('/backup/encryption'), {
    onSuccess: () => {
      setDisable(false)
      toast.success(t('setEncCleared'))
      onChanged()
    },
  })

  const submit = () => {
    if ([...p1].length < 12) return setProblem(t('setEncShort'))
    if (p1 !== p2) return setProblem(t('setEncMismatch'))
    setProblem(null)
    void save.run(p1)
  }

  return (
    <section className="flex flex-col gap-3 border-t border-line pt-4">
      <h3 className="flex items-center gap-2 text-sm font-semibold text-fg">
        <KeyRound className="size-4 text-muted" aria-hidden />
        {t('setEncTitle')}
      </h3>
      {info.error ? (
        <Alert tone="danger" title={t('encErrorTitle')}>
          {info.error}
        </Alert>
      ) : (
        <div className="flex flex-col gap-1">
          <Status tone={info.enabled ? 'success' : 'warning'}>{info.enabled ? t('encrypted') : t('unencrypted')}</Status>
          <p className="text-xs text-muted">{info.enabled ? t('setEncOn', { time: formatDateTime(info.set_at) }) : t('setEncOff')}</p>
        </div>
      )}
      <Alert tone="accent" title={t('setEncHowTitle')}>
        <p>{t('setEncHow', { file: info.key_file })}</p>
        <p className="mt-1.5">{t('setEncImply')}</p>
        <p className="mt-1.5">{t('setEncForget')}</p>
      </Alert>
      {info.enabled && <p className="text-xs text-muted">{t('setEncChangeInfo')}</p>}
      <form
        className="grid gap-3 sm:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
      >
        <Field label={t('setEncNew')} htmlFor={id1}>
          <Input id={id1} type="password" autoComplete="new-password" value={p1} onChange={(e) => setP1(e.target.value)} />
        </Field>
        <Field label={t('setEncRepeat')} htmlFor={id2}>
          <Input id={id2} type="password" autoComplete="new-password" value={p2} onChange={(e) => setP2(e.target.value)} />
        </Field>
        {(problem || save.error) && (
          <Alert tone="danger" className="sm:col-span-2">
            {problem ?? save.error}
          </Alert>
        )}
        <div className="flex flex-wrap gap-2 sm:col-span-2">
          <Button type="submit" variant="primary" icon={KeyRound} loading={save.pending} disabled={!p1 || !p2}>
            {info.enabled ? t('setEncChange') : t('setEncSet')}
          </Button>
          {info.enabled && (
            <Button variant="danger" icon={ShieldOff} onClick={() => setDisable(true)}>
              {t('setEncDisable')}
            </Button>
          )}
        </div>
      </form>
      <ConfirmDialog
        open={disable}
        onClose={() => setDisable(false)}
        onConfirm={async () => {
          await clear.run()
        }}
        danger
        title={t('setEncDisableTitle')}
        message={t('setEncDisableMsg')}
        warning={t('setEncDisableWarn')}
        confirmLabel={t('setEncDisable')}
        error={clear.error}
      />
    </section>
  )
}

export function BackupSettingsSection() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const settings = useQuery<Values>('/settings', { enabled: isAdmin })
  const overview = useQuery<Overview>('/backup/overview', { enabled: isAdmin })
  const [form, setForm] = useState<Form | null>(null)
  const [problem, setProblem] = useState<string | null>(null)
  const ids = { retention: useId(), consistency: useId(), minFree: useId() }

  useEffect(() => {
    if (settings.data) setForm(toForm(settings.data))
  }, [settings.data])

  const save = useAction(
    (f: Form) =>
      api.put<Values>('/settings', {
        [K.retention]: f.retention.trim(),
        [K.consistency]: f.consistency,
        [K.binds]: String(f.binds),
        [K.minFree]: f.minFree.trim(),
      }),
    {
      onSuccess: (v) => {
        setForm(toForm(v))
        toast.success(t('setSaved'))
      },
      onError: (m) => toast.error(m),
    },
  )

  if (!isAdmin) return null
  const set = <F extends keyof Form>(key: F, value: Form[F]) => {
    setProblem(null)
    setForm((f) => (f ? { ...f, [key]: value } : f))
  }
  const ov = overview.data
  const usedPercent = ov && ov.free_bytes !== null && ov.total_bytes ? ((ov.total_bytes - ov.free_bytes) / ov.total_bytes) * 100 : null

  return (
    <Card>
      <CardHeader title={t('setTitle')} icon={Archive} subtitle={t('setSubtitle')} />
      {settings.loading || overview.loading ? (
        <LoadingState />
      ) : settings.error || !form ? (
        <ErrorState message={settings.error ?? ''} onRetry={() => void settings.reload()} />
      ) : (
        <div className="flex flex-col gap-5">
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault()
              const ok = /^\d+$/.test(form.retention.trim()) && /^\d+$/.test(form.minFree.trim()) && Number(form.retention) >= 1 && Number(form.minFree) >= 1
              if (!ok) return setProblem(t('invalidNumber'))
              void save.run(form)
            }}
          >
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t('setRetention')} htmlFor={ids.retention} hint={t('setRetentionHint')}>
                <Input id={ids.retention} type="number" inputMode="numeric" min={1} max={365} value={form.retention} onChange={(e) => set('retention', e.target.value)} />
              </Field>
              <Field label={t('setMinFree')} htmlFor={ids.minFree} hint={t('setMinFreeHint')}>
                <Input id={ids.minFree} type="number" inputMode="numeric" min={1} value={form.minFree} onChange={(e) => set('minFree', e.target.value)} />
              </Field>
            </div>
            <Field label={t('setConsistency')} htmlFor={ids.consistency} className="max-w-md">
              <Select id={ids.consistency} value={form.consistency} onChange={(e) => set('consistency', e.target.value)}>
                <option value="stopped">{t('setConsStopped')}</option>
                <option value="live">{t('setConsLive')}</option>
              </Select>
            </Field>
            {form.consistency === 'live' && (
              <Alert tone="warning" title={t('liveWarnTitle')}>
                {t('liveWarn')}
              </Alert>
            )}
            <ToggleRow label={t('setBinds')} hint={t('bindsHint')} checked={form.binds} onChange={(v) => set('binds', v)} />
            {problem && <Alert tone="danger">{problem}</Alert>}
            <div>
              <Button type="submit" variant="primary" icon={Save} loading={save.pending}>
                {t('save')}
              </Button>
            </div>
          </form>

          {overview.error || !ov ? (
            <ErrorState message={overview.error ?? ''} onRetry={() => void overview.reload()} />
          ) : (
            <>
              <section className="flex flex-col gap-3 border-t border-line pt-4">
                <KeyValueList
                  items={[
                    { label: t('setDir'), value: <span className="break-all font-mono text-xs">{ov.dir}</span> },
                    {
                      label: t('setFree'),
                      value:
                        ov.free_bytes === null || ov.total_bytes === null
                          ? t('setFreeUnknown')
                          : t('setFreeOf', { free: formatBytes(ov.free_bytes), total: formatBytes(ov.total_bytes) }),
                    },
                    { label: t('setUsed'), value: formatBytes(ov.used_bytes) },
                  ]}
                />
                {usedPercent !== null && <ProgressBar value={usedPercent} label={t('setFree')} />}
              </section>
              <EncryptionPanel info={ov.encryption} onChanged={() => void overview.reload()} />
            </>
          )}
        </div>
      )}
    </Card>
  )
}
