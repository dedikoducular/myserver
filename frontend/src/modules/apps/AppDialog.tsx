import { useEffect, useId, useState, type FormEvent, type ReactNode } from 'react'
import { ExternalLink, Eye, EyeOff } from 'lucide-react'
import { FolderOpen } from 'lucide-react'
import { Alert, Badge, Button, ErrorState, Field, IconButton, Input, LoadingState, Modal, Switch } from '@/components/ui'
import { FolderPicker } from '@/modules/files'
import { useAction, useQuery } from '@/hooks/useApi'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { JobProgress } from './JobProgress'
import { AppIcon } from './parts'
import { portAddress } from './util'
import { t } from './strings'
import type { AppDetail, EnvField, InputsBody, Job, PathField, PortField } from './types'

export type AppDialogMode = 'install' | 'settings'

interface FormState {
  ports: Record<string, string>
  env: Record<string, string>
  paths: Record<string, string>
  options: Record<string, boolean>
  loopback: boolean
  accept: boolean
}

function initialState(detail: AppDetail): FormState {
  const state: FormState = { ports: {}, env: {}, paths: {}, options: {}, loopback: detail.bind_address === 'loopback', accept: false }
  for (const o of detail.fields.options) state.options[o.key] = o.value
  for (const p of detail.fields.ports) state.ports[p.key] = String(p.value)
  for (const e of detail.fields.env) state.env[e.key] = e.secret ? '' : e.value
  for (const p of detail.fields.paths) state.paths[p.key] = p.value
  return state
}

type Errors = Record<string, string>

function validate(detail: AppDetail, form: FormState, needAccept: boolean): Errors {
  const errors: Errors = {}
  for (const p of detail.fields.ports) {
    if (p.option && !form.options[p.option]) continue
    const n = Number(form.ports[p.key])
    if (!Number.isInteger(n) || n < 1 || n > 65535) errors['port:' + p.key] = t('portInvalid')
  }
  for (const e of detail.fields.env) {
    const value = (form.env[e.key] ?? '').trim()
    const satisfied = value !== '' || (e.secret && (e.has_value || e.generated))
    if (e.required && !satisfied) errors['env:' + e.key] = t('required')
  }
  for (const p of detail.fields.paths) {
    const value = (form.paths[p.key] ?? '').trim()
    if (value === '') {
      if (p.required) errors['path:' + p.key] = t('required')
    } else if (!value.startsWith('/')) {
      errors['path:' + p.key] = t('pathInvalid')
    }
  }
  if (needAccept && !form.accept) errors.accept = t('acceptRisksRequired')
  return errors
}

function toBody(detail: AppDetail, form: FormState): InputsBody {
  const body: InputsBody = { ports: {}, env: {}, paths: {}, options: {}, bind_address: form.loopback ? 'loopback' : 'all', accept_risks: form.accept }
  for (const o of detail.fields.options) body.options[o.key] = Boolean(form.options[o.key])
  for (const p of detail.fields.ports) {
    if (p.fixed || (p.option && !form.options[p.option])) continue
    body.ports[p.key] = Number(form.ports[p.key])
  }
  for (const e of detail.fields.env) {
    const value = form.env[e.key] ?? ''
    // An empty secret is not sent: the server keeps or generates the value.
    if (e.secret && value === '') continue
    body.env[e.key] = value
  }
  for (const p of detail.fields.paths) body.paths[p.key] = (form.paths[p.key] ?? '').trim()
  return body
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <h3 className="text-xs font-semibold tracking-wide text-faint uppercase">{title}</h3>
      {children}
    </section>
  )
}

function SecretInput({ id, field, value, onChange, invalid }: { id: string; field: EnvField; value: string; onChange: (v: string) => void; invalid: boolean }) {
  const [visible, setVisible] = useState(false)
  return (
    <div className="flex items-center gap-1">
      <Input
        id={id}
        type={visible ? 'text' : 'password'}
        value={value}
        invalid={invalid}
        autoComplete="new-password"
        spellCheck={false}
        placeholder={field.has_value ? t('secretStored') : undefined}
        onChange={(e) => onChange(e.target.value)}
      />
      <IconButton icon={visible ? EyeOff : Eye} label={visible ? t('hideSecret') : t('showSecret')} onClick={() => setVisible((v) => !v)} />
    </div>
  )
}

