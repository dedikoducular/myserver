import {
  Boxes,
  Container,
  Folder,
  HardDrive,
  History,
  Home,
  LayoutGrid,
  Network,
  RefreshCw,
  Server,
  Settings,
  SquareTerminal,
  type LucideIcon,
} from 'lucide-react'
import { messages } from '@/i18n'

export const navText = messages({
  tr: {
    home: 'Ana Sayfa',
    apps: 'Uygulamalar',
    docker: 'Docker',
    files: 'Dosyalar',
    storage: 'Depolama',
    network: 'Ağ',
    services: 'Servisler',
    terminal: 'Terminal',
    backup: 'Yedekleme',
    updates: 'Güncellemeler',
    settings: 'Ayarlar',
  },
})

export type NavId =
  | 'home'
  | 'apps'
  | 'docker'
  | 'files'
  | 'storage'
  | 'network'
  | 'services'
  | 'terminal'
  | 'backup'
  | 'updates'
  | 'settings'

export interface NavItem {
  id: NavId
  path: string
  icon: LucideIcon
  adminOnly?: boolean
  /** Shown in the phone's bottom bar. */
  primary?: boolean
  /** Extra words matched by the global search. */
  keywords: string
}

export const navItems: NavItem[] = [
  { id: 'home', path: '/', icon: Home, primary: true, keywords: 'dashboard pano özet cpu ram disk sıcaklık' },
  { id: 'apps', path: '/apps', icon: LayoutGrid, primary: true, keywords: 'uygulama mağaza kur jellyfin nextcloud' },
  { id: 'docker', path: '/docker', icon: Container, primary: true, keywords: 'konteyner container imaj image volume' },
  { id: 'files', path: '/files', icon: Folder, primary: true, keywords: 'dosya klasör yükle indir zip' },
  { id: 'storage', path: '/storage', icon: HardDrive, keywords: 'disk smart bağla mount usb biçimlendir' },
  { id: 'network', path: '/network', icon: Network, keywords: 'ağ ip dns gateway güvenlik duvarı firewall ufw' },
  { id: 'services', path: '/services', icon: Server, keywords: 'servis systemd ssh samba nfs' },
  { id: 'terminal', path: '/terminal', icon: SquareTerminal, adminOnly: true, keywords: 'terminal konsol shell komut' },
  { id: 'backup', path: '/backup', icon: History, adminOnly: true, keywords: 'yedek geri yükle restore zamanlama' },
  { id: 'updates', path: '/updates', icon: RefreshCw, keywords: 'güncelleme apt paket sürüm' },
  { id: 'settings', path: '/settings', icon: Settings, adminOnly: true, keywords: 'ayar kullanıcı güvenlik görünüm tema' },
]

export const brandIcon = Boxes
