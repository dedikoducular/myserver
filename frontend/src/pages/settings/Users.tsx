import { useEffect, useState, type FormEvent } from 'react'
import { KeyRound, Pencil, Trash2, UserPlus, Users } from 'lucide-react'
import {
  Alert,
  Badge,
  Button,
  Card,
  CardHeader,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  Input,
  LoadingState,
  Modal,
  Select,
  Switch,
} from '@/components/ui'
import { useAction, useQuery } from '@/hooks/useApi'
import { formatRelative } from '@/lib/format'
import { messages } from '@/i18n'
import { api } from '@/services/api'
import { useAuth } from '@/stores/auth'
import { toast } from '@/stores/ui'
import type { Role, User } from '@/types/api'

const t = messages({
  tr: {
    title: 'Kullanıcılar',
    subtitle: 'Panele giriş yapabilen hesaplar',
    add: 'Kullanıcı Ekle',
    empty: 'Kullanıcı bulunamadı',
    you: 'Siz',
    admin: 'Yönetici',
    user: 'Kullanıcı',
    disabled: 'Devre dışı',
    lastLogin: 'Son giriş: {when}',
    never: 'Hiç giriş yapmadı',
    edit: '{name} kullanıcısını düzenle',
    remove: '{name} kullanıcısını sil',
    username: 'Kullanıcı adı',
    usernameHint: 'Küçük harfle başlamalı; küçük harf, rakam, "-" ve "_" içerebilir.',
    password: 'Parola',
    passwordHint: 'En az 10 karakter.',
    newPassword: 'Yeni parola',
    newPasswordHint: 'Parolayı değiştirmek istemiyorsanız boş bırakın.',
    role: 'Rol',
    roleHint: 'Yöneticiler tüm işlemleri yapabilir. Kullanıcılar yalnızca görüntüleyebilir.',
    active: 'Hesap etkin',
    create: 'Oluştur',
    save: 'Kaydet',
    cancel: 'Vazgeç',
    created: 'Kullanıcı oluşturuldu.',
    updated: 'Kullanıcı güncellendi.',
    deleted: 'Kullanıcı silindi.',
    addTitle: 'Yeni kullanıcı',
    editTitle: 'Kullanıcıyı düzenle',
    deleteTitle: 'Kullanıcı silinsin mi?',
    deleteText: '"{name}" hesabı kalıcı olarak silinecek ve açık oturumları kapatılacak.',
    delete: 'Sil',
    resetNote: 'Parola veya yetki değiştiğinde kullanıcının açık oturumları kapatılır.',
  },
})

function roleLabel(role: Role): string {
  return role === 'admin' ? t('admin') : t('user')
}