function Form({
  detail,
  mode,
  form,
  errors,
  serverError,
  formId,
  onChange,
  onSubmit,
}: {
  detail: AppDetail
  mode: AppDialogMode
  form: FormState
  errors: Errors
  serverError: string | null
  formId: string
  onChange: (next: FormState) => void
  onSubmit: (e: FormEvent) => void
}) {
  const id = useId()
  const { env, paths, options } = detail.fields
  const ports = detail.fields.ports.filter((p) => !p.option || form.options[p.option])
  const [browsing, setBrowsing] = useState<PathField | null>(null)
  const multi = detail.services.length > 1
  const roots = detail.allowed_roots.join(', ')
  const set = (group: 'ports' | 'env' | 'paths', key: string, value: string) =>
    onChange({ ...form, [group]: { ...form[group], [key]: value } })

  const portHint = (p: PortField) =>
    p.fixed ? t('portFixed') : t('portHint', { container: p.container, protocol: p.protocol }) + (multi ? ` · ${p.service}` : '')
  const envHint = (e: EnvField) => {
    const parts = [e.description]
    if (e.secret && e.has_value) parts.push(t('secretKeep'))
    else if (e.secret && e.generated) parts.push(t('secretGenerated'))
    return parts.filter(Boolean).join(' ')
  }
  const pathHint = (p: PathField) =>
    [p.description, t('pathHint', { target: p.target, roots }), p.required ? '' : t('pathOptional')].filter(Boolean).join(' ')

  return (
    <form id={formId} onSubmit={onSubmit} noValidate className="flex flex-col gap-6">
      <div className="flex items-start gap-3">
        <AppIcon icon={detail.icon} size="lg" />
        <div className="min-w-0 flex-1">
          <p className="text-sm text-fg">{detail.long_description || detail.description}</p>
          <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted">
            <Badge>{detail.category_label}</Badge>
            {detail.version && (
              <span>
                {t('version')}: {detail.version}
              </span>
            )}
            {detail.architectures.length > 0 && (
              <span>
                {t('architectures')}: {detail.architectures.join(', ')}
              </span>
            )}
            {detail.website && (
              <a href={detail.website} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-accent hover:underline">
                {t('website')}
                <ExternalLink className="size-3" aria-hidden />
              </a>
            )}
          </div>
          <p className="mt-2 font-mono text-[11px] break-all text-faint">{detail.images.join(' · ')}</p>
        </div>
      </div>

      {mode === 'settings' && <Alert tone="accent">{t('settingsWarning')}</Alert>}

      {mode === 'install' && !detail.installable && (
        <Alert tone="danger" title={t('unsupported')}>
          {detail.unsupported_reason}
        </Alert>
      )}

      {detail.warnings.length > 0 && (
        <Section title={t('securityWarnings')}>
          {detail.warnings.map((w) => (
            <Alert key={w.code + w.message} tone={w.level === 'danger' ? 'danger' : 'warning'} title={w.title}>
              {w.message}
            </Alert>
          ))}
          {detail.requires_risk_acceptance && (
            <div>
              <label className="flex min-h-10 cursor-pointer items-center gap-2.5 text-sm text-fg">
                <input
                  type="checkbox"
                  checked={form.accept}
                  onChange={(e) => onChange({ ...form, accept: e.target.checked })}
                  className="size-4 accent-accent"
                />
                {t('acceptRisks')}
              </label>
              {errors.accept && (
                <p role="alert" className="text-xs text-danger">
                  {errors.accept}
                </p>
              )}
            </div>
          )}
        </Section>
      )}

      {detail.notes && mode === 'install' && (
        <Section title={t('notes')}>
          <div className="rounded-xl border border-line bg-surface px-3 py-2.5 text-xs leading-relaxed whitespace-pre-line text-muted">
            {detail.notes}
          </div>
        </Section>
      )}

      {options.length > 0 && (
        <Section title={t('sectionOptions')}>
          {options.map((o) => (
            <div key={o.key} className="flex items-start gap-3 rounded-xl border border-line bg-surface px-3 py-3">
              <Switch
                checked={Boolean(form.options[o.key])}
                onChange={(checked) => onChange({ ...form, options: { ...form.options, [o.key]: checked } })}
                label={o.label}
              />
              <div className="min-w-0 text-xs">
                <p className="text-sm font-medium text-fg">{o.label}</p>
                {o.description && <p className="mt-0.5 text-muted">{o.description}</p>}
                <p className="mt-1 text-warning">{t('optionCaps', { caps: o.cap_add.join(', ') })}</p>
              </div>
            </div>
          ))}
        </Section>
      )}

      {(detail.publishes_ports || detail.host_network) && (
        <Section title={t('sectionAccess')}>
          {detail.publishes_ports &&
            (form.loopback ? (
              <Alert tone="accent">{t('exposureLoopback')}</Alert>
            ) : (
              <Alert tone="warning" title={t('exposureTitle')}>
                {t('exposureText')}
              </Alert>
            ))}
          {detail.host_network && (
            <Alert tone="warning" title={t('exposureHostTitle')}>
              {t('exposureHostText')}
            </Alert>
          )}
          {detail.publishes_ports && (
            <div className="flex items-start gap-3 rounded-xl border border-line bg-surface px-3 py-3">
              <Switch checked={form.loopback} onChange={(checked) => onChange({ ...form, loopback: checked })} label={t('loopbackOnly')} />
              <div className="min-w-0 text-xs">
                <p className="text-sm font-medium text-fg">{t('loopbackOnly')}</p>
                <p className="mt-0.5 text-muted">{t('loopbackOnlyText')}</p>
                {detail.host_network && <p className="mt-0.5 text-muted">{t('loopbackHostNote')}</p>}
              </div>
            </div>
          )}
          {mode === 'settings' && detail.installed_app && detail.installed_app.ports.length > 0 && (
            <div className="text-xs text-muted">
              <p className="mb-1 font-medium">{t('publishedPorts')}</p>
              <ul className="flex flex-col gap-1">
                {detail.installed_app.ports.map((p) => (
                  <li key={p.service + p.host + p.protocol} className="flex flex-wrap items-center gap-x-2">
                    <span className="font-mono text-fg">{portAddress(p)}</span>
                    <span aria-hidden>→</span>
                    <span className="font-mono">
                      {p.container}/{p.protocol}
                    </span>
                    <span>
                      ({p.host_network ? t('addrHost') : p.host_ip === '127.0.0.1' ? t('addrLoopback') : t('addrAll')}
                      {p.label ? ` · ${p.label}` : ''})
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </Section>
      )}

      {ports.length > 0 && (
        <Section title={t('sectionPorts')}>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {ports.map((p) => (
              <Field key={p.key} label={p.label} htmlFor={`${id}-port-${p.key}`} hint={portHint(p)} error={errors['port:' + p.key]}>
                <Input
                  id={`${id}-port-${p.key}`}
                  type="number"
                  inputMode="numeric"
                  min={1}
                  max={65535}
                  disabled={p.fixed}
                  invalid={Boolean(errors['port:' + p.key])}
                  value={form.ports[p.key] ?? ''}
                  onChange={(e) => set('ports', p.key, e.target.value)}
                />
              </Field>
            ))}
          </div>
        </Section>
      )}

      {paths.length > 0 && (
        <Section title={t('sectionPaths')}>
          {paths.map((p) => (
            <Field
              key={p.key}
              label={p.label + (p.read_only ? ` (${t('readOnly')})` : '')}
              htmlFor={`${id}-path-${p.key}`}
              hint={pathHint(p)}
              error={errors['path:' + p.key]}
            >
              <div className="flex items-center gap-2">
                <Input
                  id={`${id}-path-${p.key}`}
                  value={form.paths[p.key] ?? ''}
                  invalid={Boolean(errors['path:' + p.key])}
                  placeholder={p.default || '/data/…'}
                  spellCheck={false}
                  autoComplete="off"
                  className="min-w-0 font-mono"
                  onChange={(e) => set('paths', p.key, e.target.value)}
                />
                <Button icon={FolderOpen} onClick={() => setBrowsing(p)} className="shrink-0">
                  {t('browse')}
                </Button>
              </div>
            </Field>
          ))}
          <FolderPicker
            open={browsing !== null}
            onClose={() => setBrowsing(null)}
            onSelect={(path) => {
              if (browsing) set('paths', browsing.key, path)
              setBrowsing(null)
            }}
            title={browsing ? t('browseTitle', { label: browsing.label }) : ''}
            confirmLabel={t('browseConfirm')}
            allowCreate
          />
        </Section>
      )}

      {env.length > 0 && (
        <Section title={t('sectionEnv')}>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {env.map((e) => (
              <Field key={e.key} label={e.label + (e.required ? ' *' : '')} htmlFor={`${id}-env-${e.key}`} hint={envHint(e)} error={errors['env:' + e.key]}>
                {e.secret ? (
                  <SecretInput
                    id={`${id}-env-${e.key}`}
                    field={e}
                    value={form.env[e.key] ?? ''}
                    invalid={Boolean(errors['env:' + e.key])}
                    onChange={(v) => set('env', e.key, v)}
                  />
                ) : (
                  <Input
                    id={`${id}-env-${e.key}`}
                    value={form.env[e.key] ?? ''}
                    invalid={Boolean(errors['env:' + e.key])}
                    spellCheck={false}
                    autoComplete="off"
                    onChange={(ev) => set('env', e.key, ev.target.value)}
                  />
                )}
              </Field>
            ))}
          </div>
        </Section>
      )}

      {ports.length + paths.length + env.length + options.length === 0 && <p className="text-sm text-muted">{t('noFields')}</p>}

      {detail.volumes.length > 0 && (
        <Section title={t('sectionVolumes')}>
          <ul className="flex flex-col gap-1.5 text-xs text-muted">
            {detail.volumes.map((v) => (
              <li key={v.service + v.target} className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <Badge tone={v.type === 'system' ? 'danger' : 'neutral'}>{v.type === 'system' ? t('volumeSystem') : t('volumeNamed')}</Badge>
                <span className="font-mono break-all text-fg">{v.source}</span>
                <span aria-hidden>→</span>
                <span className="font-mono break-all">{v.target}</span>
                {v.read_only && <span>({t('readOnly')})</span>}
              </li>
            ))}
          </ul>
        </Section>
      )}

      {serverError && <Alert tone="danger">{serverError}</Alert>}
    </form>
  )
}

/** Install dialog and settings dialog: the manifest's configurable fields,
 *  notes and warnings, followed by the live progress of the job. */
export function AppDialog({
  slug,
  mode,
  onClose,
  onChanged,
}: {
  /** null closes the dialog. */
  slug: string | null
  mode: AppDialogMode
  onClose: () => void
  onChanged: () => void
}) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const formId = useId()
  const detail = useQuery<AppDetail>(slug ? `/apps/catalog/${slug}` : null)
  const [form, setForm] = useState<FormState | null>(null)
  const [errors, setErrors] = useState<Errors>({})
  const [job, setJob] = useState<Job | null>(null)
  const [done, setDone] = useState(false)

  const data = detail.data && detail.data.slug === slug ? detail.data : undefined

  useEffect(() => {
    setForm(null)
    setErrors({})
    setJob(null)
    setDone(false)
  }, [slug, mode])

  useEffect(() => {
    if (data && form === null) setForm(initialState(data))
  }, [data, form])

  const submit = useAction(
    (body: InputsBody) =>
      mode === 'install'
        ? api.post<Job>(`/apps/catalog/${slug}/install`, body)
        : api.put<Job>(`/apps/installed/${slug}/settings`, body),
    {
      onSuccess: (started) => {
        setJob(started)
        onChanged()
        if (mode === 'install') toast.info(t('installStarted', { name: started.name }))
      },
    },
  )

  const needAccept = Boolean(data?.requires_risk_acceptance)
  const blocked = data && !isAdmin ? t('adminOnly') : null

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!data || !form || submit.pending) return
    const found = validate(data, form, needAccept)
    setErrors(found)
    if (Object.keys(found).length > 0) return
    void submit.run(toBody(data, form))
  }

  const title = data
    ? t(mode === 'install' ? (isAdmin ? 'installTitle' : 'detailsTitle') : 'settingsTitle', { name: data.name })
    : t(mode === 'install' ? 'install' : 'settings')

  let body: ReactNode
  let footer: ReactNode
  if (job) {
    body = (
      <JobProgress
        jobId={job.id}
        onFinished={(finished) => {
          setDone(true)
          onChanged()
          if (finished.status === 'success' && mode === 'install') toast.success(t('installDone', { name: finished.name }))
        }}
      />
    )
    footer = (
      <Button variant={done ? 'primary' : 'secondary'} onClick={onClose}>
        {t('close')}
      </Button>
    )
  } else if (detail.loading || (!data && !detail.error)) {
    body = <LoadingState />
  } else if (detail.error || !data || !form) {
    body = <ErrorState message={detail.error ?? ''} onRetry={() => void detail.reload()} />
  } else {
    const alreadyInstalled = mode === 'install' && (data.installed || !data.installable)
    body = (
      <>
        {blocked && (
          <Alert tone="accent" className="mb-4">
            {blocked}
          </Alert>
        )}
        <fieldset disabled={!isAdmin || alreadyInstalled || submit.pending} className="min-w-0">
          <Form
            detail={data}
            mode={mode}
            form={form}
            errors={errors}
            serverError={submit.error}
            formId={formId}
            onChange={(next) => {
              setForm(next)
              if (Object.keys(errors).length > 0) setErrors(validate(data, next, needAccept))
            }}
            onSubmit={onSubmit}
          />
        </fieldset>
      </>
    )
    footer = (
      <>
        <Button variant="ghost" onClick={onClose} disabled={submit.pending}>
          {isAdmin ? t('cancel') : t('close')}
        </Button>
        {isAdmin && !alreadyInstalled && (
          <Button type="submit" form={formId} variant="primary" loading={submit.pending} disabled={needAccept && !form.accept}>
            {mode === 'install' ? t('startInstall') : t('applySettings')}
          </Button>
        )}
      </>
    )
  }

  return (
    <Modal open={slug !== null} onClose={onClose} title={title} size="lg" busy={submit.pending} footer={footer}>
      {body}
    </Modal>
  )
}
