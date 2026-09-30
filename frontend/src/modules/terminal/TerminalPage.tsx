import { useCallback, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { Link } from 'react-router-dom'
import { Terminal, type ITheme } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import {
  AArrowDown,
  AArrowUp,
  ArrowDown,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  ClipboardPaste,
  Copy,
  Eraser,
  Lock,
  PlugZap,
  PowerOff,
  RefreshCw,
  Settings,
  ShieldAlert,
  SquareTerminal,
  UserX,
  type LucideIcon,
} from 'lucide-react'
import {
  Alert,
  Button,
  Card,
  EmptyState,
  ErrorState,
  IconButton,
  LoadingState,
  PageHeader,
  Status,
  type Tone,
} from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { useSocket, type SocketHandle } from '@/hooks/useStream'
import { clamp } from '@/lib/format'
import { useAuth } from '@/stores/auth'
import { toast, useUI } from '@/stores/ui'
import { t } from './strings'
import type { ServerMessage, TerminalStatus } from './types'

const FONT_KEY = 'myserver.terminal.font_size'
const FONT_MIN = 10
const FONT_MAX = 24
const FONT_DEFAULT = 14
/** Input is split so one frame never approaches the server's read limit. */
const INPUT_CHUNK = 4096
const PING_EVERY_MS = 30_000
const SILENCE_LIMIT_MS = 80_000

function readFontSize(): number {
  try {
    const n = Number.parseInt(localStorage.getItem(FONT_KEY) ?? '', 10)
    if (Number.isFinite(n)) return clamp(n, FONT_MIN, FONT_MAX)
  } catch {
    // Storage unavailable: use the default.
  }
  return FONT_DEFAULT
}

function cssVar(name: string): string | undefined {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return value || undefined
}

/** Adds an alpha channel to a #rrggbb token. */
function withAlpha(color: string | undefined, alpha: string): string | undefined {
  return color && /^#[0-9a-f]{6}$/i.test(color) ? color + alpha : color
}

/** Terminal colors taken from the design tokens of the active theme. */
function readTheme(): ITheme {
  const fg = cssVar('--ms-fg')
  const muted = cssVar('--ms-muted')
  const faint = cssVar('--ms-faint')
  const accent = cssVar('--ms-accent')
  const red = cssVar('--ms-danger')
  const green = cssVar('--ms-success')
  const yellow = cssVar('--ms-warning')
  const magenta = cssVar('--ms-purple')
  const cyan = cssVar('--ms-cyan')
  return {
    background: cssVar('--ms-surface'),
    foreground: fg,
    cursor: accent,
    cursorAccent: cssVar('--ms-surface'),
    selectionBackground: withAlpha(accent, '55'),
    selectionInactiveBackground: withAlpha(accent, '33'),
    black: cssVar('--ms-line-strong'),
    red,
    green,
    yellow,
    blue: accent,
    magenta,
    cyan,
    white: muted,
    brightBlack: faint,
    brightRed: red,
    brightGreen: green,
    brightYellow: yellow,
    brightBlue: accent,
    brightMagenta: magenta,
    brightCyan: cyan,
    brightWhite: fg,
  }
}

function fontFamily(): string {
  return cssVar('--font-mono') ?? 'ui-monospace, Menlo, Consolas, monospace'
}

async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Fall through to the selection-based copy.
  }
  // Panels are often reached over plain HTTP on the local network, where
  // the asynchronous clipboard API is not available.
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.appendChild(area)
  area.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  area.remove()
  return ok
}

/* ---------- Page ---------- */

