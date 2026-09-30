import {
  forwardRef,
  type ButtonHTMLAttributes,
  type HTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
} from 'react'
import { AlertTriangle, Inbox, Loader2, RefreshCw, type LucideIcon } from 'lucide-react'
import { clamp, cx } from '@/lib/format'

export type Tone = 'neutral' | 'accent' | 'success' | 'warning' | 'danger' | 'purple' | 'cyan'

const toneText: Record<Tone, string> = {
  neutral: 'text-muted',
  accent: 'text-accent',
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-danger',
  purple: 'text-purple',
  cyan: 'text-cyan',
}

const toneSoft: Record<Tone, string> = {
  neutral: 'bg-raised text-muted border-line',
  accent: 'bg-accent/12 text-accent border-accent/25',
  success: 'bg-success/12 text-success border-success/25',
  warning: 'bg-warning/12 text-warning border-warning/25',
  danger: 'bg-danger/12 text-danger border-danger/25',
  purple: 'bg-purple/12 text-purple border-purple/25',
  cyan: 'bg-cyan/12 text-cyan border-cyan/25',
}

const toneSolid: Record<Tone, string> = {
  neutral: 'bg-faint',
  accent: 'bg-accent',
  success: 'bg-success',
  warning: 'bg-warning',
  danger: 'bg-danger',
  purple: 'bg-purple',
  cyan: 'bg-cyan',
}

/* ---------- Card ---------- */

export interface CardProps extends HTMLAttributes<HTMLDivElement> {
  padded?: boolean
  interactive?: boolean
}

export function Card({ padded = true, interactive = false, className, ...rest }: CardProps) {
  return (
    <div
      className={cx(
        'rounded-2xl border border-line bg-card shadow-card',
        padded && 'p-4 sm:p-5',
        interactive && 'transition-colors hover:border-line-strong hover:bg-raised/60',
        className,
      )}
      {...rest}
    />
  )
}

export function CardHeader({
  title,
  icon: Icon,
  actions,
  subtitle,
}: {
  title: ReactNode
  icon?: LucideIcon
  actions?: ReactNode
  subtitle?: ReactNode
}) {
  return (
    <div className="mb-4 flex items-start justify-between gap-3">
      <div className="min-w-0">
        <h2 className="flex items-center gap-2 text-[15px] font-semibold text-fg">
          {Icon && <Icon className="size-4 shrink-0 text-muted" aria-hidden />}
          <span className="truncate">{title}</span>
        </h2>
        {subtitle && <p className="mt-0.5 text-xs text-muted">{subtitle}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}

/* ---------- Button ---------- */

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger'
export type ButtonSize = 'sm' | 'md' | 'lg'

const buttonVariant: Record<ButtonVariant, string> = {
  primary: 'bg-accent-strong text-accent-fg hover:bg-accent border border-transparent',
  secondary: 'bg-raised text-fg border border-line hover:border-line-strong',
  ghost: 'bg-transparent text-muted hover:text-fg hover:bg-raised border border-transparent',
  danger: 'bg-danger/15 text-danger border border-danger/30 hover:bg-danger/25',
}

const buttonSize: Record<ButtonSize, string> = {
  sm: 'h-8 px-3 text-xs gap-1.5 rounded-lg',
  md: 'h-10 px-4 text-sm gap-2 rounded-xl',
  lg: 'h-11 px-5 text-sm gap-2 rounded-xl',
}

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  size?: ButtonSize
  icon?: LucideIcon
  loading?: boolean
  block?: boolean
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', icon: Icon, loading, block, disabled, className, children, type, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type ?? 'button'}
      disabled={disabled || loading}
      className={cx(
        'inline-flex select-none items-center justify-center font-medium whitespace-nowrap transition-colors',
        'disabled:opacity-50',
        buttonVariant[variant],
        buttonSize[size],
        block && 'w-full',
        className,
      )}
      {...rest}
    >
      {loading ? (
        <Loader2 className="ms-spin size-4 shrink-0" aria-hidden />
      ) : (
        Icon && <Icon className="size-4 shrink-0" aria-hidden />
      )}
      {children}
    </button>
  )
})

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  icon: LucideIcon
  /** Accessible name; also shown as the tooltip. */
  label: string
  tone?: Tone
  size?: 'sm' | 'md'
  loading?: boolean
}

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { icon: Icon, label, tone = 'neutral', size = 'md', loading, disabled, className, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled || loading}
      className={cx(
        'inline-flex shrink-0 items-center justify-center rounded-lg transition-colors hover:bg-raised disabled:opacity-50',
        size === 'sm' ? 'size-8' : 'size-10',
        tone === 'neutral' ? 'text-muted hover:text-fg' : toneText[tone],
        className,
      )}
      {...rest}
    >
      {loading ? <Loader2 className="ms-spin size-4" aria-hidden /> : <Icon className="size-[18px]" aria-hidden />}
    </button>
  )
})

