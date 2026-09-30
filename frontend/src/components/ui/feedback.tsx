import { Component, type ErrorInfo, type ReactNode } from 'react'
import { AlertTriangle, CheckCircle2, Info, X, XCircle } from 'lucide-react'
import { cx } from '@/lib/format'
import { useUI, type ToastKind } from '@/stores/ui'
import { ErrorState } from './primitives'

const toastStyle: Record<ToastKind, { icon: typeof Info; tone: string }> = {
  success: { icon: CheckCircle2, tone: 'text-success border-success/30' },
  error: { icon: XCircle, tone: 'text-danger border-danger/30' },
  warning: { icon: AlertTriangle, tone: 'text-warning border-warning/30' },
  info: { icon: Info, tone: 'text-accent border-accent/30' },
}

/** Renders the toast stack; mounted once at the application root. */
export function Toasts() {
  const toasts = useUI((s) => s.toasts)
  const dismiss = useUI((s) => s.dismissToast)
  return (
    <div
      aria-live="polite"
      className="pointer-events-none fixed inset-x-3 bottom-20 z-[60] flex flex-col items-center gap-2 sm:inset-x-auto sm:right-5 sm:bottom-5 sm:items-end"
    >
      {toasts.map((t) => {
        const { icon: Icon, tone } = toastStyle[t.kind]
        return (
          <div
            key={t.id}
            role={t.kind === 'error' ? 'alert' : 'status'}
            className={cx('ms-fade-in pointer-events-auto flex w-full max-w-sm items-start gap-2.5 rounded-xl border bg-card px-3.5 py-3 shadow-card', tone)}
          >
            <Icon className="mt-0.5 size-4 shrink-0" aria-hidden />
            <p className="min-w-0 flex-1 break-words text-sm text-fg">{t.message}</p>
            <button type="button" aria-label="Bildirimi kapat" onClick={() => dismiss(t.id)} className="text-faint hover:text-fg">
              <X className="size-4" aria-hidden />
            </button>
          </div>
        )
      })}
    </div>
  )
}

interface BoundaryProps {
  children: ReactNode
  /** Shown in the fallback, e.g. the module's name. */
  name?: string
  /** Changing this value resets the boundary (pass the route path). */
  resetKey?: string
  compact?: boolean
  /** Runs when the user presses "Tekrar Dene", before the children are
   *  rendered again (e.g. to discard a failed lazy load). */
  onRetry?: () => void
}

interface BoundaryState {
  failed: boolean
}

/** Contains a rendering failure to one module or widget so the rest of the
 *  panel keeps working. */
export class ErrorBoundary extends Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { failed: false }

  static getDerivedStateFromError(): BoundaryState {
    return { failed: true }
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error(`[${this.props.name ?? 'bileşen'}]`, error, info.componentStack)
  }

  componentDidUpdate(prev: BoundaryProps): void {
    if (this.state.failed && prev.resetKey !== this.props.resetKey) this.setState({ failed: false })
  }

  render(): ReactNode {
    if (!this.state.failed) return this.props.children
    return (
      <ErrorState
        className={this.props.compact ? 'py-4' : undefined}
        title={this.props.name ? `${this.props.name} görüntülenemiyor` : 'Bu bölüm görüntülenemiyor'}
        message="Bu bölümde beklenmeyen bir hata oluştu. Panelin diğer bölümleri çalışmaya devam ediyor."
        onRetry={() => {
          this.props.onRetry?.()
          this.setState({ failed: false })
        }}
      />
    )
  }
}
