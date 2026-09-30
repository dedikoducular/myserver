import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { AlertTriangle, X, type LucideIcon } from 'lucide-react'
import { cx } from '@/lib/format'
import { Alert, Button, IconButton, Input } from './primitives'

const FOCUSABLE = 'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'

export interface ModalProps {
  open: boolean
  onClose: () => void
  title: string
  description?: ReactNode
  children?: ReactNode
  footer?: ReactNode
  size?: 'sm' | 'md' | 'lg' | 'xl'
  /** Blocks closing by Escape/backdrop, e.g. while an operation runs. */
  busy?: boolean
}

const modalWidth = { sm: 'max-w-sm', md: 'max-w-lg', lg: 'max-w-2xl', xl: 'max-w-4xl' }

export function Modal({ open, onClose, title, description, children, footer, size = 'md', busy }: ModalProps) {
  const panel = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const closeRef = useRef(onClose)
  closeRef.current = onClose
  const busyRef = useRef(busy)
  busyRef.current = busy

  useEffect(() => {
    if (!open) return
    const previous = document.activeElement as HTMLElement | null
    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    const first = panel.current?.querySelector<HTMLElement>('[data-autofocus]') ?? panel.current?.querySelector<HTMLElement>(FOCUSABLE)
    ;(first ?? panel.current)?.focus()

    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !busyRef.current) {
        e.stopPropagation()
        closeRef.current()
        return
      }
      if (e.key !== 'Tab' || !panel.current) return
      const items = Array.from(panel.current.querySelectorAll<HTMLElement>(FOCUSABLE))
      if (items.length === 0) {
        e.preventDefault()
        return
      }
      const firstEl = items[0]!
      const lastEl = items[items.length - 1]!
      if (e.shiftKey && document.activeElement === firstEl) {
        e.preventDefault()
        lastEl.focus()
      } else if (!e.shiftKey && document.activeElement === lastEl) {
        e.preventDefault()
        firstEl.focus()
      }
    }
    document.addEventListener('keydown', onKey, true)
    return () => {
      document.removeEventListener('keydown', onKey, true)
      document.body.style.overflow = prevOverflow
      previous?.focus?.()
    }
  }, [open])

  if (!open) return null
  return createPortal(
    <div className="fixed inset-0 z-50 flex items-end justify-center sm:items-center sm:p-4">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-[2px]" onClick={() => !busy && onClose()} aria-hidden />
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className={cx(
          'ms-fade-in relative flex max-h-[92dvh] w-full flex-col rounded-t-2xl border border-line bg-card shadow-card sm:rounded-2xl',
          modalWidth[size],
        )}
      >
        <div className="flex items-start justify-between gap-3 border-b border-line px-5 py-4">
          <div className="min-w-0">
            <h2 id={titleId} className="text-base font-semibold text-fg">
              {title}
            </h2>
            {description && <p className="mt-1 text-xs text-muted">{description}</p>}
          </div>
          <IconButton icon={X} label="Kapat" size="sm" onClick={onClose} disabled={busy} />
        </div>
        {children && <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>}
        {footer && <div className="flex flex-wrap justify-end gap-2 border-t border-line px-5 py-3">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}

export interface ConfirmDialogProps {
  open: boolean
  onClose: () => void
  /** May be async; the dialog shows progress and stays open on failure. */
  onConfirm: () => void | Promise<void>
  title: string
  message: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  /** Styles the action as destructive. */
  danger?: boolean
  /** Extra warning shown in a highlighted box. */
  warning?: ReactNode
  /** When set, the user must type this exact text to enable the action.
   *  Use for irreversible operations. */
  requireText?: string
  pending?: boolean
  error?: string | null
}

export function ConfirmDialog({
  open,
  onClose,
  onConfirm,
  title,
  message,
  confirmLabel = 'Onayla',
  cancelLabel = 'Vazgeç',
  danger,
  warning,
  requireText,
  pending,
  error,
}: ConfirmDialogProps) {
  const [typed, setTyped] = useState('')
  const [running, setRunning] = useState(false)
  // Message of an error thrown by onConfirm itself (callers that handle
  // their own errors pass `error` instead).
  const [thrown, setThrown] = useState<string | null>(null)
  const inputId = useId()
  useEffect(() => {
    if (open) {
      setTyped('')
      setThrown(null)
    }
  }, [open])

  const busy = Boolean(pending) || running
  const allowed = !requireText || typed.trim() === requireText
  const shownError = error ?? thrown

  const confirm = async () => {
    if (!allowed || busy) return
    setRunning(true)
    setThrown(null)
    try {
      await onConfirm()
    } catch (e) {
      setThrown(e instanceof Error && e.message ? e.message : 'İşlem tamamlanamadı.')
    } finally {
      setRunning(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      size="sm"
      busy={busy}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy} data-autofocus>
            {cancelLabel}
          </Button>
          <Button variant={danger ? 'danger' : 'primary'} onClick={confirm} loading={busy} disabled={!allowed}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm text-muted">
        <div className="flex gap-3">
          {danger && <AlertTriangle className="mt-0.5 size-5 shrink-0 text-danger" aria-hidden />}
          <div className="min-w-0 text-fg">{message}</div>
        </div>
        {warning && <Alert tone={danger ? 'danger' : 'warning'}>{warning}</Alert>}
        {requireText && (
          <div className="flex flex-col gap-1.5">
            <label htmlFor={inputId} className="text-xs text-muted">
              Onaylamak için <span className="font-mono font-semibold text-fg">{requireText}</span> yazın
            </label>
            <Input
              id={inputId}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              onKeyDown={(e) => {
                if (e.key === 'Enter') void confirm()
              }}
            />
          </div>
        )}
        {shownError && <Alert tone="danger">{shownError}</Alert>}
      </div>
    </Modal>
  )
}

export interface MenuItem {
  label: string
  icon?: LucideIcon
  onSelect: () => void
  danger?: boolean
  disabled?: boolean
  /** Draws a divider above this item. */
  separated?: boolean
}

/** Button-triggered menu (the "…" menu on cards and rows). */
export function Menu({
  items,
  trigger,
  align = 'end',
  label,
}: {
  items: MenuItem[]
  /** Rendered inside the trigger button. */
  trigger: ReactNode
  align?: 'start' | 'end'
  /** Accessible name of the trigger. */
  label: string
}) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const list = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        root.current?.querySelector<HTMLElement>('button')?.focus()
        return
      }
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
      e.preventDefault()
      const els = Array.from(list.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([disabled])') ?? [])
      if (els.length === 0) return
      const i = els.indexOf(document.activeElement as HTMLElement)
      const next = e.key === 'ArrowDown' ? (i + 1) % els.length : (i - 1 + els.length) % els.length
      els[next]?.focus()
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    list.current?.querySelector<HTMLElement>('[role="menuitem"]:not([disabled])')?.focus()
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  return (
    <div ref={root} className="relative inline-flex">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        title={label}
        onClick={(e) => {
          e.stopPropagation()
          setOpen((v) => !v)
        }}
        className="inline-flex min-h-8 min-w-8 items-center justify-center rounded-lg text-muted transition-colors hover:bg-raised hover:text-fg"
      >
        {trigger}
      </button>
      {open && (
        <div
          ref={list}
          role="menu"
          className={cx(
            'ms-fade-in absolute top-full z-40 mt-1 min-w-44 rounded-xl border border-line bg-card p-1 shadow-card',
            align === 'end' ? 'right-0' : 'left-0',
          )}
        >
          {items.map((item) => (
            <div key={item.label}>
              {item.separated && <div className="my-1 border-t border-line" />}
              <button
                type="button"
                role="menuitem"
                disabled={item.disabled}
                onClick={(e) => {
                  e.stopPropagation()
                  setOpen(false)
                  item.onSelect()
                }}
                className={cx(
                  'flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm transition-colors disabled:opacity-40',
                  item.danger ? 'text-danger hover:bg-danger/10' : 'text-fg hover:bg-raised',
                )}
              >
                {item.icon && <item.icon className="size-4 shrink-0" aria-hidden />}
                {item.label}
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
