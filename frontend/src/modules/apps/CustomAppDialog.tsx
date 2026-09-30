import { useEffect, useId, useRef, useState, type ChangeEvent, type ReactNode } from 'react'
import { FileUp } from 'lucide-react'
import { Alert, Badge, Button, Field, Input, Modal } from '@/components/ui'
import { useAction } from '@/hooks/useApi'
import { formatBytes } from '@/lib/format'
import { api } from '@/services/api'
import { toast } from '@/stores/ui'
import { AppIcon } from './parts'
import { t } from './strings'
import type { CatalogApp, CustomPreview, CustomRequest } from './types'

/** Largest compose file the server accepts (MaxComposeBytes). */
export const MAX_COMPOSE_BYTES = 64 * 1024

const textareaClass =
  'min-h-64 w-full resize-y rounded-xl border border-line bg-surface px-3 py-2.5 font-mono text-xs leading-relaxed text-fg ' +
  'placeholder:text-faint transition-colors hover:border-line-strong focus:border-accent focus:outline-none disabled:opacity-50'

const PLACEHOLDER = `services:
  uygulama:
    image: ghcr.io/kurum/uygulama:1.0
    ports:
      - "8080:80"
    volumes:
      - veri:/data
volumes:
  veri:`

function byteLength(text: string): number {
  return new TextEncoder().encode(text).length
}

/** A refused file comes back as one message: a summary line followed by one
 *  "• " line per problem. */
export function splitProblems(message: string): { title: string; items: string[] } {
  const lines = message.split('\n')
  const items = lines
    .slice(1)
    .filter((l) => l.startsWith('• '))
    .map((l) => l.slice(2))
  return { title: lines[0] ?? message, items }
}

function readFile(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(typeof reader.result === 'string' ? reader.result : '')
    reader.onerror = () => reject(reader.error)
    reader.readAsText(file)
  })
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-xs font-semibold tracking-wide text-faint uppercase">{title}</h3>
      {children}
    </section>
  )
}

function Row({ children }: { children: ReactNode }) {
  return <li className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-xl border border-line bg-surface px-3 py-2 text-xs text-muted">{children}</li>
}