export default function TerminalPage() {
  const isAdmin = useAuth((s) => s.isAdmin)
  const status = useQuery<TerminalStatus>('/terminal/status')
  const data = status.data

  let body
  if (status.loading) {
    body = <LoadingState label={t('loading')} />
  } else if (status.error || !data) {
    body = <ErrorState message={status.error ?? ''} onRetry={() => void status.reload()} />
  } else if (!isAdmin || !data.allowed) {
    body = <Blocked icon={Lock} title={t('notAdminTitle')} text={t('notAdminBody')} />
  } else if (!data.supported) {
    body = <Blocked icon={PowerOff} title={t('unsupportedTitle')} text={t('unsupportedBody')} />
  } else if (!data.enabled) {
    body = <Blocked icon={ShieldAlert} title={t('disabledTitle')} text={t('disabledBody')} settings onRetry={status.reload} />
  } else if (!data.user) {
    body = <Blocked icon={UserX} title={t('noUserTitle')} text={t('noUserBody')} settings onRetry={status.reload} />
  } else if (!data.user_valid) {
    body = (
      <Blocked
        icon={UserX}
        title={t('badUserTitle')}
        text={t('badUserBody', { user: data.user, reason: data.user_error ?? '' })}
        settings
        onRetry={status.reload}
      />
    )
  } else if (data.helper_available === false) {
    body = <Blocked icon={PlugZap} title={t('helperTitle')} text={t('helperBody')} onRetry={status.reload} />
  }

  return (
    <div className="flex h-[calc(100dvh-7.5rem)] min-h-[420px] flex-col">
      <PageHeader
        icon={SquareTerminal}
        title={t('title')}
        description={data?.allowed && data.user ? t('description', { user: data.user }) : t('descriptionPlain')}
      />
      {body ? (
        <Card className="flex min-h-0 flex-1 items-center justify-center">{body}</Card>
      ) : (
        data && <TerminalView status={data} reloadStatus={status.reload} />
      )}
    </div>
  )
}

function Blocked({
  icon,
  title,
  text,
  settings,
  onRetry,
}: {
  icon: LucideIcon
  title: string
  text: string
  settings?: boolean
  onRetry?: () => Promise<void>
}) {
  return (
    <EmptyState
      icon={icon}
      title={title}
      description={text}
      action={
        (settings || onRetry) && (
          <div className="flex flex-wrap items-center justify-center gap-2">
            {onRetry && (
              <Button icon={RefreshCw} onClick={() => void onRetry()}>
                {t('retry')}
              </Button>
            )}
            {settings && (
              <Link
                to="/settings"
                className="inline-flex h-10 items-center gap-2 rounded-xl border border-transparent bg-accent-strong px-4 text-sm font-medium text-accent-fg transition-colors hover:bg-accent"
              >
                <Settings className="size-4" aria-hidden />
                {t('openSettings')}
              </Link>
            )}
          </div>
        )
      }
    />
  )
}

/* ---------- Terminal ---------- */

interface Connection {
  cols: number
  rows: number
  attempt: number
}

interface Ended {
  tone: Tone
  text: string
}

/** Reasons after which the page must re-read the availability state. */
const STATUS_REASONS = new Set(['terminal_disabled', 'no_user', 'invalid_user', 'user_changed'])

