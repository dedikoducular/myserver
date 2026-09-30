import { useEffect, useRef, useState } from 'react'
import { Terminal, type ITheme } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { Eraser, LogOut, RefreshCw } from 'lucide-react'
import { Alert, Button, Modal, Status, type Tone } from '@/components/ui'
import { useSocket } from '@/hooks/useStream'
import { useUI } from '@/stores/ui'
import { t } from './strings'
import { token } from './util'
import type { ExecControl } from './types'

// WebSocket protocol (see backend/internal/docker/exec.go):
//   binary frames carry terminal input/output bytes,
//   text frames carry JSON control messages
//     client → server  {"type":"resize","cols":N,"rows":N}
//     server → client  {"type":"ready","shell":"/bin/bash"}
//                      {"type":"exit","code":N}
//                      {"type":"error","message":"…"}

/** Terminal colours taken from the design tokens, so both themes match. */
function buildTheme(): ITheme {
  const c = {
    bg: token('--ms-bg'),
    fg: token('--ms-fg'),
    muted: token('--ms-muted'),
    faint: token('--ms-faint'),
    line: token('--ms-line-strong'),
    accent: token('--ms-accent'),
    cyan: token('--ms-cyan'),
    success: token('--ms-success'),
    warning: token('--ms-warning'),
    danger: token('--ms-danger'),
    purple: token('--ms-purple'),
  }
  return {
    background: c.bg,
    foreground: c.fg,
    cursor: c.accent,
    cursorAccent: c.bg,
    selectionBackground: c.line,
    black: c.line,
    brightBlack: c.faint,
    red: c.danger,
    brightRed: c.danger,
    green: c.success,
    brightGreen: c.success,
    yellow: c.warning,
    brightYellow: c.warning,
    blue: c.accent,
    brightBlue: c.accent,
    magenta: c.purple,
    brightMagenta: c.purple,
    cyan: c.cyan,
    brightCyan: c.cyan,
    white: c.muted,
    brightWhite: c.fg,
  }
}

type Phase = 'connecting' | 'ready' | 'closed'

interface Connection {
  cols: number
  rows: number
  attempt: number
}

function parseControl(raw: string): ExecControl | null {
  try {
    const v = JSON.parse(raw) as ExecControl
    return typeof v === 'object' && v !== null && typeof v.type === 'string' ? v : null
  } catch {
    return null
  }
}