function PreviewView({ preview }: { preview: CustomPreview }) {
  const multi = preview.services.length > 1
  const { ports, paths, env } = preview.fields
  const volumes = preview.volumes
  return (
    <div className="flex flex-col gap-5">
      <div className="flex items-start gap-3">
        <AppIcon icon={preview.icon} size="lg" />
        <div className="min-w-0 flex-1">
          <p className="flex flex-wrap items-center gap-2 text-sm font-semibold text-fg">
            <span className="break-all">{preview.name}</span>
            <Badge tone="purple">{t('customBadge')}</Badge>
          </p>
          <p className="mt-1 font-mono text-[11px] break-all text-faint">{preview.slug}</p>
        </div>
      </div>

      {preview.warnings.length > 0 && (
        <Section title={t('securityWarnings')}>
          {preview.warnings.map((w) => (
            <Alert key={w.code + w.message} tone={w.level === 'danger' ? 'danger' : 'warning'} title={w.title}>
              {w.message}
            </Alert>
          ))}
          {preview.requires_risk_acceptance && <p className="text-xs text-muted">{t('customRiskNote')}</p>}
        </Section>
      )}

      <Section title={t('customServices')}>
        <ul className="flex flex-col gap-1.5">
          {preview.services.map((s) => (
            <Row key={s.name}>
              <span className="font-medium text-fg">{s.name}</span>
              <span className="font-mono break-all">{s.image}</span>
              {s.depends_on.length > 0 && <span>({t('customDependsOn', { services: s.depends_on.join(', ') })})</span>}
            </Row>
          ))}
        </ul>
      </Section>

      <Section title={t('customPorts')}>
        {ports.length === 0 ? (
          <p className="text-xs text-muted">{t('customNoPorts')}</p>
        ) : (
          <ul className="flex flex-col gap-1.5">
            {ports.map((p) => (
              <Row key={p.key}>
                <span className="font-mono text-fg">
                  {p.default} → {p.container}/{p.protocol}
                </span>
                {multi && <span>· {p.service}</span>}
                {p.label && <span>· {p.label}</span>}
                {p.web_ui && <Badge tone="accent">{t('customWebUI')}</Badge>}
                {p.fixed && <Badge tone="warning">{t('addrHost')}</Badge>}
              </Row>
            ))}
          </ul>
        )}
        {preview.publishes_ports && preview.bind_address === 'loopback' && <p className="text-xs text-muted">{t('customLoopback')}</p>}
      </Section>

      <Section title={t('customVolumes')}>
        {volumes.length + paths.length === 0 ? (
          <p className="text-xs text-muted">{t('customNoVolumes')}</p>
        ) : (
          <ul className="flex flex-col gap-1.5">
            {volumes.map((v) => (
              <Row key={v.service + v.target}>
                <Badge tone={v.type === 'system' ? 'danger' : 'neutral'}>{v.type === 'system' ? t('volumeSystem') : t('volumeNamed')}</Badge>
                <span className="font-mono break-all text-fg">{v.source}</span>
                <span aria-hidden>→</span>
                <span className="font-mono break-all">{v.target}</span>
                {v.read_only && <span>({t('readOnly')})</span>}
              </Row>
            ))}
            {paths.map((p) => (
              <Row key={p.key}>
                <Badge tone="cyan">{t('customFolders')}</Badge>
                <span className="font-mono break-all text-fg">{p.default}</span>
                <span aria-hidden>→</span>
                <span className="font-mono break-all">{p.target}</span>
                {p.read_only && <span>({t('readOnly')})</span>}
              </Row>
            ))}
          </ul>
        )}
      </Section>

      <Section title={t('customEnv')}>
        {env.length === 0 ? (
          <p className="text-xs text-muted">{t('customNoEnv')}</p>
        ) : (
          <ul className="flex flex-col gap-1.5">
            {env.map((e) => (
              <Row key={e.key}>
                <span className="font-mono text-fg">{e.name}</span>
                {!e.secret && e.default !== '' && <span className="font-mono break-all">= {e.default}</span>}
                {e.secret && <Badge tone="warning">{t('customSecret')}</Badge>}
                {e.generated && <Badge>{t('customGenerated')}</Badge>}
                {e.required && <Badge tone="danger">{t('customRequired')}</Badge>}
              </Row>
            ))}
          </ul>
        )}
      </Section>

      {preview.conversion_notes.length > 0 && (
        <Section title={t('customNotes')}>
          <ul className="flex list-disc flex-col gap-1 pl-5 text-xs text-muted">
            {preview.conversion_notes.map((n, i) => (
              <li key={i} className="break-words">
                {n.service && <span className="font-medium text-fg">{n.service}</span>}
                {n.service && n.key && ' · '}
                {n.key && <span className="font-mono">{n.key}</span>}
                {(n.service || n.key) && ': '}
                {n.message}
              </li>
            ))}
          </ul>
        </Section>
      )}

      <details className="rounded-xl border border-line bg-surface px-3 py-2 text-xs text-muted">
        <summary className="min-h-8 cursor-pointer content-center select-none">{t('customYaml')}</summary>
        <pre className="mt-2 max-h-72 overflow-auto font-mono text-[11px] leading-relaxed whitespace-pre text-fg">{preview.manifest_yaml}</pre>
      </details>
    </div>
  )
}

/** "Özel Uygulama Ekle": paste or upload a compose file, preview what it
 *  becomes, save it into the store. Installing happens afterwards through
 *  the ordinary install dialog. */
