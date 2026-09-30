import { Link } from 'react-router-dom'
import { Compass } from 'lucide-react'
import { EmptyState } from '@/components/ui'
import { messages } from '@/i18n'

const t = messages({
  tr: {
    title: 'Sayfa bulunamadı',
    text: 'Aradığınız sayfa taşınmış veya hiç var olmamış olabilir.',
    home: 'Ana Sayfaya Dön',
    forbiddenTitle: 'Bu sayfaya erişim yetkiniz yok',
    forbiddenText: 'Bu bölüm yalnızca yönetici hesapları tarafından kullanılabilir.',
  },
})

const linkClass =
  'inline-flex h-10 items-center rounded-xl bg-accent-strong px-4 text-sm font-medium text-accent-fg transition-colors hover:bg-accent'

export default function NotFoundPage() {
  return (
    <EmptyState
      icon={Compass}
      title={t('title')}
      description={t('text')}
      className="py-24"
      action={
        <Link to="/" className={linkClass}>
          {t('home')}
        </Link>
      }
    />
  )
}

export function ForbiddenPage() {
  return (
    <EmptyState
      icon={Compass}
      title={t('forbiddenTitle')}
      description={t('forbiddenText')}
      className="py-24"
      action={
        <Link to="/" className={linkClass}>
          {t('home')}
        </Link>
      }
    />
  )
}
