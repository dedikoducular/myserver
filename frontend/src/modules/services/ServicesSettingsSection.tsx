import { useEffect, useId, useState } from 'react'
import { ArrowDown, ArrowUp, Plus, Star, Trash2 } from 'lucide-react'
import { Alert, Button, Card, CardHeader, EmptyState, ErrorState, Field, IconButton, Input, LoadingState, Select } from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { t } from './strings'
import { DEFAULT_FEATURED, MAX_FEATURED, SETTING_FEATURED, type ServiceList } from './types'
import { normalizeUnit } from './util'

function parseList(raw: string | undefined): string[] {
  try {
    const v: unknown = JSON.parse(raw ?? '[]')
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
  } catch {
    return []
  }
}

export function ServicesSettingsSection() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const settings = useQuery<Record<string, string>>('/settings', { enabled: isAdmin })
  const known = useQuery<ServiceList>('/services', { enabled: isAdmin })
  const [items, setItems] = useState<string[]>([])
  const [dirty, setDirty] = useState(false)
  const [picked, setPicked] = useState('')
  const [manual, setManual] = useState('')
  const [addError, setAddError] = useState<string | null>(null)
  const pickId = useId()
  const manualId = useId()

  const stored = settings.data?.[SETTING_FEATURED]
  useEffect(() => {
    if (stored !== undefined) {
      setItems(parseList(stored))
      setDirty(false)
    }
  }, [stored])

  const save = useAction(
    (list: string[]) => api.put<Record<string, string>>('/settings', { [SETTING_FEATURED]: JSON.stringify(list) }),
    {
      onSuccess: (res) => {
        setItems(parseList(res[SETTING_FEATURED]))
        setDirty(false)
        toast.success(t('set_saved'))
        void settings.reload()
      },
      onError: (message) => toast.error(message),
    },
  )

  const change = (next: string[]) => {
    setItems(next)
    setDirty(true)
    setAddError(null)
  }

  const add = (raw: string): boolean => {
    const unit = normalizeUnit(raw)
    if (!unit) {
      setAddError(t('set_invalid'))
      return false
    }
    if (items.includes(unit)) {
      setAddError(t('set_duplicate'))
      return false
    }
    if (items.length >= MAX_FEATURED) {
      setAddError(t('set_full', { n: MAX_FEATURED }))
      return false
    }
    change([...items, unit])
    return true
  }

  const move = (index: number, delta: number) => {
    const target = index + delta
    const a = items[index]
    const b = items[target]
    if (a === undefined || b === undefined) return
    const next = [...items]
    next[index] = b
    next[target] = a
    change(next)
  }

  const names = new Map((known.data?.services ?? []).map((s) => [s.unit, s.name]))
  for (const s of known.data?.featured ?? []) names.set(s.unit, s.name)
  const nameOf = (unit: string) => names.get(unit) ?? unit.replace(/\.service$/, '')
  const choices = (known.data?.services ?? []).filter((s) => s.installed && !items.includes(s.unit))

  let body
  if (!isAdmin) {
    body = <Alert tone="accent">{t('set_admin')}</Alert>
  } else if (settings.loading) {
    body = <LoadingState />
  } else if (!settings.data) {
    body = <ErrorState message={settings.error ?? ''} onRetry={() => void settings.reload()} />
  } else {
    body = (
      <div className="flex flex-col gap-4">
        {items.length === 0 ? (
          <EmptyState className="py-6" icon={Star} title={t('set_empty')} description={t('set_empty_hint')} />
        ) : (
          <ol className="flex flex-col">
            {items.map((unit, i) => (
              <li key={unit} className="flex items-center gap-2 border-b border-line/60 py-1.5 last:border-b-0">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm text-fg">{nameOf(unit)}</p>
                  <p className="truncate font-mono text-[11px] text-faint">{unit}</p>
                </div>
                <IconButton icon={ArrowUp} label={t('set_up', { name: nameOf(unit) })} disabled={i === 0} onClick={() => move(i, -1)} />
                <IconButton
                  icon={ArrowDown}
                  label={t('set_down', { name: nameOf(unit) })}
                  disabled={i === items.length - 1}
                  onClick={() => move(i, 1)}
                />
                <IconButton
                  icon={Trash2}
                  tone="danger"
                  label={t('set_remove', { name: nameOf(unit) })}
                  onClick={() => change(items.filter((u) => u !== unit))}
                />
              </li>
            ))}
          </ol>
        )}

        <div className="grid gap-3 sm:grid-cols-2">
          <Field label={t('set_pick')} htmlFor={pickId}>
            <div className="flex gap-2">
              <Select id={pickId} value={picked} onChange={(e) => setPicked(e.target.value)} disabled={choices.length === 0}>
                <option value="">{t('set_pick_placeholder')}</option>
                {choices.map((s) => (
                  <option key={s.unit} value={s.unit}>
                    {s.name === s.unit.replace(/\.service$/, '') ? s.unit : `${s.name} (${s.unit})`}
                  </option>
                ))}
              </Select>
              <Button
                icon={Plus}
                disabled={!picked}
                onClick={() => {
                  if (add(picked)) setPicked('')
                }}
              >
                {t('set_add')}
              </Button>
            </div>
          </Field>
          <Field label={t('set_manual')} htmlFor={manualId} hint={t('set_manual_hint')} error={addError}>
            <form
              className="flex gap-2"
              onSubmit={(e) => {
                e.preventDefault()
                if (add(manual)) setManual('')
              }}
            >
              <Input
                id={manualId}
                value={manual}
                invalid={addError !== null}
                onChange={(e) => {
                  setManual(e.target.value)
                  setAddError(null)
                }}
                autoComplete="off"
                spellCheck={false}
                maxLength={136}
              />
              <Button type="submit" icon={Plus} disabled={!manual.trim()}>
                {t('set_add')}
              </Button>
            </form>
          </Field>
        </div>

        <div className="flex flex-wrap justify-end gap-2">
          <Button variant="ghost" disabled={save.pending} onClick={() => change([...DEFAULT_FEATURED])}>
            {t('set_reset')}
          </Button>
          <Button variant="primary" disabled={!dirty} loading={save.pending} onClick={() => void save.run(items)}>
            {t('set_save')}
          </Button>
        </div>
      </div>
    )
  }

  return (
    <Card>
      <CardHeader title={t('set_title')} icon={Star} subtitle={t('set_subtitle')} />
      {body}
    </Card>
  )
}