function UserDialog({ user, open, onClose, onSaved }: { user: User | null; open: boolean; onClose: () => void; onSaved: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [role, setRole] = useState<Role>('user')
  const [active, setActive] = useState(true)
  const self = useAuth((s) => s.user?.id === user?.id)

  const save = useAction(
    () =>
      user
        ? api.put(`/auth/users/${user.id}`, { role, disabled: !active, ...(password ? { password } : {}) })
        : api.post('/auth/users', { username: username.trim(), password, role }),
    {
      onSuccess: () => {
        toast.success(user ? t('updated') : t('created'))
        onSaved()
        onClose()
      },
    },
  )

  useEffect(() => {
    if (!open) return
    setUsername(user?.username ?? '')
    setPassword('')
    setRole(user?.role ?? 'user')
    setActive(user ? !user.disabled : true)
    save.clearError()
    // Reset only when the dialog opens for a (different) user.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, user])

  const submit = (e: FormEvent) => {
    e.preventDefault()
    void save.run()
  }

  return (
    <Modal open={open} onClose={onClose} title={user ? t('editTitle') : t('addTitle')} size="sm" busy={save.pending}>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <Field label={t('username')} htmlFor="user-name" hint={user ? undefined : t('usernameHint')}>
          <Input
            id="user-name"
            value={username}
            disabled={Boolean(user)}
            required
            autoComplete="off"
            autoCapitalize="none"
            spellCheck={false}
            data-autofocus={user ? undefined : true}
            onChange={(e) => setUsername(e.target.value.toLowerCase())}
          />
        </Field>
        <Field label={user ? t('newPassword') : t('password')} htmlFor="user-password" hint={user ? t('newPasswordHint') : t('passwordHint')}>
          <Input
            id="user-password"
            type="password"
            autoComplete="new-password"
            required={!user}
            minLength={10}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </Field>
        <Field label={t('role')} htmlFor="user-role" hint={t('roleHint')}>
          <Select id="user-role" value={role} disabled={self} onChange={(e) => setRole(e.target.value as Role)}>
            <option value="admin">{t('admin')}</option>
            <option value="user">{t('user')}</option>
          </Select>
        </Field>
        {user && (
          <div className="flex items-center justify-between gap-3 rounded-xl border border-line bg-surface px-3 py-2.5">
            <span className="text-sm text-fg">{t('active')}</span>
            <Switch checked={active} onChange={setActive} label={t('active')} disabled={self} />
          </div>
        )}
        {user && <p className="text-xs text-faint">{t('resetNote')}</p>}
        {save.error && <Alert tone="danger">{save.error}</Alert>}
        <div className="mt-1 flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={save.pending}>
            {t('cancel')}
          </Button>
          <Button type="submit" variant="primary" loading={save.pending}>
            {user ? t('save') : t('create')}
          </Button>
        </div>
      </form>
    </Modal>
  )
}

export function UsersSection() {
  const { data, error, loading, reload } = useQuery<User[]>('/auth/users')
  const me = useAuth((s) => s.user)
  const [editing, setEditing] = useState<User | null>(null)
  const [dialog, setDialog] = useState(false)
  const [removing, setRemoving] = useState<User | null>(null)

  const remove = useAction((u: User) => api.del(`/auth/users/${u.id}`), {
    onSuccess: () => {
      toast.success(t('deleted'))
      setRemoving(null)
      void reload()
    },
  })

  return (
    <Card>
      <CardHeader
        title={t('title')}
        subtitle={t('subtitle')}
        icon={Users}
        actions={
          <Button
            variant="primary"
            size="sm"
            icon={UserPlus}
            onClick={() => {
              setEditing(null)
              setDialog(true)
            }}
          >
            {t('add')}
          </Button>
        }
      />
      {loading ? (
        <LoadingState />
      ) : error ? (
        <ErrorState message={error} onRetry={() => void reload()} />
      ) : !data || data.length === 0 ? (
        <EmptyState icon={Users} title={t('empty')} />
      ) : (
        <ul className="flex flex-col divide-y divide-line">
          {data.map((u) => (
            <li key={u.id} className="flex items-center gap-3 py-3">
              <span className="inline-flex size-10 shrink-0 items-center justify-center rounded-full bg-raised text-sm font-semibold text-fg">
                {u.username.charAt(0).toLocaleUpperCase('tr-TR')}
              </span>
              <div className="min-w-0 flex-1">
                <p className="flex flex-wrap items-center gap-1.5 text-sm font-medium text-fg">
                  <span className="truncate">{u.username}</span>
                  <Badge tone={u.role === 'admin' ? 'accent' : 'neutral'}>{roleLabel(u.role)}</Badge>
                  {u.id === me?.id && <Badge tone="success">{t('you')}</Badge>}
                  {u.disabled && <Badge tone="danger">{t('disabled')}</Badge>}
                </p>
                <p className="mt-0.5 text-xs text-muted">
                  {u.last_login_at ? t('lastLogin', { when: formatRelative(u.last_login_at) }) : t('never')}
                </p>
              </div>
              <IconButton
                icon={u.id === me?.id ? KeyRound : Pencil}
                label={t('edit', { name: u.username })}
                onClick={() => {
                  setEditing(u)
                  setDialog(true)
                }}
              />
              <IconButton
                icon={Trash2}
                tone="danger"
                label={t('remove', { name: u.username })}
                disabled={u.id === me?.id}
                onClick={() => {
                  remove.clearError()
                  setRemoving(u)
                }}
              />
            </li>
          ))}
        </ul>
      )}
      <UserDialog user={editing} open={dialog} onClose={() => setDialog(false)} onSaved={() => void reload()} />
      <ConfirmDialog
        open={removing !== null}
        onClose={() => setRemoving(null)}
        title={t('deleteTitle')}
        message={t('deleteText', { name: removing?.username ?? '' })}
        confirmLabel={t('delete')}
        danger
        error={remove.error}
        onConfirm={async () => {
          if (removing) await remove.run(removing)
        }}
      />
    </Card>
  )
}
