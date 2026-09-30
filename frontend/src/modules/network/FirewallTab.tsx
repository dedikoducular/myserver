import { useEffect, useId, useMemo, useState } from 'react'
import { Plus, RefreshCw, Shield, ShieldAlert, ShieldCheck, ShieldOff, Trash2 } from 'lucide-react'
import {
  Alert,
  Badge,
  Button,
  Card,
  CardHeader,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  IconTile,
  Input,
  KeyValueList,
  LoadingState,
  Modal,
  Select,
  Status,
  TableWrap,
  tableClass,
  type Tone,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { api } from '@/services/api'
import { cx } from '@/lib/format'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { t } from './strings'
import type {
  EnableCheck,
  FirewallRule,
  FirewallState,
  LanSource,
  RuleAction,
  RuleProtocol,
  Suggestions,
} from './types'

/* ---------- labels ---------- */

function actionLabel(a: string): string {
  switch (a) {
    case 'allow':
      return t('actionAllow')
    case 'deny':
      return t('actionDeny')
    case 'reject':
      return t('actionReject')
    case 'limit':
      return t('actionLimit')
    default:
      return a
  }
}

function actionHint(a: RuleAction): string {
  switch (a) {
    case 'allow':
      return t('actionAllowHint')
    case 'deny':
      return t('actionDenyHint')
    case 'reject':
      return t('actionRejectHint')
    default:
      return t('actionLimitHint')
  }
}

const actionTone: Record<string, Tone> = { allow: 'success', limit: 'cyan', deny: 'danger', reject: 'warning' }

function policyLabel(p: string | null): string {
  switch (p) {
    case 'allow':
      return t('policyAllow')
    case 'deny':
      return t('policyDeny')
    case 'reject':
      return t('policyReject')
    case 'disabled':
      return t('policyDisabled')
    default:
      return t('unknown')
  }
}

function protocolLabel(p: string): string {
  if (p === 'any' || p === '') return t('ruleAnyProto')
  return p.toUpperCase()
}

function sourceOptionLabel(s: LanSource): string {
  return s.kind === 'subnet' ? t('sourceSubnet', { cidr: s.cidr, iface: s.interface }) : t('sourceBlock', { cidr: s.cidr })
}

/* ---------- client-side validation (the server validates again) ---------- */

function parsePort(text: string): { port: string; range: boolean; lo: number; hi: number } | null {
  const m = /^(\d{1,5})(?:[:-](\d{1,5}))?$/.exec(text.trim())
  if (!m) return null
  const lo = Number(m[1])
  const hi = m[2] === undefined ? lo : Number(m[2])
  if (lo < 1 || lo > 65535 || hi < 1 || hi > 65535) return null
  if (m[2] !== undefined && lo >= hi) return null
  return { port: m[2] === undefined ? String(lo) : `${lo}:${hi}`, range: m[2] !== undefined, lo, hi }
}

function validSource(text: string): boolean {
  const s = text.trim()
  if (s.length === 0 || s.length > 64) return false
  const [addr, bits, extra] = s.split('/')
  if (extra !== undefined || !addr) return false
  const v4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(addr)
  const isV4 = v4 !== null && v4.slice(1).every((o) => Number(o) <= 255)
  const isV6 = !isV4 && addr.includes(':') && /^[0-9a-fA-F:.]+$/.test(addr)
  if (!isV4 && !isV6) return false
  if (bits === undefined) return true
  if (!/^\d{1,3}$/.test(bits)) return false
  const n = Number(bits)
  return n >= 1 && n <= (isV4 ? 32 : 128)
}

const commentPattern = /^[A-Za-z0-9 _.,:()/+-]{0,64}$/

/* ---------- enable dialog ---------- */

function EnableDialog({
  check,
  onClose,
  onDone,
}: {
  check: EnableCheck | null
  onClose: () => void
  onDone: () => void
}) {
  const [sources, setSources] = useState<Record<string, string>>({})
  const baseId = useId()

  useEffect(() => {
    if (!check) return
    const next: Record<string, string> = {}
    for (const m of check.missing) next[`${m.kind}-${m.port}`] = m.suggested_source
    setSources(next)
  }, [check])

  const enable = useAction(
    (body: { add_rules: Array<{ kind: string; port: number; source: string }> }) =>
      api.post<FirewallState | null>('/firewall/enable', body),
    {
      onSuccess: () => {
        toast.success(t('enabledToast'))
        onDone()
      },
    },
  )

  const missing = check?.missing ?? []
  const anyChosen = missing.some((m) => (sources[`${m.kind}-${m.port}`] ?? m.suggested_source) === 'any')
  const lanChosen = missing.some((m) => (sources[`${m.kind}-${m.port}`] ?? m.suggested_source) !== 'any')

  return (
    <ConfirmDialog
      open={check !== null}
      onClose={() => {
        enable.clearError()
        onClose()
      }}
      title={t('enableTitle')}
      danger
      confirmLabel={missing.length > 0 ? t('enableConfirmAdd') : t('enableConfirm')}
      pending={enable.pending}
      error={enable.error}
      onConfirm={async () => {
        await enable.run({
          add_rules: missing.map((m) => ({
            kind: m.kind,
            port: m.port,
            source: sources[`${m.kind}-${m.port}`] ?? m.suggested_source,
          })),
        })
      }}
      message={
        <div className="flex flex-col gap-3">
          <p>{t('enableMessage')}</p>
          {missing.length === 0 ? (
            <p className="text-xs text-success">{t('enableReady')}</p>
          ) : (
            <>
              <p className="text-xs text-muted">{t('enableMissing')}</p>
              {missing.map((m, i) => {
                const key = `${m.kind}-${m.port}`
                const name =
                  m.kind === 'ssh'
                    ? t('enableMissingSsh', { port: m.port, protocol: m.protocol })
                    : t('enableMissingPanel', { port: m.port, protocol: m.protocol })
                const id = `${baseId}-${i}`
                return (
                  <Field key={key} label={t('enableSourceLabel', { name })} htmlFor={id}>
                    <Select
                      id={id}
                      value={sources[key] ?? m.suggested_source}
                      onChange={(e) => setSources((s) => ({ ...s, [key]: e.target.value }))}
                      disabled={enable.pending}
                    >
                      {check?.lan_sources.map((s) => (
                        <option key={s.cidr} value={s.cidr}>
                          {sourceOptionLabel(s)}
                        </option>
                      ))}
                      <option value="any">{t('sourceAny')}</option>
                    </Select>
                  </Field>
                )
              })}
              <p className="text-xs text-faint">{t('enableSshNote')}</p>
            </>
          )}
        </div>
      }
      warning={
        anyChosen ? (
          t('enableAnyWarn')
        ) : lanChosen && check && check.client_ip && !check.client_in_lan ? (
          t('enableClientOutside', { ip: check.client_ip })
        ) : undefined
      }
    />
  )
}

/* ---------- add rule ---------- */

interface RuleForm {
  service: string
  port: string
  protocol: RuleProtocol
  sourceMode: string // a CIDR from the suggestions, 'custom' or 'any'
  customSource: string
  action: RuleAction
  comment: string
}

function AddRuleDialog({
  open,
  onClose,
  onDone,
  state,
  suggestions,
}: {
  open: boolean
  onClose: () => void
  onDone: () => void
  state: FirewallState
  suggestions: Suggestions | undefined
}) {
  const lan = suggestions?.lan_sources ?? []
  const defaultMode = suggestions && suggestions.default_source !== 'any' ? suggestions.default_source : 'custom'
  const initial: RuleForm = {
    service: 'custom',
    port: '',
    protocol: 'tcp',
    sourceMode: defaultMode,
    customSource: '',
    action: 'allow',
    comment: '',
  }
  const [form, setForm] = useState<RuleForm>(initial)
  const [errors, setErrors] = useState<Partial<Record<'port' | 'source' | 'comment', string>>>({})
  const [confirming, setConfirming] = useState(false)
  const id = useId()

  useEffect(() => {
    if (open) {
      setForm(initial)
      setErrors({})
      setConfirming(false)
    }
    // Reset only when the dialog opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, defaultMode])

  const add = useAction(
    (body: Record<string, unknown>) => api.post<FirewallState | null>('/firewall/rules', body),
    {
      onSuccess: () => {
        toast.success(t('addedToast'))
        setConfirming(false)
        onDone()
      },
    },
  )

  const preset = suggestions?.services.find((s) => s.id === form.service)
  const parsed = parsePort(form.port)
  const source = form.sourceMode === 'any' ? 'any' : form.sourceMode === 'custom' ? form.customSource.trim() : form.sourceMode

  const risky = useMemo(() => {
    if (!parsed || (form.action !== 'deny' && form.action !== 'reject') || form.protocol === 'udp') return false
    const ports = [...state.ssh_ports, ...(state.panel_port_required ? [state.panel_port] : [])]
    return ports.some((p) => p >= parsed.lo && p <= parsed.hi)
  }, [parsed, form.action, form.protocol, state])

  const pickService = (serviceId: string) => {
    const s = suggestions?.services.find((x) => x.id === serviceId)
    if (!s) {
      setForm((f) => ({ ...f, service: 'custom' }))
      return
    }
    setForm((f) => ({
      ...f,
      service: s.id,
      port: s.port,
      protocol: s.protocol,
      // LAN services always go back to the LAN suggestion.
      sourceMode: s.lan_only ? defaultMode : f.sourceMode,
    }))
  }

  const validate = (): boolean => {
    const next: typeof errors = {}
    if (!parsed) next.port = t('errPort')
    else if (parsed.range && form.protocol === 'any') next.port = t('errRangeProto')
    if (form.sourceMode === 'custom' && !validSource(form.customSource)) next.source = t('errSource')
    if (!commentPattern.test(form.comment.trim())) next.comment = t('errComment')
    setErrors(next)
    return Object.keys(next).length === 0
  }

  const summary = parsed
    ? t('addConfirmMessage', {
        action: actionLabel(form.action),
        port: parsed.port,
        protocol: protocolLabel(form.protocol),
        source: source === 'any' ? t('addConfirmFromAny') : t('addConfirmFrom', { source }),
      })
    : ''

  return (
    <>
      <Modal
        open={open && !confirming}
        onClose={onClose}
        title={t('addTitle')}
        footer={
          <>
            <Button variant="ghost" onClick={onClose}>
              {t('cancel')}
            </Button>
            <Button
              variant="primary"
              onClick={() => {
                if (validate()) {
                  add.clearError()
                  setConfirming(true)
                }
              }}
            >
              {t('addContinue')}
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          <Field label={t('addService')} htmlFor={`${id}-service`}>
            <Select id={`${id}-service`} value={form.service} onChange={(e) => pickService(e.target.value)}>
              <option value="custom">{t('addServiceCustom')}</option>
              {suggestions?.services.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name} ({s.port}/{protocolLabel(s.protocol)})
                </option>
              ))}
            </Select>
          </Field>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t('addPort')} htmlFor={`${id}-port`} hint={t('addPortHint')} error={errors.port}>
              <Input
                id={`${id}-port`}
                value={form.port}
                inputMode="numeric"
                autoComplete="off"
                invalid={Boolean(errors.port)}
                onChange={(e) => setForm((f) => ({ ...f, port: e.target.value, service: 'custom' }))}
              />
            </Field>
            <Field label={t('addProtocol')} htmlFor={`${id}-proto`}>
              <Select
                id={`${id}-proto`}
                value={form.protocol}
                onChange={(e) => setForm((f) => ({ ...f, protocol: e.target.value as RuleProtocol }))}
              >
                <option value="tcp">{t('protoTcp')}</option>
                <option value="udp">{t('protoUdp')}</option>
                <option value="any">{t('protoAny')}</option>
              </Select>
            </Field>
          </div>

          <Field label={t('addSource')} htmlFor={`${id}-source`} hint={lan.length === 0 ? t('addNoLan') : undefined}>
            <Select id={`${id}-source`} value={form.sourceMode} onChange={(e) => setForm((f) => ({ ...f, sourceMode: e.target.value }))}>
              {lan.map((s) => (
                <option key={s.cidr} value={s.cidr}>
                  {sourceOptionLabel(s)}
                </option>
              ))}
              <option value="custom">{t('sourceCustom')}</option>
              <option value="any">{t('sourceAny')}</option>
            </Select>
          </Field>

          {form.sourceMode === 'custom' && (
            <Field label={t('addSourceCustom')} htmlFor={`${id}-custom`} hint={t('addSourceCustomHint')} error={errors.source}>
              <Input
                id={`${id}-custom`}
                value={form.customSource}
                autoComplete="off"
                spellCheck={false}
                invalid={Boolean(errors.source)}
                onChange={(e) => setForm((f) => ({ ...f, customSource: e.target.value }))}
              />
            </Field>
          )}

          {form.sourceMode === 'any' && form.action !== 'deny' && form.action !== 'reject' && (
            <Alert tone="danger" title={t('addAnyWarnTitle')}>
              {t('addAnyWarn')}
              {preset?.lan_only && <span className="mt-1 block">{t('addLanOnlyWarn')}</span>}
            </Alert>
          )}

          <Field label={t('addAction')} htmlFor={`${id}-action`} hint={actionHint(form.action)}>
            <Select id={`${id}-action`} value={form.action} onChange={(e) => setForm((f) => ({ ...f, action: e.target.value as RuleAction }))}>
              <option value="allow">{t('actionAllow')}</option>
              <option value="limit">{t('actionLimit')}</option>
              <option value="deny">{t('actionDeny')}</option>
              <option value="reject">{t('actionReject')}</option>
            </Select>
          </Field>

          <Field label={t('addComment')} htmlFor={`${id}-comment`} hint={t('addCommentHint')} error={errors.comment}>
            <Input
              id={`${id}-comment`}
              value={form.comment}
              maxLength={64}
              autoComplete="off"
              invalid={Boolean(errors.comment)}
              onChange={(e) => setForm((f) => ({ ...f, comment: e.target.value }))}
            />
          </Field>
        </div>
      </Modal>

      <ConfirmDialog
        open={open && confirming}
        onClose={() => setConfirming(false)}
        title={t('addConfirmTitle')}
        danger={risky || source === 'any'}
        confirmLabel={t('addConfirm')}
        requireText={risky ? t('addRiskText') : undefined}
        pending={add.pending}
        error={add.error}
        message={
          <div className="flex flex-col gap-2">
            <p>{summary}</p>
            <p className="text-xs text-muted">{state.active ? t('addConfirmApply') : t('addConfirmInactive')}</p>
          </div>
        }
        warning={
          risky
            ? t('addRiskWarn')
            : source === 'any' && form.action !== 'deny' && form.action !== 'reject'
              ? t('addAnyWarn')
              : undefined
        }
        onConfirm={async () => {
          if (!parsed) return
          await add.run({
            port: parsed.port,
            protocol: form.protocol,
            source,
            action: form.action,
            comment: form.comment.trim(),
            acknowledge_access_risk: risky,
          })
        }}
      />
    </>
  )
}