function TerminalView({ status, reloadStatus }: { status: TerminalStatus; reloadStatus: () => Promise<void> }) {
  const theme = useUI((s) => s.theme)
  const host = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const sendRef = useRef<SocketHandle['send']>(() => {})
  const closeRef = useRef<() => void>(() => {})
  const openedRef = useRef(false)
  const endedRef = useRef(false)
  const lastSeen = useRef(0)
  const ctrlRef = useRef(false)

  const [conn, setConn] = useState<Connection | null>(null)
  const [ended, setEndedState] = useState<Ended | null>(null)
  const [idleUntil, setIdleUntil] = useState<number | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const [ctrl, setCtrlState] = useState(false)
  const [fontSize, setFontSize] = useState(readFontSize)

  const setEnded = useCallback((value: Ended | null) => {
    endedRef.current = value !== null
    setEndedState(value)
  }, [])

  const setCtrl = useCallback((value: boolean) => {
    ctrlRef.current = value
    setCtrlState(value)
  }, [])

  const sendBytes = useCallback((bytes: Uint8Array<ArrayBuffer>) => {
    for (let i = 0; i < bytes.length; i += INPUT_CHUNK) {
      sendRef.current(bytes.slice(i, Math.min(i + INPUT_CHUNK, bytes.length)))
    }
    setIdleUntil(null)
  }, [])

  /** Sends typed or pasted text, applying the on-screen Ctrl key. */
  const sendInput = useCallback(
    (text: string) => {
      let data = text
      if (ctrlRef.current) {
        setCtrl(false)
        if (data.length === 1) {
          const code = data.toUpperCase().charCodeAt(0)
          if (code >= 64 && code <= 95) data = String.fromCharCode(code - 64)
          else if (data === ' ') data = '\x00'
          else if (data === '?') data = '\x7f'
        }
      }
      sendBytes(new TextEncoder().encode(data))
    },
    [sendBytes, setCtrl],
  )

  const socket = useSocket(conn ? '/terminal/ws' : null, {
    enabled: conn !== null,
    query: conn ? { cols: conn.cols, rows: conn.rows, n: conn.attempt } : undefined,
    binaryType: 'arraybuffer',
    onOpen: () => {
      openedRef.current = true
      lastSeen.current = Date.now()
      const term = termRef.current
      if (term) {
        // The size may have changed while the socket was connecting.
        sendRef.current(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
        term.focus()
      }
    },
    onMessage: (data) => {
      lastSeen.current = Date.now()
      if (data instanceof ArrayBuffer) {
        termRef.current?.write(new Uint8Array(data))
        return
      }
      if (typeof data !== 'string') return
      let msg: ServerMessage
      try {
        msg = JSON.parse(data) as ServerMessage
      } catch {
        return
      }
      switch (msg.type) {
        case 'exit':
          setEnded({ tone: msg.code === 0 ? 'accent' : 'warning', text: t('closedExit', { code: msg.code }) })
          break
        case 'error':
          setEnded({ tone: 'danger', text: msg.message })
          if (msg.reason && STATUS_REASONS.has(msg.reason)) void reloadStatus()
          break
        case 'idle_warning':
          setIdleUntil(Date.now() + msg.seconds * 1000)
          break
        case 'pong':
          break
      }
    },
    onClose: () => {
      setIdleUntil(null)
      if (!endedRef.current) {
        setEnded({ tone: 'danger', text: openedRef.current ? t('closedLost') : t('closedNoConnect') })
      }
      void reloadStatus()
    },
  })
  sendRef.current = socket.send
  closeRef.current = socket.close
  const state = socket.state

  // Create the terminal once.
  useEffect(() => {
    const el = host.current
    if (!el) return
    const term = new Terminal({
      cursorBlink: true,
      fontSize: readFontSize(),
      fontFamily: fontFamily(),
      theme: readTheme(),
      scrollback: 5000,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(el)
    try {
      fit.fit()
    } catch {
      // The element may not be laid out yet; the observer fits it later.
    }
    termRef.current = term
    fitRef.current = fit

    const onData = term.onData((d) => sendInput(d))
    const onBinary = term.onBinary((d) => {
      const bytes = new Uint8Array(d.length)
      for (let i = 0; i < d.length; i++) bytes[i] = d.charCodeAt(i) & 0xff
      sendBytes(bytes)
    })
    const onResize = term.onResize(({ cols, rows }) => {
      sendRef.current(JSON.stringify({ type: 'resize', cols, rows }))
    })
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== 'keydown' || !e.ctrlKey || e.altKey) return true
      const key = e.key.toLowerCase()
      if (key === 'c' && (e.shiftKey || term.hasSelection())) {
        const text = term.getSelection()
        if (text) {
          e.preventDefault()
          void copyText(text).then((ok) => {
            if (!ok) toast.error(t('copyFailed'))
          })
          term.clearSelection()
          return false
        }
        return !e.shiftKey
      }
      // Leave Ctrl+V to the browser: its paste event reaches the terminal
      // and works without clipboard permissions.
      if (key === 'v') return false
      return true
    })

    // Refit on any size change of the container: window resize, sidebar
    // collapse, on-screen keyboard.
    let frame = 0
    const observer = new ResizeObserver(() => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(() => {
        if (el.clientWidth > 0 && el.clientHeight > 0) {
          try {
            fit.fit()
          } catch {
            // Ignored: the next resize fits again.
          }
        }
      })
    })
    observer.observe(el)

    setConn({ cols: term.cols, rows: term.rows, attempt: 1 })

    return () => {
      cancelAnimationFrame(frame)
      observer.disconnect()
      onData.dispose()
      onBinary.dispose()
      onResize.dispose()
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
  }, [sendInput, sendBytes])

  // Follow the panel theme.
  useEffect(() => {
    const term = termRef.current
    if (term) term.options.theme = readTheme()
  }, [theme])

  useEffect(() => {
    const term = termRef.current
    if (!term) return
    term.options.fontSize = fontSize
    try {
      fitRef.current?.fit()
    } catch {
      // Ignored: the observer fits again.
    }
    try {
      localStorage.setItem(FONT_KEY, String(fontSize))
    } catch {
      // The preference then lasts for this page load only.
    }
  }, [fontSize])

  // Application-level keepalive: detects a server that stopped answering.
  useEffect(() => {
    if (state !== 'open') return
    const timer = window.setInterval(() => {
      if (Date.now() - lastSeen.current > SILENCE_LIMIT_MS) {
        setEnded({ tone: 'danger', text: t('closedSilent') })
        closeRef.current()
        return
      }
      sendRef.current(JSON.stringify({ type: 'ping' }))
    }, PING_EVERY_MS)
    return () => window.clearInterval(timer)
  }, [state, setEnded])

  // Countdown of the idle warning.
  useEffect(() => {
    if (idleUntil === null) return
    setNow(Date.now())
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [idleUntil])

  const reconnect = () => {
    const term = termRef.current
    if (!term) return
    term.reset()
    openedRef.current = false
    setEnded(null)
    setIdleUntil(null)
    setCtrl(false)
    setConn((c) => ({ cols: term.cols, rows: term.rows, attempt: (c?.attempt ?? 0) + 1 }))
    term.focus()
  }

  const copySelection = async () => {
    const text = termRef.current?.getSelection() ?? ''
    if (!text) {
      toast.info(t('copyEmpty'))
      return
    }
    if (await copyText(text)) toast.success(t('copied'))
    else toast.error(t('copyFailed'))
    termRef.current?.focus()
  }

  const pasteClipboard = async () => {
    try {
      if (!navigator.clipboard || !window.isSecureContext) throw new Error('unavailable')
      const text = await navigator.clipboard.readText()
      if (text) termRef.current?.paste(text)
    } catch {
      toast.warning(t('pasteFailed'))
    }
    termRef.current?.focus()
  }

  const arrow = (letter: 'A' | 'B' | 'C' | 'D') => {
    const app = termRef.current?.modes.applicationCursorKeysMode
    setCtrl(false)
    sendBytes(new TextEncoder().encode((app ? '\x1bO' : '\x1b[') + letter))
  }

  // Helper keys must not take the focus (and the on-screen keyboard) away
  // from the terminal.
  const keepFocus = (e: ReactPointerEvent) => e.preventDefault()
  const live = state === 'open' && !ended
  const tone: Tone = live ? 'success' : state === 'connecting' && !ended ? 'warning' : 'danger'
  const label = live ? t('stateOpen') : state === 'connecting' && !ended ? t('stateConnecting') : t('stateClosed')
  const idleSeconds = idleUntil === null ? 0 : Math.max(0, Math.ceil((idleUntil - now) / 1000))

  const keys: Array<{ id: string; label: string; aria?: string; icon?: LucideIcon; run: () => void }> = [
    { id: 'esc', label: t('keyEsc'), run: () => sendInput('\x1b') },
    { id: 'tab', label: t('keyTab'), run: () => sendInput('\t') },
    { id: 'up', label: '', aria: t('keyUp'), icon: ArrowUp, run: () => arrow('A') },
    { id: 'down', label: '', aria: t('keyDown'), icon: ArrowDown, run: () => arrow('B') },
    { id: 'left', label: '', aria: t('keyLeft'), icon: ArrowLeft, run: () => arrow('D') },
    { id: 'right', label: '', aria: t('keyRight'), icon: ArrowRight, run: () => arrow('C') },
    { id: 'pipe', label: '|', run: () => sendInput('|') },
    { id: 'slash', label: '/', run: () => sendInput('/') },
    { id: 'dash', label: '-', run: () => sendInput('-') },
    { id: 'tilde', label: '~', run: () => sendInput('~') },
  ]

  return (
    <Card padded={false} className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <div
        role="toolbar"
        aria-label={t('toolbar')}
        className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 border-b border-line px-3 py-2"
      >
        <div className="flex min-w-0 items-center gap-3">
          <Status tone={tone} pulse={state === 'connecting' && !ended}>
            {label}
          </Status>
          <span className="truncate text-xs text-faint">
            {t('sessions', { count: status.active_sessions, max: status.max_sessions })}
          </span>
        </div>
        <div className="flex items-center gap-1">
          <IconButton icon={Copy} label={t('copy')} onPointerDown={keepFocus} onClick={() => void copySelection()} />
          <IconButton
            icon={ClipboardPaste}
            label={t('paste')}
            disabled={!live}
            onPointerDown={keepFocus}
            onClick={() => void pasteClipboard()}
          />
          <IconButton
            icon={AArrowDown}
            label={t('fontSmaller')}
            disabled={fontSize <= FONT_MIN}
            onPointerDown={keepFocus}
            onClick={() => setFontSize((s) => clamp(s - 1, FONT_MIN, FONT_MAX))}
          />
          <span className="w-6 text-center text-xs text-muted" aria-label={t('fontSize', { size: fontSize })}>
            {fontSize}
          </span>
          <IconButton
            icon={AArrowUp}
            label={t('fontLarger')}
            disabled={fontSize >= FONT_MAX}
            onPointerDown={keepFocus}
            onClick={() => setFontSize((s) => clamp(s + 1, FONT_MIN, FONT_MAX))}
          />
          <Button
            variant="ghost"
            icon={Eraser}
            onPointerDown={keepFocus}
            onClick={() => {
              termRef.current?.clear()
              termRef.current?.focus()
            }}
          >
            <span className="hidden sm:inline">{t('clear')}</span>
            <span className="sr-only sm:hidden">{t('clear')}</span>
          </Button>
          <Button
            variant={ended ? 'primary' : 'secondary'}
            icon={RefreshCw}
            disabled={state === 'connecting' && !ended}
            onClick={reconnect}
          >
            <span className="hidden sm:inline">{t('reconnect')}</span>
            <span className="sr-only sm:hidden">{t('reconnect')}</span>
          </Button>
        </div>
      </div>

      {ended && (
        <Alert tone={ended.tone} title={t('stateClosed')} className="mx-3 mt-3">
          {ended.text}
        </Alert>
      )}
      {!ended && live && idleUntil !== null && (
        <Alert tone="warning" className="mx-3 mt-3">
          {t('idleWarning', { seconds: idleSeconds })}
        </Alert>
      )}

      <div className="relative min-h-0 flex-1 bg-surface p-2">
        <div ref={host} role="group" aria-label={t('terminalLabel')} className="h-full w-full overflow-hidden" />
        {state === 'connecting' && !ended && (
          <div className="absolute inset-0 flex items-center justify-center bg-surface/80">
            <LoadingState label={t('overlayConnecting')} />
          </div>
        )}
      </div>

      <div
        role="toolbar"
        aria-label={t('helperKeys')}
        className="flex gap-1.5 overflow-x-auto border-t border-line px-3 py-2"
      >
        <Button
          size="md"
          variant={ctrl ? 'primary' : 'secondary'}
          aria-pressed={ctrl}
          title={t('keyCtrlHint')}
          disabled={!live}
          onPointerDown={keepFocus}
          onClick={() => {
            setCtrl(!ctrlRef.current)
            termRef.current?.focus()
          }}
          className="min-w-12 shrink-0 px-3 font-mono"
        >
          {t('keyCtrl')}
        </Button>
        {keys.map((k) => (
          <Button
            key={k.id}
            size="md"
            icon={k.icon}
            aria-label={k.aria}
            title={k.aria}
            disabled={!live}
            onPointerDown={keepFocus}
            onClick={() => {
              k.run()
              termRef.current?.focus()
            }}
            className="min-w-10 shrink-0 px-3 font-mono"
          >
            {k.label}
          </Button>
        ))}
      </div>
    </Card>
  )
}