/* ---------- Badge / status ---------- */

export function Badge({ tone = 'neutral', children, className }: { tone?: Tone; children: ReactNode; className?: string }) {
  return (
    <span
      className={cx(
        'inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[11px] font-medium leading-none whitespace-nowrap',
        toneSoft[tone],
        className,
      )}
    >
      {children}
    </span>
  )
}

export function StatusDot({ tone = 'neutral', pulse = false }: { tone?: Tone; pulse?: boolean }) {
  return (
    <span className="relative inline-flex size-2 shrink-0" aria-hidden>
      {pulse && <span className={cx('absolute inset-0 animate-ping rounded-full opacity-60', toneSolid[tone])} />}
      <span className={cx('relative size-2 rounded-full', toneSolid[tone])} />
    </span>
  )
}

/** A colored dot with a label, e.g. "● Çalışıyor". */
export function Status({ tone, children, pulse }: { tone: Tone; children: ReactNode; pulse?: boolean }) {
  return (
    <span className={cx('inline-flex items-center gap-1.5 text-xs font-medium', toneText[tone])}>
      <StatusDot tone={tone} pulse={pulse} />
      {children}
    </span>
  )
}

/** Rounded colored square holding an icon, as used on metric cards. */
export function IconTile({ icon: Icon, tone = 'accent', size = 'md' }: { icon: LucideIcon; tone?: Tone; size?: 'sm' | 'md' | 'lg' }) {
  const box = size === 'sm' ? 'size-8 rounded-lg' : size === 'lg' ? 'size-12 rounded-2xl' : 'size-10 rounded-xl'
  const icon = size === 'sm' ? 'size-4' : size === 'lg' ? 'size-6' : 'size-5'
  return (
    <span className={cx('inline-flex shrink-0 items-center justify-center border', box, toneSoft[tone])} aria-hidden>
      <Icon className={icon} />
    </span>
  )
}

/* ---------- Progress ---------- */

/** Tone for a usage percentage: warning from 75, danger from 90. */
export function usageTone(percent: number, base: Tone = 'accent'): Tone {
  if (percent >= 90) return 'danger'
  if (percent >= 75) return 'warning'
  return base
}

export function ProgressBar({
  value,
  tone,
  label,
  className,
}: {
  /** 0-100 */
  value: number
  tone?: Tone
  label?: string
  className?: string
}) {
  const v = clamp(Number.isFinite(value) ? value : 0, 0, 100)
  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(v)}
      aria-label={label}
      className={cx('h-1.5 w-full overflow-hidden rounded-full bg-raised', className)}
    >
      <div
        className={cx('h-full rounded-full transition-[width] duration-500', toneSolid[tone ?? usageTone(v)])}
        style={{ width: `${v}%` }}
      />
    </div>
  )
}

/* ---------- Loading / empty / error ---------- */

