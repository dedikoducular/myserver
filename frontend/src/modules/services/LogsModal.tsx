import { useEffect, useRef, useState } from 'react'
import { RefreshCw, ScrollText } from 'lucide-react'
import { Alert, Button, EmptyState, ErrorState, LoadingState, Modal, Select, Status, Switch } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { useEventSource } from '@/hooks/useStream'
import { t } from './strings'
import type { Service, ServiceLogs } from './types'

const LINE_CHOICES = [100, 200, 500]
const MAX_LIVE_LINES = 1000

export function LogsModal({ svc, onClose }: { svc: Service; onClose: () => void }) {
  const [count, setCount] = useState(200)
  const [live, setLive] = useState(false)
  const [ended, setEnded] = useState(false)
  const [extra, setExtra] = useState<string[]>([])
  const box = useRef<HTMLPreElement>(null)
  const path = `/services/${encodeURIComponent(svc.unit)}/logs`

  const logs = useQuery<ServiceLogs>(path, { query: { lines: count } })

  const stream = useEventSource(
    live ? `${path}/stream` : null,
    (event, data) => {
      if (event === 'end') {
        setLive(false)
        setEnded(true)
        return
      }
      if (typeof data !== 'string') return
      setExtra((prev) => [...prev, data].slice(-MAX_LIVE_LINES))
    },
    { enabled: live, events: ['log', 'end'] },
  )

  const lines = logs.data ? [...logs.data.lines, ...extra] : extra
  const total = lines.length
  useEffect(() => {
    const el = box.current
    if (el) el.scrollTop = el.scrollHeight
  }, [total])

  const reload = () => {
    setExtra([])
    void logs.reload()
  }

  return (
    <Modal
      open
      onClose={onClose}
      size="xl"
      title={t('logs_title', { name: svc.name })}
      description={t('logs_desc', { unit: svc.unit })}
      footer={<Button onClick={onClose}>{t('close')}</Button>}
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-3">
          <Select
            aria-label={t('logs_lines')}
            className="w-auto"
            value={count}
            onChange={(e) => {
              setExtra([])
              setCount(Number(e.target.value))
            }}
          >
            {LINE_CHOICES.map((n) => (
              <option key={n} value={n}>
                {t('logs_n', { n })}
              </option>
            ))}
          </Select>
          <Button icon={RefreshCw} onClick={reload} loading={logs.fetching && !logs.loading}>
            {t('refresh')}
          </Button>
          <span className="flex min-h-10 items-center gap-2 text-sm text-fg">
            <Switch
              checked={live}
              label={t('logs_live')}
              onChange={(on) => {
                setEnded(false)
                setLive(on)
              }}
            />
            <span aria-hidden>{t('logs_live')}</span>
          </span>
          {live && (
            <Status tone={stream === 'open' ? 'success' : 'warning'} pulse={stream === 'open'}>
              {stream === 'open' ? t('logs_live_on') : t('logs_connecting')}
            </Status>
          )}
        </div>
        {ended && <Alert tone="accent">{t('logs_live_ended')}</Alert>}
        {logs.loading ? (
          <LoadingState label={t('logs_loading')} />
        ) : logs.error && total === 0 ? (
          <ErrorState message={logs.error} onRetry={reload} />
        ) : total === 0 ? (
          <EmptyState icon={ScrollText} title={t('logs_empty')} description={t('logs_empty_hint')} />
        ) : (
          <pre
            ref={box}
            tabIndex={0}
            className="max-h-[60dvh] overflow-auto rounded-xl border border-line bg-surface p-3 font-mono text-xs leading-relaxed text-fg"
          >
            {lines.join('\n')}
          </pre>
        )}
      </div>
    </Modal>
  )
}
