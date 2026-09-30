import { useState, type ReactNode } from 'react'
import { FileText, Play, Power, PowerOff, RefreshCcw, RotateCw, Square, type LucideIcon } from 'lucide-react'
import { ConfirmDialog, IconButton, type Tone } from '@/components/ui'
import { useAction } from '@/hooks/useApi'
import { ApiError, api } from '@/services/api'
import { toast } from '@/stores/ui'
import { t } from './strings'
import type { Service, Verb } from './types'
import { isApplicable, isDenied, isDisruptive, riskWarning, verbLabel } from './util'

type Outcome = 'ok' | 'panel'

function doneMessage(svc: Service, verb: Verb, outcome: Outcome): string {
  if (outcome === 'panel' || (svc.risk_kind === 'panel' && (verb === 'stop' || verb === 'restart'))) {
    return verb === 'restart' ? t('panel_restarting') : t('panel_stopped')
  }
  const vars = { name: svc.name }
  switch (verb) {
    case 'start':
      return t('done_start', vars)
    case 'stop':
      return t('done_stop', vars)
    case 'restart':
      return t('done_restart', vars)
    case 'reload':
      return t('done_reload', vars)
    case 'enable':
      return t('done_enable', vars)
    default:
      return t('done_disable', vars)
  }
}

export interface ServiceControl {
  /** Runs the action, asking for confirmation first when it is disruptive. */
  request: (svc: Service, verb: Verb) => void
  /** "unit:verb" of the action in flight, if any. */
  busy: string | null
  dialog: ReactNode
}

export function useServiceControl(onDone: () => void): ServiceControl {
  const [asking, setAsking] = useState<{ svc: Service; verb: Verb } | null>(null)
  const [busy, setBusy] = useState<string | null>(null)

  const action = useAction(async (svc: Service, verb: Verb, confirm?: string): Promise<Outcome> => {
    try {
      await api.post<Service>(`/services/${encodeURIComponent(svc.unit)}/${verb}`, confirm ? { confirm } : {})
      return 'ok'
    } catch (e) {
      // The panel may go down before it can answer its own stop/restart.
      const panelGone =
        svc.risk_kind === 'panel' &&
        (verb === 'stop' || verb === 'restart') &&
        e instanceof ApiError &&
        (e.code === 'network_error' || e.status === 502 || e.status === 503 || e.status === 504)
      if (panelGone) return 'panel'
      throw e
    }
  }, {
    // Failures of unconfirmed actions are reported by toast; confirmed ones
    // are shown inside the dialog, which stays open.
    onError: (message) => {
      if (!asking) toast.error(message)
    },
  })

  const execute = async (svc: Service, verb: Verb, confirm?: string): Promise<boolean> => {
    setBusy(`${svc.unit}:${verb}`)
    try {
      const outcome = await action.run(svc, verb, confirm)
      if (!outcome) return false
      const message = doneMessage(svc, verb, outcome)
      if (svc.risk_kind === 'panel' && (verb === 'stop' || verb === 'restart')) toast.info(message)
      else toast.success(message)
      onDone()
      return true
    } finally {
      setBusy(null)
    }
  }

  const request = (svc: Service, verb: Verb) => {
    if (busy || isDenied(svc, verb)) return
    action.clearError()
    if (isDisruptive(verb)) {
      setAsking({ svc, verb })
      return
    }
    void execute(svc, verb).then((ok) => {
      if (!ok) onDone()
    })
  }

  let dialog: ReactNode = null
  if (asking) {
    const { svc, verb } = asking
    const critical = svc.risk !== 'normal'
    const vars = { name: svc.name, unit: svc.unit }
    dialog = (
      <ConfirmDialog
        open
        danger
        onClose={() => {
          setAsking(null)
          action.clearError()
        }}
        onConfirm={async () => {
          const ok = await execute(svc, verb, critical ? svc.unit : undefined)
          if (ok) setAsking(null)
        }}
        title={verb === 'stop' ? t('c_title_stop') : verb === 'restart' ? t('c_title_restart') : t('c_title_disable')}
        message={verb === 'stop' ? t('c_msg_stop', vars) : verb === 'restart' ? t('c_msg_restart', vars) : t('c_msg_disable', vars)}
        warning={riskWarning(svc, verb)}
        requireText={critical ? svc.unit : undefined}
        confirmLabel={verbLabel(verb)}
        cancelLabel={t('cancel')}
        pending={action.pending}
        error={action.error}
      />
    )
  }

  return { request, busy, dialog }
}

const verbs: Array<{ verb: Verb; icon: LucideIcon; tone: Tone }> = [
  { verb: 'start', icon: Play, tone: 'success' },
  { verb: 'stop', icon: Square, tone: 'danger' },
  { verb: 'restart', icon: RotateCw, tone: 'accent' },
  { verb: 'reload', icon: RefreshCcw, tone: 'neutral' },
  { verb: 'enable', icon: Power, tone: 'neutral' },
  { verb: 'disable', icon: PowerOff, tone: 'neutral' },
]

/** The action buttons of one service. Rendered for administrators only. */
export function ServiceActions({
  svc,
  control,
  onLogs,
}: {
  svc: Service
  control: ServiceControl
  onLogs: (svc: Service) => void
}) {
  // 40 px touch targets, tightened on wide screens where a mouse is used.
  const size = 'xl:size-8'
  return (
    <div className="flex flex-wrap items-center gap-1 xl:flex-nowrap xl:gap-0.5">
      {verbs.map(({ verb, icon, tone }) => {
        const denied = isDenied(svc, verb)
        const enabled = isApplicable(svc, verb) && !denied
        const label = t('action_for', { action: verbLabel(verb), name: svc.name })
        return (
          <IconButton
            key={verb}
            icon={icon}
            label={denied ? `${label} — ${t('protected_hint')}` : label}
            tone={enabled ? tone : 'neutral'}
            className={size}
            disabled={!enabled || control.busy !== null}
            loading={control.busy === `${svc.unit}:${verb}`}
            onClick={() => control.request(svc, verb)}
          />
        )
      })}
      <IconButton
        icon={FileText}
        label={t('action_for', { action: t('logs'), name: svc.name })}
        className={size}
        onClick={() => onLogs(svc)}
      />
    </div>
  )
}
