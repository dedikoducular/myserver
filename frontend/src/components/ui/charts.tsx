import { useId, useMemo } from 'react'
import { cx } from '@/lib/format'
import type { Tone } from './primitives'

// Dependency-free SVG charts. Colors come from the design tokens so they
// follow the active theme.

const toneVar: Record<Tone, string> = {
  neutral: 'var(--ms-muted)',
  accent: 'var(--ms-accent)',
  success: 'var(--ms-success)',
  warning: 'var(--ms-warning)',
  danger: 'var(--ms-danger)',
  purple: 'var(--ms-purple)',
  cyan: 'var(--ms-cyan)',
}

function linePath(values: number[], width: number, height: number, max: number, pad: number): string {
  if (values.length === 0) return ''
  const step = values.length > 1 ? width / (values.length - 1) : 0
  const usable = height - pad * 2
  return values
    .map((v, i) => {
      const x = values.length > 1 ? i * step : width
      const y = pad + usable - (Math.max(0, v) / max) * usable
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
}

export interface SparklineProps {
  values: number[]
  tone?: Tone
  /** Upper bound of the scale; defaults to the largest value. Use 100 for
   *  percentages so the line height is meaningful. */
  max?: number
  className?: string
  /** Text alternative describing the series. */
  label: string
}

/** Small live trend line used on the metric cards. */
export function Sparkline({ values, tone = 'accent', max, className, label }: SparklineProps) {
  const id = useId()
  const w = 100
  const h = 36
  const top = Math.max(max ?? 0, ...values, 1e-9)
  const line = useMemo(() => linePath(values, w, h, top, 2), [values, top])
  const color = toneVar[tone]
  return (
    <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" role="img" aria-label={label} className={cx('h-9 w-24', className)}>
      <defs>
        <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.35" />
          <stop offset="100%" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      {values.length > 1 && (
        <>
          <path d={`${line} L${w},${h} L0,${h} Z`} fill={`url(#${id})`} />
          <path d={line} fill="none" stroke={color} strokeWidth="1.6" strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
        </>
      )}
    </svg>
  )
}

export interface ChartSeries {
  id: string
  label: string
  tone: Tone
  values: number[]
}

export interface AreaChartProps {
  series: ChartSeries[]
  /** One label per sample, oldest first; a few are drawn on the x axis. */
  labels?: string[]
  formatValue: (value: number) => string
  height?: number
  className?: string
  /** Text alternative describing the chart. */
  label: string
  emptyText?: string
}

function niceMax(value: number): number {
  if (value <= 0) return 1
  const exp = Math.pow(10, Math.floor(Math.log10(value)))
  const f = value / exp
  const nice = f <= 1 ? 1 : f <= 2 ? 2 : f <= 2.5 ? 2.5 : f <= 5 ? 5 : 10
  return nice * exp
}

/** Multi-series area chart used for network traffic. */
export function AreaChart({ series, labels, formatValue, height = 220, className, label, emptyText = 'Henüz veri yok' }: AreaChartProps) {
  const id = useId()
  const w = 600
  const h = height
  const padL = 64
  const padB = 22
  const padT = 8
  const plotW = w - padL - 8
  const plotH = h - padT - padB
  const count = Math.max(0, ...series.map((s) => s.values.length))
  const top = niceMax(Math.max(0, ...series.flatMap((s) => s.values)))
  const ticks = [0, 0.25, 0.5, 0.75, 1]

  const xLabels = useMemo(() => {
    if (!labels || labels.length < 2) return []
    const n = Math.min(5, labels.length)
    return Array.from({ length: n }, (_, i) => {
      const idx = Math.round((i * (labels.length - 1)) / (n - 1))
      return { x: padL + (idx / (labels.length - 1)) * plotW, text: labels[idx] ?? '' }
    })
  }, [labels, plotW])

  return (
    <div className={className}>
      <div className="mb-2 flex flex-wrap justify-end gap-x-4 gap-y-1 text-xs text-muted">
        {series.map((s) => (
          <span key={s.id} className="inline-flex items-center gap-1.5">
            <span className="size-2 rounded-full" style={{ background: toneVar[s.tone] }} aria-hidden />
            {s.label}
            {s.values.length > 0 && <span className="text-fg">{formatValue(s.values[s.values.length - 1] ?? 0)}</span>}
          </span>
        ))}
      </div>
      <svg viewBox={`0 0 ${w} ${h}`} role="img" aria-label={label} className="block h-auto w-full" style={{ maxHeight: h }}>
        <defs>
          {series.map((s) => (
            <linearGradient key={s.id} id={`${id}-${s.id}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={toneVar[s.tone]} stopOpacity="0.3" />
              <stop offset="100%" stopColor={toneVar[s.tone]} stopOpacity="0" />
            </linearGradient>
          ))}
        </defs>
        {ticks.map((t) => {
          const y = padT + plotH - t * plotH
          return (
            <g key={t}>
              <line x1={padL} x2={w - 8} y1={y} y2={y} stroke="var(--ms-line)" strokeWidth="1" strokeDasharray={t === 0 ? undefined : '3 4'} />
              <text x={padL - 8} y={y + 4} textAnchor="end" fontSize="11" fill="var(--ms-faint)">
                {formatValue(top * t)}
              </text>
            </g>
          )
        })}
        {xLabels.map((l, i) => (
          <text key={i} x={l.x} y={h - 5} textAnchor={i === 0 ? 'start' : i === xLabels.length - 1 ? 'end' : 'middle'} fontSize="11" fill="var(--ms-faint)">
            {l.text}
          </text>
        ))}
        {count < 2 ? (
          <text x={padL + plotW / 2} y={padT + plotH / 2} textAnchor="middle" fontSize="12" fill="var(--ms-muted)">
            {emptyText}
          </text>
        ) : (
          <g transform={`translate(${padL},${padT})`}>
            {series.map((s) => {
              const line = linePath(s.values, plotW, plotH, top, 0)
              return (
                <g key={s.id}>
                  <path d={`${line} L${plotW},${plotH} L0,${plotH} Z`} fill={`url(#${id}-${s.id})`} />
                  <path d={line} fill="none" stroke={toneVar[s.tone]} strokeWidth="1.8" strokeLinejoin="round" strokeLinecap="round" />
                </g>
              )
            })}
          </g>
        )}
      </svg>
    </div>
  )
}
