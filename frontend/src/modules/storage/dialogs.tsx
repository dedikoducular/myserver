import { useCallback, useEffect, useId, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Activity, RefreshCw } from 'lucide-react'
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  ErrorState,
  Field,
  Input,
  KeyValueList,
  LoadingState,
  Modal,
  Select,
  Switch,
  TableWrap,
  tableClass,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import { cx, formatBytes, formatRelative, formatTemperature } from '@/lib/format'
import { LABEL, MOUNT_NAME, busLabel, fsDescription, isRemovable, smartLabel, smartTone, t, typeLabel } from './strings'
import type { Device, FormatPrepare, FormatResult, MountResult, SmartReport } from './types'

export function filesPath(path: string): string {
  return `/files?path=${encodeURIComponent(path)}`
}

/* ---------- mount ---------- */

export function MountDialog({ device, onClose, onDone }: { device: Device | null; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState('')
  const [persistent, setPersistent] = useState(false)
  const inputId = useId()

  useEffect(() => {
    if (device) {
      setName(device.suggested_name)
      setPersistent(false)
    }
  }, [device])

  const removable = device ? isRemovable(device) : false
  const base = removable ? '/media' : '/mnt'
  const valid = MOUNT_NAME.test(name)

  const mount = useAction(
    () => api.post<MountResult>('/storage/mount', { device: device?.name, name, persistent }),
    {
      onSuccess: (res) => {
        toast.success(t('mountDone', { name: res.device, path: res.mountpoint }))
        if (res.warning) toast.warning(res.warning)
        onDone()
        onClose()
      },
    },
  )
  const { clearError } = mount
  useEffect(() => {
    if (device) clearError()
  }, [device, clearError])

  return (
    <Modal
      open={device !== null}
      onClose={onClose}
      busy={mount.pending}
      title={t('mountTitle', { name: device?.name ?? '' })}
      description={device ? `${device.fstype} · ${formatBytes(device.size)}${device.label ? ' · ' + device.label : ''}` : undefined}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={mount.pending}>
            {t('cancel')}
          </Button>
          <Button variant="primary" onClick={() => void mount.run()} loading={mount.pending} disabled={!valid}>
            {t('mount')}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Field
          label={t('mountName')}
          htmlFor={inputId}
          hint={t('mountNameHint', { path: `${base}/${name || '…'}` })}
          error={name !== '' && !valid ? t('mountNameInvalid') : null}
        >
          <Input
            id={inputId}
            data-autofocus
            value={name}
            invalid={name !== '' && !valid}
            maxLength={48}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-sm font-medium text-fg">{t('mountPersistent')}</p>
            <p className="mt-0.5 text-xs text-muted">{t('mountPersistentHint')}</p>
          </div>
          <Switch checked={persistent} onChange={setPersistent} label={t('mountPersistent')} disabled={!device?.uuid} />
        </div>
        <p className="text-xs text-faint">
          {t('mountOptions', { options: removable ? 'nosuid, nodev, noexec' : 'nosuid, nodev' })}
        </p>
        {mount.error && <Alert tone="danger">{mount.error}</Alert>}
      </div>
    </Modal>
  )
}

/* ---------- browse ---------- */

export function BrowseBlockedDialog({ path, onClose, onDone }: { path: string | null; onClose: () => void; onDone: () => void }) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const navigate = useNavigate()
  const allow = useAction(() => api.post<{ path: string }>('/storage/allow-root', { path }), {
    onSuccess: (res) => {
      toast.success(t('browseAllowed', { path: res.path }))
      onDone()
      onClose()
      navigate(filesPath(res.path))
    },
  })
  const { clearError } = allow
  useEffect(() => {
    if (path) clearError()
  }, [path, clearError])

  return (
    <Modal
      open={path !== null}
      onClose={onClose}
      busy={allow.pending}
      size="sm"
      title={t('browseBlockedTitle')}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={allow.pending} data-autofocus>
            {isAdmin ? t('cancel') : t('close')}
          </Button>
          {isAdmin && (
            <Button variant="primary" onClick={() => void allow.run()} loading={allow.pending}>
              {t('browseAllow')}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm text-fg">
        <p className="break-words">{t('browseBlockedMessage', { path: path ?? '' })}</p>
        {!isAdmin && <p className="text-xs text-muted">{t('browseBlockedUser')}</p>}
        {allow.error && <Alert tone="danger">{allow.error}</Alert>}
      </div>
    </Modal>
  )
}

/* ---------- format (two steps) ---------- */

export function FormatDialog({ device, onClose, onDone }: { device: Device | null; onClose: () => void; onDone: () => void }) {
  const [prep, setPrep] = useState<FormatPrepare | null>(null)
  const [loading, setLoading] = useState(false)
  const [prepError, setPrepError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [fstype, setFstype] = useState('')
  const [label, setLabel] = useState('')
  const [step, setStep] = useState<1 | 2>(1)
  const [seconds, setSeconds] = useState(0)
  const fsId = useId()
  const labelId = useId()
  const name = device?.name ?? null

  // Step 1: the server verifies the device and issues a single-use token
  // bound to its name, serial number and size.
  const prepare = useCallback(async (target: string) => {
    setLoading(true)
    setPrepError(null)
    try {
      const res = await api.post<FormatPrepare>('/storage/format/prepare', { device: target })
      setPrep(res)
      setFstype((cur) => {
        if (res.filesystems.some((f) => f.installed && f.type === cur)) return cur
        return res.filesystems.find((f) => f.installed)?.type ?? ''
      })
    } catch (e) {
      setPrep(null)
      setPrepError(errorMessage(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    if (!name) return
    setPrep(null)
    setStep(1)
    setLabel('')
    setNotice(null)
    void prepare(name)
  }, [name, prepare])

  const option = prep?.filesystems.find((f) => f.type === fstype)
  const labelValid = LABEL.test(label) && label.length <= (option?.max_label ?? 0)
  const anyTool = prep?.filesystems.some((f) => f.installed) ?? false
  const blockedWhole = Boolean(prep?.whole_disk && !prep.sfdisk)
  const canContinue = Boolean(prep && option?.installed && labelValid && !blockedWhole)

  const goConfirm = () => {
    if (!prep || !canContinue) return
    const left = Math.floor(prep.expires_at - Date.now() / 1000)
    if (left < 15 && name) {
      // Too little time left to read and type: verify again first.
      setNotice(t('formatExpired'))
      void prepare(name)
      return
    }
    setSeconds(left)
    setNotice(null)
    setStep(2)
  }

  const confirm = async () => {
    if (!prep || !name) return
    try {
      const res = await api.post<FormatResult>('/storage/format', {
        device: prep.device,
        token: prep.token,
        confirm_name: prep.device,
        fstype,
        label,
      })
      toast.success(t('formatDone', { name: res.partition || res.device, fstype: res.fstype }))
      onDone()
      onClose()
    } catch (e) {
      // The token is spent by any attempt; start again from step 1.
      const message = errorMessage(e)
      toast.error(message)
      setNotice(message)
      setStep(1)
      onDone()
      void prepare(name)
    }
  }

  const modelText = prep ? [prep.disk.vendor, prep.disk.model].filter(Boolean).join(' ') || t('unknownModel') : ''

  return (
    <>
      <Modal
        open={device !== null && step === 1}
        onClose={onClose}
        size="lg"
        title={t('formatTitle', { name: name ?? '' })}
        description={t('formatStep', { n: 1 })}
        footer={
          <>
            <Button variant="ghost" onClick={onClose} data-autofocus>
              {t('cancel')}
            </Button>
            <Button variant="danger" onClick={goConfirm} disabled={!canContinue || loading}>
              {t('continue')}
            </Button>
          </>
        }
      >
        {loading && !prep && <LoadingState label={t('formatPreparing')} />}
        {prepError && !loading && <Alert tone="danger">{prepError}</Alert>}
        {prep && (
          <div className="flex flex-col gap-4">
            {notice && <Alert tone="warning">{notice}</Alert>}
            <Alert tone="danger" title={t('formatDangerTitle')}>
              {t(prep.whole_disk ? 'formatDangerDisk' : 'formatDangerPart', { name: prep.path })}
            </Alert>
            <KeyValueList
              items={[
                {
                  label: t('formatDevice'),
                  value: (
                    <span className="font-mono font-semibold">
                      {prep.path} <span className="font-sans font-normal text-muted">({typeLabel(prep.type)})</span>
                    </span>
                  ),
                },
                { label: t('formatModel'), value: modelText },
                { label: t('serial'), value: <span className="font-mono">{prep.disk.serial || t('unknown')}</span> },
                { label: t('capacity'), value: formatBytes(prep.size) },
                { label: t('formatBus'), value: busLabel(prep.disk.transport) || t('unknown') },
                {
                  label: t('formatCurrent'),
                  value: prep.fstype ? `${prep.fstype}${prep.label ? ' · ' + prep.label : ''}` : t('formatEmpty'),
                },
              ]}
            />
            {prep.contents.length > 0 && (
              <div>
                <p className="mb-2 text-xs font-medium text-muted">{t('formatContents')}</p>
                <ul className="flex flex-col gap-1.5">
                  {prep.contents.map((c) => (
                    <li key={c.name} className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-xl border border-line bg-surface px-3 py-2 text-xs">
                      <span className="font-mono font-semibold text-fg">{c.name}</span>
                      <span className="text-muted">{formatBytes(c.size)}</span>
                      <span className="text-muted">{c.fstype || t('noFilesystem')}</span>
                      {c.label && <span className="text-muted">{c.label}</span>}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {!anyTool && <Alert tone="warning">{t('formatNoTools')}</Alert>}
            {blockedWhole && <Alert tone="warning">{t('formatNoSfdisk')}</Alert>}
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label={t('formatFilesystem')} htmlFor={fsId} hint={t('formatFsHint')}>
                <Select id={fsId} value={fstype} onChange={(e) => setFstype(e.target.value)} disabled={!anyTool}>
                  {prep.filesystems.map((f) => (
                    <option key={f.type} value={f.type} disabled={!f.installed}>
                      {f.installed ? fsDescription(f.type) : t('formatNotInstalled', { type: f.type })}
                    </option>
                  ))}
                </Select>
              </Field>
              <Field
                label={t('formatLabel')}
                htmlFor={labelId}
                hint={t('formatLabelHint', { max: option?.max_label ?? 0 })}
                error={label !== '' && !labelValid ? t('formatLabelInvalid') : null}
              >
                <Input
                  id={labelId}
                  value={label}
                  invalid={label !== '' && !labelValid}
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(e) => setLabel(e.target.value)}
                />
              </Field>
            </div>
            {prep.whole_disk && <p className="text-xs text-faint">{t('formatWholeNote')}</p>}
          </div>
        )}
      </Modal>

      <ConfirmDialog
        open={device !== null && step === 2 && prep !== null}
        onClose={() => setStep(1)}
        onConfirm={confirm}
        danger
        title={t('formatConfirmTitle', { name: prep?.device ?? '' })}
        confirmLabel={t('format')}
        cancelLabel={t('cancel')}
        requireText={prep?.device}
        message={
          <span className="break-words">
            {t('formatConfirmMessage', {
              path: prep?.path ?? '',
              model: modelText,
              size: formatBytes(prep?.size),
              serial: prep?.disk.serial || t('unknown'),
              fstype,
            })}
          </span>
        }
        warning={t('formatConfirmWarning', { seconds })}
      />
    </>
  )
}

/* ---------- SMART ---------- */

function num(v: number | null | undefined): string {
  return v == null ? '—' : v.toLocaleString('tr-TR')
}

function statusNote(r: SmartReport): string | null {
  switch (r.status) {
    case 'standby':
      return t('smartStandbyNote')
    case 'unsupported':
      return t('smartUnsupportedNote')
    case 'disabled':
      return t('smartDisabledNote')
    case 'not_installed':
      return t('smartNotInstalledNote')
    default:
      return null
  }
}

export function SmartDialog({ device, onClose, onDone }: { device: Device | null; onClose: () => void; onDone: () => void }) {
  const isAdmin = useAuth((s) => s.isAdmin)
  const name = device?.name ?? null
  const query = useQuery<SmartReport>(name ? `/storage/smart/${name}` : null)
  const [fresh, setFresh] = useState<SmartReport | null>(null)
  useEffect(() => setFresh(null), [name])

  const refresh = useAction(() => api.post<SmartReport>(`/storage/smart/${name}/refresh`), {
    onSuccess: (r) => {
      setFresh(r)
      onDone()
    },
    onError: (m) => toast.error(m),
  })
  const test = useAction((type: 'short' | 'long') => api.post(`/storage/smart/${name}/test`, { type }), {
    onSuccess: () => toast.success(t('smartTestStarted')),
    onError: (m) => toast.error(m),
  })

  // A report of a previously opened disk must never be shown for this one.
  const current = fresh ?? query.data
  const r = current && current.device === name ? current : undefined
  const usable = r && (r.status === 'passed' || r.status === 'warning' || r.status === 'failed')
  const note = r ? statusNote(r) : null
  const busy = refresh.pending || test.pending

  return (
    <Modal
      open={device !== null}
      onClose={onClose}
      size="xl"
      title={t('smartTitle', { name: name ?? '' })}
      description={device ? [device.vendor, device.model].filter(Boolean).join(' ') || undefined : undefined}
      footer={
        <>
          {isAdmin && r && r.status !== 'not_installed' && (
            <>
              <Button icon={RefreshCw} onClick={() => void refresh.run()} loading={refresh.pending} disabled={busy}>
                {t('smartRefresh')}
              </Button>
              {usable && (
                <>
                  <Button icon={Activity} onClick={() => void test.run('short')} disabled={busy}>
                    {t('smartShortTest')}
                  </Button>
                  <Button icon={Activity} onClick={() => void test.run('long')} disabled={busy}>
                    {t('smartLongTest')}
                  </Button>
                </>
              )}
            </>
          )}
          <Button variant="ghost" onClick={onClose} data-autofocus>
            {t('close')}
          </Button>
        </>
      }
    >
      {query.loading && !r && <LoadingState label={t('smartLoading')} />}
      {query.error && !r && <ErrorState message={query.error} onRetry={() => void query.reload()} />}
      {r && (
        <div className="flex flex-col gap-5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-sm text-muted">{t('smartOverall')}:</span>
            <Badge tone={smartTone(r.status)}>{smartLabel(r.status)}</Badge>
            {r.checked_at > 0 && (
              <span className="text-xs text-faint">
                {t('smartChecked')}: {formatRelative(r.checked_at)}
              </span>
            )}
          </div>
          {note && <Alert tone="warning">{note}</Alert>}
          {r.problems.length > 0 && (
            <Alert tone={r.status === 'failed' ? 'danger' : 'warning'} title={t('smartProblems')}>
              <ul className="list-disc pl-4">
                {r.problems.map((p) => (
                  <li key={p}>{p}</li>
                ))}
              </ul>
            </Alert>
          )}
          {usable && (
            <div className="grid gap-x-8 gap-y-2.5 md:grid-cols-2">
              <KeyValueList
                items={[
                  { label: t('temperature'), value: formatTemperature(r.temperature) },
                  { label: t('smartPowerOn'), value: r.power_on_hours == null ? '—' : t('smartHours', { hours: num(r.power_on_hours) }) },
                  { label: t('smartCycles'), value: num(r.power_cycles) },
                  { label: t('serial'), value: <span className="font-mono">{r.serial || '—'}</span> },
                  { label: t('smartFirmware'), value: r.firmware || '—' },
                  { label: t('smartProtocol'), value: r.protocol || '—' },
                ]}
              />
              {r.nvme ? (
                <KeyValueList
                  items={[
                    { label: t('smartPercentUsed'), value: `%${r.nvme.percentage_used}` },
                    {
                      label: t('smartSpare'),
                      value: t('smartSpareValue', { value: r.nvme.available_spare, threshold: r.nvme.available_spare_threshold }),
                    },
                    { label: t('smartMediaErrors'), value: num(r.nvme.media_errors) },
                    {
                      label: t('smartCritical'),
                      value: r.nvme.critical_warning === 0 ? t('smartCriticalNone') : t('smartCriticalCode', { code: r.nvme.critical_warning }),
                    },
                    { label: t('smartUnsafe'), value: num(r.nvme.unsafe_shutdowns) },
                  ]}
                />
              ) : (
                <KeyValueList
                  items={[
                    { label: t('smartReallocated'), value: num(r.reallocated_sectors) },
                    { label: t('smartPendingSectors'), value: num(r.pending_sectors) },
                    { label: t('smartUncorrectable'), value: num(r.uncorrectable_sectors) },
                  ]}
                />
              )}
            </div>
          )}
          {usable && !r.nvme && (
            <div>
              <p className="mb-2 text-xs font-medium text-muted">{t('smartAttributes')}</p>
              {r.attributes.length === 0 ? (
                <p className="text-xs text-faint">{t('smartNoAttributes')}</p>
              ) : (
                <TableWrap>
                  <table className={tableClass.table}>
                    <thead>
                      <tr>
                        <th className={tableClass.th}>{t('smartAttrId')}</th>
                        <th className={tableClass.th}>{t('smartAttrName')}</th>
                        <th className={cx(tableClass.th, 'text-right')}>{t('smartAttrValue')}</th>
                        <th className={cx(tableClass.th, 'text-right')}>{t('smartAttrWorst')}</th>
                        <th className={cx(tableClass.th, 'text-right')}>{t('smartAttrThresh')}</th>
                        <th className={tableClass.th}>{t('smartAttrRaw')}</th>
                        <th className={tableClass.th}>{t('smartAttrState')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {r.attributes.map((a) => {
                        const failing = a.when_failed === 'now'
                        const past = a.when_failed === 'past'
                        return (
                          <tr key={a.id} className={tableClass.row}>
                            <td className={cx(tableClass.td, 'font-mono text-muted')}>{a.id}</td>
                            <td className={tableClass.td}>{a.name.replace(/_/g, ' ')}</td>
                            <td className={cx(tableClass.td, 'text-right font-mono')}>{a.value}</td>
                            <td className={cx(tableClass.td, 'text-right font-mono')}>{a.worst}</td>
                            <td className={cx(tableClass.td, 'text-right font-mono')}>{a.threshold}</td>
                            <td className={cx(tableClass.td, 'font-mono')}>{a.raw_string || a.raw}</td>
                            <td className={tableClass.td}>
                              <Badge tone={failing ? 'danger' : past ? 'warning' : 'neutral'}>
                                {failing ? t('smartAttrFailing') : past ? t('smartAttrPast') : t('smartAttrOk')}
                              </Badge>
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </TableWrap>
              )}
            </div>
          )}
        </div>
      )}
    </Modal>
  )
}