export function Spinner({ className, label = 'Yükleniyor' }: { className?: string; label?: string }) {
  return (
    <span role="status" aria-label={label} className="inline-flex">
      <Loader2 className={cx('ms-spin size-5 text-muted', className)} aria-hidden />
    </span>
  )
}

export function LoadingState({ label = 'Yükleniyor…', className }: { label?: string; className?: string }) {
  return (
    <div className={cx('flex flex-col items-center justify-center gap-3 py-10 text-sm text-muted', className)}>
      <Spinner label={label} />
      <span>{label}</span>
    </div>
  )
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cx('animate-pulse rounded-lg bg-raised', className)} aria-hidden />
}

export function EmptyState({
  icon: Icon = Inbox,
  title,
  description,
  action,
  className,
}: {
  icon?: LucideIcon
  title: string
  description?: ReactNode
  action?: ReactNode
  className?: string
}) {
  return (
    <div className={cx('flex flex-col items-center justify-center gap-2 px-4 py-10 text-center', className)}>
      <IconTile icon={Icon} tone="neutral" size="lg" />
      <p className="mt-1 text-sm font-medium text-fg">{title}</p>
      {description && <p className="max-w-md text-xs text-muted">{description}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}

export function ErrorState({
  message,
  onRetry,
  title = 'Veriler alınamadı',
  className,
}: {
  message: string
  onRetry?: () => void
  title?: string
  className?: string
}) {
  return (
    <div role="alert" className={cx('flex flex-col items-center justify-center gap-2 px-4 py-8 text-center', className)}>
      <IconTile icon={AlertTriangle} tone="danger" size="lg" />
      <p className="mt-1 text-sm font-medium text-fg">{title}</p>
      <p className="max-w-md text-xs text-muted">{message}</p>
      {onRetry && (
        <Button size="sm" icon={RefreshCw} onClick={onRetry} className="mt-2">
          Tekrar Dene
        </Button>
      )}
    </div>
  )
}

/** Inline message box for warnings and errors inside forms and dialogs. */
export function Alert({ tone = 'warning', title, children, className }: { tone?: Tone; title?: string; children?: ReactNode; className?: string }) {
  return (
    <div role={tone === 'danger' ? 'alert' : 'status'} className={cx('flex gap-2.5 rounded-xl border px-3 py-2.5 text-xs', toneSoft[tone], className)}>
      <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
      <div className="min-w-0 text-fg">
        {title && <p className="font-semibold">{title}</p>}
        {children && <div className={cx('text-muted', title && 'mt-0.5')}>{children}</div>}
      </div>
    </div>
  )
}

/* ---------- Form controls ---------- */

export function Field({
  label,
  hint,
  error,
  htmlFor,
  children,
  className,
}: {
  label: string
  hint?: ReactNode
  error?: string | null
  htmlFor?: string
  children: ReactNode
  className?: string
}) {
  return (
    <div className={cx('flex flex-col gap-1.5', className)}>
      <label htmlFor={htmlFor} className="text-xs font-medium text-muted">
        {label}
      </label>
      {children}
      {error ? (
        <p role="alert" className="text-xs text-danger">
          {error}
        </p>
      ) : (
        hint && <p className="text-xs text-faint">{hint}</p>
      )}
    </div>
  )
}

const controlClass =
  'h-10 w-full rounded-xl border border-line bg-surface px-3 text-sm text-fg placeholder:text-faint ' +
  'transition-colors hover:border-line-strong focus:border-accent focus:outline-none disabled:opacity-50'

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  invalid?: boolean
}

export const Input = forwardRef<HTMLInputElement, InputProps>(function Input({ invalid, className, ...rest }, ref) {
  return <input ref={ref} aria-invalid={invalid || undefined} className={cx(controlClass, invalid && 'border-danger', className)} {...rest} />
})

export const Select = forwardRef<HTMLSelectElement, SelectHTMLAttributes<HTMLSelectElement>>(function Select(
  { className, children, ...rest },
  ref,
) {
  return (
    <select ref={ref} className={cx(controlClass, 'pr-8', className)} {...rest}>
      {children}
    </select>
  )
})

export function Switch({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean
  onChange: (checked: boolean) => void
  /** Accessible name. */
  label: string
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cx(
        'relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border transition-colors disabled:opacity-50',
        checked ? 'border-accent bg-accent-strong' : 'border-line-strong bg-raised',
      )}
    >
      <span className={cx('inline-block size-4 rounded-full bg-white shadow transition-transform', checked ? 'translate-x-6' : 'translate-x-1')} />
    </button>
  )
}