export function CustomAppDialog({ open, onClose, onSaved }: { open: boolean; onClose: () => void; onSaved: (app: CatalogApp) => void }) {
  const id = useId()
  const fileInput = useRef<HTMLInputElement>(null)
  const [name, setName] = useState('')
  const [compose, setCompose] = useState('')
  const [inputError, setInputError] = useState<string | null>(null)
  const [preview, setPreview] = useState<CustomPreview | null>(null)
  const maxSize = formatBytes(MAX_COMPOSE_BYTES)

  useEffect(() => {
    if (!open) return
    setName('')
    setCompose('')
    setInputError(null)
    setPreview(null)
  }, [open])

  const previewAction = useAction((body: CustomRequest) => api.post<CustomPreview>('/apps/custom/preview', body), {
    onSuccess: (res) => setPreview(res),
  })
  const saveAction = useAction((body: CustomRequest) => api.post<CatalogApp>('/apps/custom', body), {
    onSuccess: (app) => {
      toast.success(t('customSaved', { name: app.name }))
      onSaved(app)
    },
  })
  const { clearError: clearPreviewError } = previewAction
  const { clearError: clearSaveError } = saveAction
  useEffect(() => {
    if (!open) return
    clearPreviewError()
    clearSaveError()
  }, [open, clearPreviewError, clearSaveError])

  const busy = previewAction.pending || saveAction.pending

  const onFile = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    if (!/\.ya?ml$/i.test(file.name)) {
      setInputError(t('customFileType'))
      return
    }
    if (file.size > MAX_COMPOSE_BYTES) {
      setInputError(t('customFileTooBig', { size: maxSize }))
      return
    }
    try {
      const text = await readFile(file)
      setCompose(text)
      setInputError(null)
      previewAction.clearError()
      toast.info(t('customFileLoaded', { name: file.name }))
    } catch {
      setInputError(t('customFileUnreadable'))
    }
  }

  const runPreview = () => {
    if (busy) return
    if (compose.trim() === '') {
      setInputError(t('customComposeRequired'))
      return
    }
    if (byteLength(compose) > MAX_COMPOSE_BYTES) {
      setInputError(t('customTooBig', { size: maxSize }))
      return
    }
    setInputError(null)
    void previewAction.run({ name: name.trim(), compose })
  }

  const save = () => {
    if (!preview || busy) return
    void saveAction.run({ name: name.trim(), compose, digest: preview.digest })
  }

  const back = () => {
    setPreview(null)
    saveAction.clearError()
  }

  const problems = previewAction.error ? splitProblems(previewAction.error) : null

  let body: ReactNode
  let footer: ReactNode
  if (preview) {
    body = (
      <div className="flex flex-col gap-4">
        <PreviewView preview={preview} />
        {saveAction.error && <Alert tone="danger">{saveAction.error}</Alert>}
      </div>
    )
    footer = (
      <>
        <Button variant="ghost" onClick={back} disabled={busy}>
          {t('customBack')}
        </Button>
        <Button variant="primary" onClick={save} loading={saveAction.pending}>
          {t('customSave')}
        </Button>
      </>
    )
  } else {
    body = (
      <div className="flex flex-col gap-4">
        <p className="text-sm text-muted">{t('customIntro')}</p>
        <Alert tone="warning">{t('customTrust')}</Alert>
        <Field label={t('customName')} htmlFor={`${id}-name`} hint={t('customNameHint')}>
          <Input id={`${id}-name`} value={name} maxLength={60} autoComplete="off" onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label={t('customCompose')} htmlFor={`${id}-compose`} hint={t('customComposeHint', { size: maxSize })} error={inputError}>
          <textarea
            id={`${id}-compose`}
            value={compose}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            wrap="off"
            placeholder={PLACEHOLDER}
            aria-invalid={Boolean(inputError) || undefined}
            className={textareaClass}
            onChange={(e) => {
              setCompose(e.target.value)
              if (inputError) setInputError(null)
            }}
          />
        </Field>
        <div>
          <input
            ref={fileInput}
            type="file"
            accept=".yml,.yaml,application/yaml,application/x-yaml,text/yaml"
            className="sr-only"
            tabIndex={-1}
            aria-hidden
            data-testid="compose-file"
            onChange={(e) => void onFile(e)}
          />
          <Button icon={FileUp} onClick={() => fileInput.current?.click()} disabled={busy}>
            {t('customFile')}
          </Button>
        </div>
        {problems && (
          <Alert tone="danger" title={problems.items.length > 0 ? t('customProblems') : undefined}>
            {problems.items.length > 0 ? (
              <>
                <p>{problems.title}</p>
                <ul className="mt-1.5 flex list-disc flex-col gap-1 pl-5">
                  {problems.items.map((p, i) => (
                    <li key={i} className="break-words">
                      {p}
                    </li>
                  ))}
                </ul>
                <p className="mt-1.5">{t('customProblemsHint')}</p>
              </>
            ) : (
              problems.title
            )}
          </Alert>
        )}
      </div>
    )
    footer = (
      <>
        <Button variant="ghost" onClick={onClose} disabled={busy}>
          {t('cancel')}
        </Button>
        <Button variant="primary" onClick={runPreview} loading={previewAction.pending}>
          {t('customPreview')}
        </Button>
      </>
    )
  }

  return (
    <Modal open={open} onClose={onClose} title={t('customTitle')} size="xl" busy={busy} footer={footer}>
      {body}
    </Modal>
  )
}