/* ---------- rules table ---------- */

function ruleDetails(r: FirewallRule): string[] {
  const out: string[] = []
  if (r.direction === 'out') out.push(t('ruleOut'))
  if (r.direction === 'routed') out.push(t('ruleRouted'))
  if (r.interface) out.push(t('ruleOn', { iface: r.interface }))
  if (r.destination && r.destination !== 'any') out.push(t('ruleTo', { address: r.destination }))
  if (r.source_port) out.push(t('ruleFromPort', { port: r.source_port }))
  if (r.ipv6) out.push('IPv6')
  return out
}

function RulesTable({
  rules,
  canEdit,
  onDelete,
}: {
  rules: FirewallRule[]
  canEdit: boolean
  onDelete: (r: FirewallRule) => void
}) {
  return (
    <TableWrap>
      <table className={tableClass.table}>
        <thead>
          <tr>
            <th className={tableClass.th}>{t('ruleNumber')}</th>
            <th className={tableClass.th}>{t('rulePort')}</th>
            <th className={tableClass.th}>{t('ruleProtocol')}</th>
            <th className={tableClass.th}>{t('ruleSource')}</th>
            <th className={tableClass.th}>{t('ruleAction')}</th>
            <th className={tableClass.th}>{t('ruleComment')}</th>
            {canEdit && <th className={cx(tableClass.th, 'text-right')} />}
          </tr>
        </thead>
        <tbody>
          {rules.map((r) => {
            const details = ruleDetails(r)
            return (
              <tr key={`${r.origin}-${r.number}-${r.id}`} className={tableClass.row}>
                <td className={cx(tableClass.td, 'text-faint')}>{r.number}</td>
                <td className={cx(tableClass.td, 'font-mono font-semibold')}>
                  {r.app ? r.app : r.port ? r.port : <span className="font-sans font-normal text-muted">{t('ruleAnyPort')}</span>}
                  {details.length > 0 && <span className="block font-sans text-[11px] font-normal text-faint">{details.join(' · ')}</span>}
                </td>
                <td className={tableClass.td}>{r.app ? <span className="text-faint">—</span> : protocolLabel(r.protocol)}</td>
                <td className={cx(tableClass.td, 'font-mono text-xs break-all')}>
                  {r.source === 'any' || r.source === '' ? (
                    <span className={cx('font-sans', r.action === 'allow' || r.action === 'limit' ? 'text-warning' : 'text-muted')}>
                      {t('ruleAnywhere')}
                    </span>
                  ) : (
                    r.source
                  )}
                </td>
                <td className={tableClass.td}>
                  <Badge tone={actionTone[r.action] ?? 'neutral'}>{actionLabel(r.action)}</Badge>
                </td>
                <td className={cx(tableClass.td, 'text-xs text-muted')}>
                  {r.comment}
                  {r.protects !== '' && (
                    <Badge tone="accent" className={r.comment ? 'ml-2' : undefined}>
                      {r.protects === 'ssh' ? t('ruleProtectsSsh') : t('ruleProtectsPanel')}
                    </Badge>
                  )}
                </td>
                {canEdit && (
                  <td className={cx(tableClass.td, 'text-right')}>
                    <IconButton icon={Trash2} label={t('ruleDelete')} tone="danger" onClick={() => onDelete(r)} />
                  </td>
                )}
              </tr>
            )
          })}
        </tbody>
      </table>
    </TableWrap>
  )
}