export function TerminalModal({ container, onClose }: { container: { id: string; name: string }; onClose: () => void }) {
  const theme = useUI((s) => s.theme)
  const host = useRef<HTMLDivElement>(null)
  const term = useRef<Terminal | null>(null)
  const fit = useRef<FitAddon | null>(null)
  const encoder = useRef(new TextEncoder())
  /** Set once the server said why the session ended. */
  const finished = useRef(false)
  const wasReady = useRef(false)

  const [conn, setConn] = useState<Connection | null>(null)
  const [phase, setPhase] = useState<Phase>('connecting')
  const [shell, setShell] = useState('')
  const [notice, setNotice] = useState<{ tone: Tone; text: string } | null>(null)

  const socket = useSocket(conn ? `/docker/containers/${encodeURIComponent(container.id)}/exec/ws` : null, {
    query: conn ? { cols: conn.cols, rows: conn.rows, n: conn.attempt } : undefined,
    enabled: conn !== null,
    onMessage: (data) => {
      if (data instanceof ArrayBuffer) {
        term.current?.write(new Uint8Array(data))
        return
      }
      if (typeof data !== 'string') return
      const msg = parseControl(data)
      if (!msg) return
      switch (msg.type) {
        case 'ready': {
          wasReady.current = true
          setShell(msg.shell)
          setPhase('ready')
          setNotice(null)
          const tm = term.current
          if (tm) {
            // The window may have been resized while connecting.
            sendRef.current(JSON.stringify({ type: 'resize', cols: tm.cols, rows: tm.rows }))
            tm.focus()
          }
          break
        }
        case 'exit':
          finished.current = true
          setNotice({
            tone: 'neutral',
            text: msg.code == null ? t('termExitedUnknown') : t('termExited', { code: msg.code }),
          })
          break
        case 'error':
          finished.current = true
          setNotice({ tone: 'danger', text: msg.message })
          break
      }
    },
    onClose: () => {
      setPhase('closed')
      if (!finished.current) {
        setNotice({ tone: 'danger', text: wasReady.current ? t('termLost') : t('termFailed') })
      }
    },
  })
  const sendRef = useRef(socket.send)
  sendRef.current = socket.send

  // Create the terminal once and connect when its size is known.
  useEffect(() => {
    const el = host.current
    if (!el) return
    const tm = new Terminal({
      cursorBlink: true,
      scrollback: 5000,
      fontSize: 13,
      fontFamily: token('--font-mono') || 'ui-monospace, Menlo, Consolas, monospace',
      theme: buildTheme(),
      allowProposedApi: false,
    })
    const fa = new FitAddon()
    tm.loadAddon(fa)
    tm.open(el)
    term.current = tm
    fit.current = fa

    const input = tm.onData((d) => sendRef.current(encoder.current.encode(d)))
    const binary = tm.onBinary((d) => sendRef.current(Uint8Array.from(d, (ch) => ch.charCodeAt(0) & 0xff)))
    const resized = tm.onResize(({ cols, rows }) => sendRef.current(JSON.stringify({ type: 'resize', cols, rows })))

    let frame: number | null = null
    const refit = () => {
      frame = null
      if (el.clientWidth === 0 || el.clientHeight === 0) return
      try {
        fa.fit()
      } catch {
        // The renderer is not ready yet; the next resize will fit.
      }
    }
    const observer = new ResizeObserver(() => {
      if (frame === null) frame = window.requestAnimationFrame(refit)
    })
    observer.observe(el)

    refit()
    setConn({ cols: tm.cols, rows: tm.rows, attempt: 0 })

    return () => {
      observer.disconnect()
      if (frame !== null) window.cancelAnimationFrame(frame)
      input.dispose()
      binary.dispose()
      resized.dispose()
      tm.dispose()
      term.current = null
      fit.current = null
    }
  }, [])

  // Follow the panel theme.
  useEffect(() => {
    if (term.current) term.current.options.theme = buildTheme()
  }, [theme])

  const reconnect = () => {
    const tm = term.current
    if (!tm) return
    finished.current = false
    wasReady.current = false
    tm.reset()
    setNotice(null)
    setShell('')
    setPhase('connecting')
    setConn((c) => ({ cols: tm.cols, rows: tm.rows, attempt: (c?.attempt ?? 0) + 1 }))
  }

  const open = phase !== 'closed'

  return (
    <Modal
      open
      onClose={onClose}
      title={t('termTitle', { name: container.name })}
      description={open ? t('termHint') : undefined}
      size="xl"
      // While a session is open Escape belongs to the terminal (vi, less…).
      busy={open}
      footer={
        <Button variant="ghost" icon={Eraser} onClick={() => term.current?.clear()}>
          {t('logsClear')}
        </Button>
      }
    >
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          {phase === 'ready' ? (
            <Status tone="success">{t('termConnected', { shell })}</Status>
          ) : phase === 'connecting' ? (
            <Status tone="warning" pulse>
              {t('termConnecting')}
            </Status>
          ) : (
            <Status tone="neutral">{t('termClosed')}</Status>
          )}
          <div className="flex flex-wrap items-center gap-2">
            {!open && (
              <Button icon={RefreshCw} onClick={reconnect}>
                {t('termReconnect')}
              </Button>
            )}
            <Button icon={LogOut} variant={open ? 'danger' : 'secondary'} onClick={onClose}>
              {open ? t('termEnd') : t('close')}
            </Button>
          </div>
        </div>
        {notice && <Alert tone={notice.tone}>{notice.text}</Alert>}
        <div className="overflow-hidden rounded-xl border border-line bg-bg p-2">
          <div ref={host} role="group" aria-label={t('termRegion')} className="h-[52dvh] min-h-56 w-full" />
        </div>
      </div>
    </Modal>
  )
}
