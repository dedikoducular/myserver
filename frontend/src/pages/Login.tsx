import { useState, type FormEvent } from 'react'
import { LogIn } from 'lucide-react'
import { Alert, Button, Card, Field, Input } from '@/components/ui'
import { messages } from '@/i18n'
import { brandIcon as Brand } from '@/layouts/navigation'
import { errorMessage } from '@/services/api'
import { useAuth } from '@/stores/auth'

const t = messages({
  tr: {
    title: 'MyServer',
    subtitle: 'Sunucunuzu yönetmek için oturum açın',
    username: 'Kullanıcı adı',
    password: 'Parola',
    submit: 'Oturum Aç',
  },
})

export default function LoginPage() {
  const login = useAuth((s) => s.login)
  const version = useAuth((s) => s.status?.version)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setPending(true)
    setError(null)
    try {
      await login(username.trim(), password)
    } catch (err) {
      setError(errorMessage(err))
      setPassword('')
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="flex min-h-dvh items-center justify-center bg-bg px-4 py-10">
      <div className="w-full max-w-sm">
        <div className="mb-6 flex flex-col items-center gap-3 text-center">
          <span className="inline-flex size-14 items-center justify-center rounded-2xl bg-accent-strong text-accent-fg shadow-glow">
            <Brand className="size-7" aria-hidden />
          </span>
          <div>
            <h1 className="text-2xl font-semibold tracking-tight text-fg">{t('title')}</h1>
            <p className="mt-1 text-sm text-muted">{t('subtitle')}</p>
          </div>
        </div>
        <Card>
          <form onSubmit={submit} className="flex flex-col gap-4">
            <Field label={t('username')} htmlFor="login-username">
              <Input
                id="login-username"
                name="username"
                autoComplete="username"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                autoFocus
                required
                value={username}
                onChange={(e) => setUsername(e.target.value)}
              />
            </Field>
            <Field label={t('password')} htmlFor="login-password">
              <Input
                id="login-password"
                name="password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </Field>
            {error && <Alert tone="danger">{error}</Alert>}
            <Button type="submit" variant="primary" size="lg" block icon={LogIn} loading={pending}>
              {t('submit')}
            </Button>
          </form>
        </Card>
        {version && <p className="mt-4 text-center text-xs text-faint">MyServer v{version}</p>}
      </div>
    </div>
  )
}