/* ---------- tab ---------- */

export function FirewallTab() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const q = useQuery<FirewallState>('/firewall/status')
  const sg = useQuery<Suggestions>('/firewall/suggestions')
  const [check, setCheck] = useState<EnableCheck | null>(null)
  const [disabling, setDisabling] = useState(false)
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState<FirewallRule | null>(null)

  const precheck = useAction(() => api.get<EnableCheck>('/firewall/enable-check'), {
    onSuccess: (c) => setCheck(c),
    onError: (m) => toast.error(m),
  })
  const disable = useAction(() => api.post<FirewallState | null>('/firewall/disable', {}), {
    onSuccess: () => {
      toast.success(t('disabledToast'))
      setDisabling(false)
      void q.reload()
    },
  })
  const remove = useAction(
    (r: FirewallRule) =>
      api.post<FirewallState | null>('/firewall/rules/delete', {
        origin: r.origin,
        number: r.number,
        id: r.id,
        acknowledge_access_risk: r.protects !== '',
      }),
    {
      onSuccess: () => {
        toast.success(t('deletedToast'))
        setDeleting(null)
        void q.reload()
      },
    },
  )

  if (q.loading) {
    return (
      <Card>
        <LoadingState />
      </Card>
    )
  }
  if (q.error || !q.data) {
    return (
      <Card>
        <ErrorState message={q.error ?? t('unknown')} onRetry={() => void q.reload()} />
      </Card>
    )
  }
  const st = q.data

  if (!st.installed) {
    return (
      <Card>
        <EmptyState
          icon={ShieldOff}
          title={t('fwNotInstalledTitle')}
          description={t('fwNotInstalledDesc')}
          action={
            <div className="flex flex-col items-center gap-3">
              <code className="max-w-full overflow-x-auto rounded-lg border border-line bg-surface px-3 py-2 font-mono text-xs text-fg">
                {st.install_hint}
              </code>
              <Button size="sm" icon={RefreshCw} onClick={() => void q.reload()} loading={q.fetching}>
                {t('refresh')}
              </Button>
            </div>
          }
        />
      </Card>
    )
  }

  const canEdit = isAdmin
  const logging = st.defaults.logging
  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex min-w-0 items-center gap-3">
              <IconTile icon={st.active ? ShieldCheck : ShieldAlert} tone={st.active ? 'success' : 'warning'} size="lg" />
              <div className="min-w-0">
                <h2 className="text-[15px] font-semibold text-fg">{t('fwTitle')}</h2>
                <Status tone={st.active ? 'success' : 'warning'}>{st.active ? t('fwActive') : t('fwInactive')}</Status>
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <IconButton icon={RefreshCw} label={t('refresh')} onClick={() => void q.reload()} loading={q.fetching} />
              {canEdit &&
                (st.active ? (
                  <Button
                    variant="danger"
                    icon={ShieldOff}
                    onClick={() => {
                      disable.clearError()
                      setDisabling(true)
                    }}
                  >
                    {t('fwDisable')}
                  </Button>
                ) : (
                  <Button variant="primary" icon={Shield} loading={precheck.pending} onClick={() => void precheck.run()}>
                    {t('fwEnable')}
                  </Button>
                ))}
            </div>
          </div>
          {!st.active && (
            <Alert tone="warning" title={t('fwInactiveWarn')} className="mt-4">
              {t('fwInactiveWarnDesc')}
            </Alert>
          )}
          {!canEdit && <p className="mt-3 text-xs text-faint">{t('fwAdminOnly')}</p>}
        </Card>

        <Card>
          <CardHeader title={t('policies')} icon={Shield} />
          <KeyValueList
            items={[
              { label: t('policyIncoming'), value: policyLabel(st.defaults.incoming) },
              { label: t('policyOutgoing'), value: policyLabel(st.defaults.outgoing) },
              { label: t('policyRouted'), value: policyLabel(st.defaults.routed) },
              { label: t('logging'), value: logging == null ? t('unknown') : logging === 'off' ? t('loggingOff') : logging },
              { label: t('sshPort'), value: <span title={st.ssh_evidence}>{st.ssh_ports.join(', ')}</span> },
              {
                label: t('panelPort'),
                value:
                  st.panel_port === 0
                    ? t('unknown')
                    : st.panel_port_required
                      ? String(st.panel_port)
                      : t('panelPortLocal', { port: st.panel_port }),
              },
            ]}
          />
        </Card>
      </div>

      <Card>
        <CardHeader
          title={t('rulesTitle')}
          icon={Shield}
          actions={
            canEdit ? (
              <Button size="sm" variant="primary" icon={Plus} onClick={() => setAdding(true)}>
                {t('ruleAdd')}
              </Button>
            ) : undefined
          }
        />
        {!st.active && st.rules.length > 0 && (
          <Alert tone="warning" className="mb-3">
            {t('fwInactiveRules')}
          </Alert>
        )}
        {st.rules.length === 0 ? (
          <EmptyState icon={Shield} title={t('rulesEmpty')} description={t('rulesEmptyDesc')} />
        ) : (
          <RulesTable
            rules={st.rules}
            canEdit={canEdit}
            onDelete={(r) => {
              remove.clearError()
              setDeleting(r)
            }}
          />
        )}
      </Card>

      <EnableDialog
        check={check}
        onClose={() => setCheck(null)}
        onDone={() => {
          setCheck(null)
          void q.reload()
        }}
      />

      <ConfirmDialog
        open={disabling}
        onClose={() => setDisabling(false)}
        title={t('disableTitle')}
        message={t('disableMessage')}
        confirmLabel={t('disableConfirm')}
        danger
        pending={disable.pending}
        error={disable.error}
        onConfirm={async () => {
          await disable.run()
        }}
      />

      <AddRuleDialog
        open={adding}
        onClose={() => setAdding(false)}
        onDone={() => {
          setAdding(false)
          void q.reload()
        }}
        state={st}
        suggestions={sg.data}
      />

      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        title={t('deleteTitle')}
        danger
        confirmLabel={t('deleteConfirm')}
        requireText={deleting && deleting.protects !== '' ? t('deleteRiskText') : undefined}
        pending={remove.pending}
        error={remove.error}
        message={
          deleting ? (
            <div className="flex flex-col gap-2">
              <p>{t('deleteMessage', { rule: '' })}</p>
              <code className="break-all rounded-lg border border-line bg-surface px-2 py-1.5 font-mono text-xs">{deleting.raw}</code>
              <p className="text-xs text-muted">
                {deleting.action === 'allow' || deleting.action === 'limit' ? t('deleteAllowEffect') : t('deleteBlockEffect')}
              </p>
            </div>
          ) : null
        }
        warning={
          deleting && deleting.protects !== ''
            ? t('deleteProtectedWarn', {
                what: deleting.protects === 'ssh' ? t('deleteProtectedSsh') : t('deleteProtectedPanel'),
              })
            : undefined
        }
        onConfirm={async () => {
          if (deleting) await remove.run(deleting)
        }}
      />
    </div>
  )
}
