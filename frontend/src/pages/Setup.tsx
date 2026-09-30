import { useMemo, useState, type FormEvent, type ReactNode } from 'react'
import {
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCircle2,
  Clock,
  Container,
  HardDrive,
  RefreshCw,
  Server,
  ShieldCheck,
  UserRound,
  XCircle,
  type LucideIcon,
} from 'lucide-react'
import { Alert, Button, Card, ErrorState, Field, IconTile, Input, LoadingState, Select } from '@/components/ui'
import { useQuery } from '@/hooks/useApi'
import { cx } from '@/lib/format'
import { messages } from '@/i18n'
import { brandIcon as Brand } from '@/layouts/navigation'
import { api, errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'
import type { AuthStatus, SetupCheck, SetupChecks, SetupResult } from '@/types/api'

const t = messages({
  tr: {
    welcome: 'Hoş Geldiniz',
    intro: 'MyServer kurulumunu birkaç adımda tamamlayalım.',
    stepAdmin: 'Yönetici',
    stepName: 'Sunucu Adı',
    stepTime: 'Saat Dilimi',
    stepStorage: 'Depolama',
    stepDocker: 'Docker',
    stepDone: 'Tamamla',
    adminTitle: 'Yönetici hesabı oluşturun',
    adminText: 'Bu hesap panelin tam yetkili kullanıcısıdır. Güvenlik nedeniyle "root" adı kullanılamaz.',
    username: 'Kullanıcı adı',
    usernameHint: 'Küçük harfle başlamalı; küçük harf, rakam, "-" ve "_" içerebilir.',
    usernameInvalid: 'Kullanıcı adı 3-32 karakter olmalı ve küçük harfle başlamalıdır.',
    usernameRoot: '"root" kullanıcı adı panelde kullanılamaz.',
    password: 'Parola',
    passwordHint: 'En az 10 karakter. Tahmin edilmesi zor bir parola seçin.',
    passwordShort: 'Parola en az 10 karakter olmalıdır.',
    passwordSame: 'Parola kullanıcı adıyla aynı olamaz.',
    repeat: 'Parola (tekrar)',
    mismatch: 'Parolalar eşleşmiyor.',
    nameTitle: 'Sunucu adını belirleyin',
    nameText: 'Sunucunuz ağda bu adla görünür.',
    hostname: 'Sunucu adı',
    hostnameHint: 'Harf, rakam ve "-" kullanın. En fazla 63 karakter.',
    hostnameInvalid: 'Sunucu adı geçersiz.',
    timeTitle: 'Saat dilimini seçin',
    timeText: 'Kayıtlar, zamanlanmış yedekler ve grafikler bu saat dilimini kullanır.',
    timezone: 'Saat dilimi',
    timezoneSearch: 'Saat dilimi ara',
    timezoneEmpty: 'Eşleşen saat dilimi yok',
    storageTitle: 'Depolama denetimi',
    dockerTitle: 'Docker denetimi',
    helperTitle: 'Sistem yetkileri',
    recheck: 'Yeniden Denetle',
    continueAnyway: 'Bu sorun kurulumu engellemez; daha sonra giderebilirsiniz.',
    dockerMissing: 'Docker çalışmadığında uygulama kurulumu ve konteyner yönetimi kullanılamaz.',
    doneTitle: 'Kurulumu tamamlayın',
    doneText: 'Aşağıdaki bilgileri kontrol edin. Tamamladıktan sonra kurulum sihirbazı bir daha kullanılamaz.',
    summaryAdmin: 'Yönetici',
    summaryHost: 'Sunucu adı',
    summaryTime: 'Saat dilimi',
    back: 'Geri',
    next: 'İleri',
    finish: 'Kurulumu Tamamla',
    completed: 'Kurulum tamamlandı',
    completedText: 'MyServer kullanıma hazır.',
    warnings: 'Bazı ayarlar uygulanamadı',
    open: 'Panele Git',
    ok: 'Hazır',
    problem: 'Sorun var',
    step: 'Adım {n} / {total}',
  },
})

const usernameRe = /^[a-z][a-z0-9_-]{2,31}$/
const hostnameRe = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

interface Step {
  id: 'admin' | 'name' | 'time' | 'storage' | 'docker' | 'done'
  label: string
  icon: LucideIcon
}

const steps: Step[] = [
  { id: 'admin', label: t('stepAdmin'), icon: UserRound },
  { id: 'name', label: t('stepName'), icon: Server },
  { id: 'time', label: t('stepTime'), icon: Clock },
  { id: 'storage', label: t('stepStorage'), icon: HardDrive },
  { id: 'docker', label: t('stepDocker'), icon: Container },
  { id: 'done', label: t('stepDone'), icon: ShieldCheck },
]

function Stepper({ index }: { index: number }) {
  return (
    <ol className="mb-6 flex items-center" aria-label={t('step', { n: index + 1, total: steps.length })}>
      {steps.map((s, i) => {
        const done = i < index
        const current = i === index
        return (
          <li key={s.id} className={cx('flex items-center', i < steps.length - 1 && 'flex-1')} aria-current={current ? 'step' : undefined}>
            <span className="flex flex-col items-center gap-1.5">
              <span
                className={cx(
                  'inline-flex size-9 items-center justify-center rounded-full border text-sm font-semibold transition-colors',
                  done && 'border-accent bg-accent-strong text-accent-fg',
                  current && 'border-accent bg-accent/15 text-accent',
                  !done && !current && 'border-line bg-surface text-faint',
                )}
              >
                {done ? <Check className="size-4" aria-hidden /> : i + 1}
              </span>
              <span className={cx('hidden text-[11px] sm:block', current ? 'text-fg' : 'text-faint')}>{s.label}</span>
            </span>
            {i < steps.length - 1 && <span className={cx('mx-1.5 mb-0 h-px flex-1 sm:mx-2 sm:mb-5', done ? 'bg-accent' : 'bg-line')} aria-hidden />}
          </li>
        )
      })}
    </ol>
  )
}

function CheckRow({ icon, title, check, extra }: { icon: LucideIcon; title: string; check: SetupCheck; extra?: ReactNode }) {
  return (
    <div className="flex items-start gap-3 rounded-xl border border-line bg-surface p-4">
      <IconTile icon={icon} tone={check.ok ? 'success' : 'warning'} />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-sm font-medium text-fg">{title}</p>
          <span className={cx('inline-flex items-center gap-1 text-xs font-medium', check.ok ? 'text-success' : 'text-warning')}>
            {check.ok ? <CheckCircle2 className="size-4" aria-hidden /> : <XCircle className="size-4" aria-hidden />}
            {check.ok ? t('ok') : t('problem')}
          </span>
        </div>
        <p className="mt-1 text-sm text-muted">{check.message}</p>
        {check.detail && <p className="mt-0.5 text-xs text-faint">{check.detail}</p>}
        {extra}
      </div>
    </div>
  )
}

export default function SetupPage() {
  const adopt = useAuth((s) => s.adopt)
  const checks = useQuery<SetupChecks>('/auth/setup/checks')
  const zones = useQuery<string[]>('/auth/setup/timezones')

  const [index, setIndex] = useState(0)
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [repeat, setRepeat] = useState('')
  const [hostname, setHostname] = useState<string | null>(null)
  const [timezone, setTimezone] = useState<string | null>(null)
  const [zoneFilter, setZoneFilter] = useState('')
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [warnings, setWarnings] = useState<string[] | null>(null)

  // Until the user edits them, the fields follow the values detected on
  // the server.
  const host = hostname ?? checks.data?.hostname.toLowerCase() ?? ''
  const zone = timezone ?? checks.data?.timezone ?? 'Europe/Istanbul'

  // The select shows its first option when its value is not among the
  // options. Searching therefore selects the first match for real, so what
  // the user sees is what gets saved.
  const searchZone = (text: string) => {
    setZoneFilter(text)
    const q = text.trim().toLowerCase().replace(/\s+/g, '_')
    if (!q) return
    const hits = (zones.data ?? []).filter((z) => z.toLowerCase().includes(q))
    if (hits.length > 0 && !hits.includes(zone)) setTimezone(hits[0]!)
  }

  const filteredZones = useMemo(() => {
    const all = zones.data ?? []
    const q = zoneFilter.trim().toLowerCase().replace(/\s+/g, '_')
    const list = q ? all.filter((z) => z.toLowerCase().includes(q)) : all
    return list.includes(zone) || q ? list : [zone, ...list]
  }, [zones.data, zoneFilter, zone])

  const usernameError = !usernameRe.test(username) ? t('usernameInvalid') : username === 'root' ? t('usernameRoot') : null
  const passwordError =
    password.length < 10 ? t('passwordShort') : password.toLowerCase() === username.toLowerCase() ? t('passwordSame') : null
  const repeatError = password !== repeat ? t('mismatch') : null
  const hostnameError = !hostnameRe.test(host) ? t('hostnameInvalid') : null

  const step = steps[index]!
  const stepValid =
    step.id === 'admin'
      ? !usernameError && !passwordError && !repeatError
      : step.id === 'name'
        ? !hostnameError
        : step.id === 'time'
          ? Boolean(zone)
          : true

  const next = (e: FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!stepValid) return
    setTouched(false)
    setError(null)
    if (step.id === 'done') {
      void finish()
      return
    }
    setIndex((i) => Math.min(steps.length - 1, i + 1))
  }

  // The store must not learn about the new session until "Panele Git" is
  // pressed: the moment it does, the application replaces this wizard with
  // the panel and the result (with its warnings) would never be seen. So
  // the request is made here and its status is kept until then.
  const [result, setResult] = useState<AuthStatus | null>(null)

  const finish = async () => {
    if (pending) return
    setPending(true)
    try {
      const res = await api.post<SetupResult>('/auth/setup', { username, password, hostname: host, timezone: zone })
      const { warnings: list, ...status } = res
      setResult(status)
      setWarnings(list ?? [])
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  if (warnings) {
    return (
      <Shell>
        <Card className="text-center">
          <div className="flex flex-col items-center gap-3 py-4">
            <IconTile icon={CheckCircle2} tone="success" size="lg" />
            <h2 className="text-xl font-semibold text-fg">{t('completed')}</h2>
            <p className="text-sm text-muted">{t('completedText')}</p>
          </div>
          {warnings.length > 0 && (
            <Alert tone="warning" title={t('warnings')} className="mb-4 text-left">
              <ul className="list-disc pl-4">
                {warnings.map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            </Alert>
          )}
          <Button variant="primary" size="lg" block icon={ArrowRight} onClick={() => result && adopt(result)}>
            {t('open')}
          </Button>
        </Card>
      </Shell>
    )
  }

  return (
    <Shell>
      <Stepper index={index} />
      <Card>
        <form onSubmit={next} noValidate className="flex flex-col gap-5">
          {step.id === 'admin' && (
            <Section title={t('adminTitle')} text={t('adminText')}>
              <Field label={t('username')} htmlFor="setup-username" hint={t('usernameHint')} error={touched ? usernameError : null}>
                <Input
                  id="setup-username"
                  autoComplete="username"
                  autoCapitalize="none"
                  spellCheck={false}
                  autoFocus
                  invalid={touched && Boolean(usernameError)}
                  value={username}
                  onChange={(e) => setUsername(e.target.value.toLowerCase())}
                />
              </Field>
              <Field label={t('password')} htmlFor="setup-password" hint={t('passwordHint')} error={touched ? passwordError : null}>
                <Input
                  id="setup-password"
                  type="password"
                  autoComplete="new-password"
                  invalid={touched && Boolean(passwordError)}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </Field>
              <Field label={t('repeat')} htmlFor="setup-repeat" error={touched ? repeatError : null}>
                <Input
                  id="setup-repeat"
                  type="password"
                  autoComplete="new-password"
                  invalid={touched && Boolean(repeatError)}
                  value={repeat}
                  onChange={(e) => setRepeat(e.target.value)}
                />
              </Field>
            </Section>
          )}

          {step.id === 'name' && (
            <Section title={t('nameTitle')} text={t('nameText')}>
              <Field label={t('hostname')} htmlFor="setup-hostname" hint={t('hostnameHint')} error={touched ? hostnameError : null}>
                <Input
                  id="setup-hostname"
                  autoCapitalize="none"
                  spellCheck={false}
                  autoFocus
                  maxLength={63}
                  invalid={touched && Boolean(hostnameError)}
                  value={host}
                  onChange={(e) => setHostname(e.target.value.toLowerCase())}
                />
              </Field>
            </Section>
          )}

          {step.id === 'time' && (
            <Section title={t('timeTitle')} text={t('timeText')}>
              {zones.loading ? (
                <LoadingState className="py-4" />
              ) : zones.error ? (
                <ErrorState message={zones.error} onRetry={() => void zones.reload()} className="py-4" />
              ) : (
                <>
                  <Field label={t('timezoneSearch')} htmlFor="setup-zone-filter">
                    <Input id="setup-zone-filter" autoFocus spellCheck={false} value={zoneFilter} onChange={(e) => searchZone(e.target.value)} placeholder="Istanbul" />
                  </Field>
                  <Field label={t('timezone')} htmlFor="setup-zone" error={filteredZones.length === 0 ? t('timezoneEmpty') : null}>
                    <Select id="setup-zone" value={zone} onChange={(e) => setTimezone(e.target.value)}>
                      {filteredZones.map((z) => (
                        <option key={z} value={z}>
                          {z.replace(/_/g, ' ')}
                        </option>
                      ))}
                    </Select>
                  </Field>
                </>
              )}
            </Section>
          )}

          {(step.id === 'storage' || step.id === 'docker') && (
            <Section title={step.id === 'storage' ? t('storageTitle') : t('dockerTitle')}>
              {checks.loading ? (
                <LoadingState className="py-4" />
              ) : checks.error || !checks.data ? (
                <ErrorState message={checks.error ?? ''} onRetry={() => void checks.reload()} className="py-4" />
              ) : step.id === 'storage' ? (
                <>
                  <CheckRow icon={HardDrive} title={t('storageTitle')} check={checks.data.storage} />
                  <CheckRow icon={ShieldCheck} title={t('helperTitle')} check={checks.data.helper} />
                  {(!checks.data.storage.ok || !checks.data.helper.ok) && <p className="text-xs text-muted">{t('continueAnyway')}</p>}
                </>
              ) : (
                <>
                  <CheckRow icon={Container} title={t('dockerTitle')} check={checks.data.docker} />
                  {!checks.data.docker.ok && <Alert tone="warning">{t('dockerMissing')}</Alert>}
                </>
              )}
              <div>
                <Button size="sm" icon={RefreshCw} loading={checks.fetching} onClick={() => void checks.reload()}>
                  {t('recheck')}
                </Button>
              </div>
            </Section>
          )}

          {step.id === 'done' && (
            <Section title={t('doneTitle')} text={t('doneText')}>
              <dl className="flex flex-col divide-y divide-line rounded-xl border border-line bg-surface text-sm">
                {[
                  [t('summaryAdmin'), username],
                  [t('summaryHost'), host],
                  [t('summaryTime'), zone.replace(/_/g, ' ')],
                ].map(([k, v]) => (
                  <div key={k} className="flex items-center justify-between gap-3 px-4 py-3">
                    <dt className="text-muted">{k}</dt>
                    <dd className="min-w-0 truncate font-medium text-fg">{v}</dd>
                  </div>
                ))}
              </dl>
              {error && <Alert tone="danger">{error}</Alert>}
            </Section>
          )}

          <div className="flex items-center justify-between gap-2 border-t border-line pt-4">
            <Button
              variant="ghost"
              icon={ArrowLeft}
              disabled={index === 0 || pending}
              onClick={() => {
                setTouched(false)
                setIndex((i) => Math.max(0, i - 1))
              }}
            >
              {t('back')}
            </Button>
            <Button type="submit" variant="primary" loading={pending}>
              {step.id === 'done' ? t('finish') : t('next')}
              {step.id !== 'done' && <ArrowRight className="size-4" aria-hidden />}
            </Button>
          </div>
        </form>
      </Card>
    </Shell>
  )
}

function Shell({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-bg px-4 py-8">
      <div className="w-full max-w-xl">
        <div className="mb-6 flex flex-col items-center gap-3 text-center">
          <span className="inline-flex size-14 items-center justify-center rounded-2xl bg-accent-strong text-accent-fg shadow-glow">
            <Brand className="size-7" aria-hidden />
          </span>
          <div>
            <h1 className="text-2xl font-semibold tracking-tight text-fg">{t('welcome')}</h1>
            <p className="mt-1 text-sm text-muted">{t('intro')}</p>
          </div>
        </div>
        {children}
      </div>
    </div>
  )
}

function Section({ title, text, children }: { title: string; text?: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-4">
      <div>
        <h2 className="text-base font-semibold text-fg">{title}</h2>
        {text && <p className="mt-1 text-sm text-muted">{text}</p>}
      </div>
      {children}
    </section>
  )
}