/* ---------- Tabs ---------- */

export interface TabItem<T extends string> {
  id: T
  label: string
  count?: number
}

/** Pill-style tab bar, as on the dashboard's app category filter. */
export function Tabs<T extends string>({
  items,
  value,
  onChange,
  label,
  className,
}: {
  items: TabItem<T>[]
  value: T
  onChange: (id: T) => void
  label: string
  className?: string
}) {
  return (
    <div role="tablist" aria-label={label} className={cx('flex max-w-full gap-1 overflow-x-auto rounded-xl border border-line bg-surface p-1', className)}>
      {items.map((item) => {
        const active = item.id === value
        return (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(item.id)}
            className={cx(
              'inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg px-3 text-xs font-medium transition-colors',
              active ? 'bg-accent-strong text-accent-fg' : 'text-muted hover:bg-raised hover:text-fg',
            )}
          >
            {item.label}
            {item.count !== undefined && (
              <span className={cx('rounded px-1 text-[10px]', active ? 'bg-white/20' : 'bg-raised text-faint')}>{item.count}</span>
            )}
          </button>
        )
      })}
    </div>
  )
}

/* ---------- Page scaffolding ---------- */

export function PageHeader({
  title,
  description,
  actions,
  icon: Icon,
}: {
  title: string
  description?: ReactNode
  actions?: ReactNode
  icon?: LucideIcon
}) {
  return (
    <header className="mb-5 flex flex-wrap items-center justify-between gap-3">
      <div className="flex min-w-0 items-center gap-3">
        {Icon && <IconTile icon={Icon} tone="accent" />}
        <div className="min-w-0">
          <h1 className="truncate text-xl font-semibold text-fg sm:text-2xl">{title}</h1>
          {description && <p className="mt-0.5 text-sm text-muted">{description}</p>}
        </div>
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </header>
  )
}

/** Label/value rows, as in the "Sistem Bilgileri" panel. */
export function KeyValueList({ items }: { items: Array<{ label: string; value: ReactNode; icon?: LucideIcon }> }) {
  return (
    <dl className="flex flex-col gap-2.5 text-sm">
      {items.map(({ label, value, icon: Icon }) => (
        <div key={label} className="flex items-start gap-3">
          <dt className="flex w-36 shrink-0 items-center gap-2 text-muted">
            {Icon && <Icon className="size-4 shrink-0 text-faint" aria-hidden />}
            <span className="truncate">{label}</span>
          </dt>
          <dd className="min-w-0 flex-1 break-words text-fg">{value}</dd>
        </div>
      ))}
    </dl>
  )
}

/** Horizontally scrollable wrapper for tables on small screens. */
export function TableWrap({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cx('-mx-4 overflow-x-auto px-4 sm:-mx-5 sm:px-5', className)}>{children}</div>
}

export const tableClass = {
  table: 'w-full min-w-[560px] border-collapse text-left text-sm',
  th: 'border-b border-line px-2 py-2 text-[11px] font-medium uppercase tracking-wide text-faint whitespace-nowrap',
  td: 'border-b border-line/60 px-2 py-2.5 align-middle text-fg',
  row: 'transition-colors hover:bg-raised/50',
}
