import { useEffect, useId, useRef, useState } from 'react'
import { Button, Field, Modal, Select, Status, Switch } from '@/components/ui'
import { useEventSource } from '@/hooks/useStream'
import { cx } from '@/lib/format'
import { t } from './strings'
import type { InstalledApp, LogLine } from './types'

const MAX_LINES = 1500

interface Row extends LogLine {
  id: number
}

function Viewer({ app, service }: { app: InstalledApp; service: string }) {
  const [rows, setRows] = useState<Row[]>([])
  const [ended, setEnded] = useState(false)
  const [follow, setFollow] = useState(true)
  const nextId = useRef(1)
  const lastTime = useRef('')
  const box = useRef<HTMLDivElement>(null)

  const state = useEventSource(
    `/apps/installed/${app.slug}/logs`,
    (name, data) => {
      if (name === 'end') {
        setEnded(true)
        return
      }
      const line = data as LogLine
      if (typeof line?.line !== 'string') return
      // A reopened stream replays the tail; timestamps are fixed-width, so
      // comparing them as text orders them.
      if (line.time && lastTime.current && line.time <= lastTime.current) return
      if (line.time) lastTime.current = line.time
      setEnded(false)
      setRows((prev) => {
        const next = [...prev, { ...line, id: nextId.current++ }]
        return next.length > MAX_LINES ? next.slice(next.length - MAX_LINES) : next
      })
    },
    { events: ['log', 'end'], query: { service, tail: 300 } },
  )

  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight
  }, [rows, follow])

  const live = state === 'open' && !ended
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <Status tone={live ? 'success' : state === 'connecting' ? 'warning' : 'neutral'} pulse={live}>
          {ended ? t('logsEnded') : state === 'open' ? t('logsLive') : state === 'connecting' ? t('logsConnecting') : t('logsClosed')}
        </Status>
        <div className="flex items-center gap-3">
          <label className="flex items-center gap-2 text-xs text-muted">
            <Switch checked={follow} onChange={setFollow} label={t('logsFollow')} />
            {t('logsFollow')}
          </label>
          <Button size="sm" onClick={() => setRows([])}>
            {t('logsClear')}
          </Button>
        </div>
      </div>
      <div
        ref={box}
        role="log"
        aria-label={t('logsRegion')}
        tabIndex={0}
        className="h-[55dvh] overflow-auto rounded-xl border border-line bg-bg p-3 font-mono text-[11px] leading-relaxed"
      >
        {rows.length === 0 ? (
          <p className="text-faint">{t('logsWaiting')}</p>
        ) : (
          rows.map((r) => (
            <p key={r.id} className={cx('break-all whitespace-pre-wrap', r.stream === 'stderr' ? 'text-warning' : 'text-fg')}>
              {r.time && <span className="text-faint select-none">{r.time.slice(11, 19)} </span>}
              {r.line}
            </p>
          ))
        )}
      </div>
    </div>
  )
}

export function LogsDialog({ app, onClose }: { app: InstalledApp | null; onClose: () => void }) {
  const selectId = useId()
  const [service, setService] = useState('')

  useEffect(() => {
    setService(app?.services[0]?.name ?? '')
  }, [app?.slug]) // eslint-disable-line react-hooks/exhaustive-deps

  const current = app?.services.some((s) => s.name === service) ? service : (app?.services[0]?.name ?? '')
  return (
    <Modal
      open={app !== null}
      onClose={onClose}
      size="xl"
      title={app ? t('logsTitle', { name: app.name }) : ''}
      footer={<Button onClick={onClose}>{t('close')}</Button>}
    >
      {app && current && (
        <div className="flex flex-col gap-3">
          {app.services.length > 1 && (
            <Field label={t('service')} htmlFor={selectId} className="sm:max-w-xs">
              <Select id={selectId} value={current} onChange={(e) => setService(e.target.value)}>
                {app.services.map((s) => (
                  <option key={s.name} value={s.name}>
                    {s.name}
                  </option>
                ))}
              </Select>
            </Field>
          )}
          <Viewer key={app.slug + '/' + current} app={app} service={current} />
        </div>
      )}
    </Modal>
  )
}
