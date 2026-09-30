import type { Tone } from '@/components/ui'
import { t } from './strings'
import type { Service, ServiceState, Verb } from './types'

const UNIT_RE = /^[A-Za-z0-9@_.:-]{1,128}\.service$/

/** Normalizes user input to a unit name, or returns null when invalid. */
export function normalizeUnit(input: string): string | null {
  let name = input.trim()
  if (!name) return null
  if (!name.endsWith('.service')) name += '.service'
  if (!UNIT_RE.test(name) || name.startsWith('-') || name.endsWith('@.service')) return null
  return name
}

export function stateLabel(state: ServiceState): string {
  switch (state) {
    case 'running':
      return t('s_running')
    case 'active':
      return t('s_active')
    case 'stopped':
      return t('s_stopped')
    case 'failed':
      return t('s_failed')
    case 'disabled':
      return t('s_disabled')
    case 'activating':
      return t('s_activating')
    case 'deactivating':
      return t('s_deactivating')
    case 'masked':
      return t('s_masked')
    default:
      return t('s_not_installed')
  }
}

/** A stopped service is only alarming when it is meant to start at boot. */
export function stateTone(svc: Service): Tone {
  switch (svc.state) {
    case 'running':
    case 'active':
      return 'success'
    case 'failed':
      return 'danger'
    case 'activating':
    case 'deactivating':
      return 'warning'
    case 'stopped':
      return svc.unit_file_state === 'enabled' ? 'danger' : 'neutral'
    default:
      return 'neutral'
  }
}

export function bootLabel(fileState: string): string {
  switch (fileState) {
    case 'enabled':
      return t('b_enabled')
    case 'enabled-runtime':
      return t('b_runtime')
    case 'disabled':
      return t('b_disabled')
    case 'static':
      return t('b_static')
    case 'masked':
    case 'masked-runtime':
      return t('b_masked')
    case 'indirect':
      return t('b_indirect')
    case 'generated':
      return t('b_generated')
    case 'transient':
      return t('b_transient')
    default:
      return t('b_unknown')
  }
}

export function verbLabel(verb: Verb): string {
  switch (verb) {
    case 'start':
      return t('v_start')
    case 'stop':
      return t('v_stop')
    case 'restart':
      return t('v_restart')
    case 'reload':
      return t('v_reload')
    case 'enable':
      return t('v_enable')
    default:
      return t('v_disable')
  }
}

export function isDisruptive(verb: Verb): boolean {
  return verb === 'stop' || verb === 'restart' || verb === 'disable'
}

/** Mirrors the backend deny-list; the backend and helper enforce it. */
export function isDenied(svc: Service, verb: Verb): boolean {
  if (svc.risk !== 'protected') return false
  if (verb === 'stop' || verb === 'disable') return true
  return verb === 'restart' && (svc.unit === 'dbus.service' || svc.unit === 'dbus-broker.service')
}

/** Whether the action makes sense in the service's current state. */
export function isApplicable(svc: Service, verb: Verb): boolean {
  if (!svc.installed) return false
  const up = svc.state === 'running' || svc.state === 'active' || svc.state === 'activating'
  const masked = svc.state === 'masked'
  switch (verb) {
    case 'start':
      return !masked && !up
    case 'stop':
      return up || svc.state === 'deactivating' || svc.state === 'failed'
    case 'restart':
      return !masked && (up || svc.state === 'failed')
    case 'reload':
      return svc.can_reload && svc.state === 'running'
    case 'enable':
      return svc.unit_file_state === 'disabled' || svc.unit_file_state === 'indirect'
    default:
      return svc.unit_file_state === 'enabled' || svc.unit_file_state === 'enabled-runtime'
  }
}

export function riskWarning(svc: Service, verb: Verb): string | undefined {
  if (svc.risk === 'normal' || !isDisruptive(verb)) return undefined
  switch (svc.risk_kind) {
    case 'ssh': {
      const sockets = svc.triggered_by.join(', ')
      return sockets ? `${t('w_ssh')} ${t('w_ssh_socket', { names: sockets })}` : t('w_ssh')
    }
    case 'panel':
      return verb === 'restart' ? t('w_panel_restart') : t('w_panel_stop')
    case 'network':
      return t('w_network')
    case 'container':
      return t('w_container')
    default:
      return t('w_core')
  }
}

export function uptimeSeconds(svc: Service, nowSeconds: number): number | null {
  if (svc.active_since == null) return null
  if (svc.state !== 'running' && svc.state !== 'active') return null
  return Math.max(0, nowSeconds - svc.active_since)
}
