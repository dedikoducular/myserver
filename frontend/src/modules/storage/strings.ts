import { messages } from '@/i18n'
import type { Tone } from '@/components/ui'
import type { SmartStatus } from './types'

export const t = messages({
  tr: {
    title: 'Depolama',
    description: 'Diskler, dosya sistemleri ve disk sağlığı',
    refresh: 'Yenile',
    showAll: 'Sanal aygıtları göster',
    live: 'Canlı',
    liveOff: 'Canlı bağlantı yok',
    loading: 'Diskler okunuyor…',
    emptyTitle: 'Disk bulunamadı',
    emptyDescription: 'Sunucuda gösterilecek bir disk algılanmadı.',
    detectionTitle: 'Sistem diski belirlenemedi',
    detectionBody:
      'Kök dosya sisteminin hangi diskte olduğu saptanamadı. Güvenlik nedeniyle tüm diskler korumalı kabul ediliyor; bağlama, ayırma ve biçimlendirme kapalı.',
    smartMissingTitle: 'smartmontools kurulu değil',
    smartMissingBody: 'Disk sağlığı ve sıcaklığı okunamıyor. Sunucuya "smartmontools" paketini kurun.',

    systemDisk: 'Sistem Diski',
    systemExplain: 'Bu disk işletim sistemini barındırıyor ({reason}). Ayırma ve biçimlendirme işlemleri güvenlik nedeniyle kapalıdır.',
    systemExplainPlain: 'Bu disk sistem tarafından kullanılıyor. Ayırma ve biçimlendirme işlemleri güvenlik nedeniyle kapalıdır.',
    removable: 'Çıkarılabilir',
    readOnly: 'Salt okunur',
    persistent: 'Kalıcı',
    swap: 'Takas alanı',
    inUse: 'Kullanımda',
    ssd: 'SSD',
    hdd: 'HDD',
    serial: 'Seri no',
    capacity: 'Kapasite',
    noFilesystem: 'Dosya sistemi yok',
    notMounted: 'Bağlı değil',
    noPartitions: 'Bu diskte bölüm veya dosya sistemi yok.',
    usedOf: '{used} / {total}',
    free: '{size} boş',
    usageLabel: '{name} doluluk oranı',
    temperature: 'Sıcaklık',
    unknownModel: 'Bilinmeyen model',

    typeDisk: 'Disk',
    typePart: 'Bölüm',
    typeRom: 'Optik sürücü',
    typeRaid: 'RAID',
    typeLvm: 'LVM',
    typeCrypt: 'Şifreli birim',
    typeLoop: 'Sanal aygıt',

    smartPassed: 'Sağlıklı',
    smartWarning: 'Uyarı',
    smartFailed: 'Arızalı',
    smartStandby: 'Uykuda',
    smartDisabled: 'SMART kapalı',
    smartUnsupported: 'SMART desteklenmiyor',
    smartNotInstalled: 'SMART aracı yok',
    smartUnknown: 'Bilinmiyor',
    smartPending: 'SMART bekleniyor',

    mount: 'Bağla',
    unmount: 'Ayır',
    smart: 'SMART',
    browse: 'Göz At',
    format: 'Biçimlendir',
    makePersistent: 'Kalıcı Yap',
    removePersistent: 'Kalıcılığı Kaldır',
    moreActions: '{name} için diğer işlemler',
    cancel: 'Vazgeç',
    close: 'Kapat',
    continue: 'Devam Et',

    mountTitle: '{name} diskini bağla',
    mountName: 'Bağlama adı',
    mountNameHint: 'Yalnızca harf, rakam, "-" ve "_". Disk {path} konumuna bağlanır.',
    mountNameInvalid: 'Ad geçersiz. Yalnızca harf, rakam, "-" ve "_" kullanın.',
    mountPersistent: 'Kalıcı bağla',
    mountPersistentHint:
      'Sunucu yeniden başladığında disk kendiliğinden bağlanır. Disk takılı değilse açılış engellenmez.',
    mountOptions: 'Güvenli seçeneklerle bağlanır: {options}',
    mountDone: '{name}, {path} konumuna bağlandı.',
    unmountTitle: '{name} diskini ayır',
    unmountMessage: '{path} konumundaki disk ayrılacak. Diskteki veriler silinmez.',
    unmountWarning: 'Bu diski kullanan uygulamalar varsa işlem reddedilir; önce onları durdurun.',
    unmountDone: '{name} ayrıldı. Çıkarılabilir diski artık güvenle çıkarabilirsiniz.',
    persistDone: 'Kalıcı bağlama kaydı yazıldı.',
    persistRemoved: 'Kalıcı bağlama kaydı kaldırıldı.',
    persistRemoveTitle: 'Kalıcı bağlamayı kaldır',
    persistRemoveMessage: '{path} için /etc/fstab kaydı silinecek. Disk şu an bağlıysa bağlı kalır.',
    orphanTitle: 'Takılı olmayan kalıcı diskler',
    orphanDescription: 'Bu diskler için kalıcı bağlama kaydı var ancak disk şu an takılı değil.',
    remove: 'Kaldır',

    browseBlockedTitle: 'Dosya yöneticisinde açılamıyor',
    browseBlockedMessage: '{path} konumu dosya yöneticisinin izin verilen dizinleri arasında değil.',
    browseBlockedUser: 'Bu konumu açmak için bir yöneticinin dizine izin vermesi gerekir.',
    browseAllow: 'İzin Ver ve Aç',
    browseAllowed: '{path} dosya yöneticisine eklendi.',

    formatTitle: '{name} aygıtını biçimlendir',
    formatStep: 'Adım {n} / 2',
    formatPreparing: 'Disk bilgileri doğrulanıyor…',
    formatDangerTitle: 'Bu işlem geri alınamaz',
    formatDangerDisk: '{name} diskindeki tüm bölümler ve tüm veriler kalıcı olarak silinecek.',
    formatDangerPart: '{name} bölümündeki tüm veriler kalıcı olarak silinecek.',
    formatDevice: 'Aygıt',
    formatModel: 'Model',
    formatBus: 'Bağlantı',
    formatCurrent: 'Mevcut içerik',
    formatContents: 'Silinecek bölümler',
    formatEmpty: 'Boş (tanınan dosya sistemi yok)',
    formatFilesystem: 'Yeni dosya sistemi',
    formatFsHint: 'Yalnızca sunucuda aracı kurulu olan dosya sistemleri seçilebilir.',
    formatNotInstalled: '{type} (araç kurulu değil)',
    formatNoTools: 'Sunucuda dosya sistemi oluşturma araçlarının hiçbiri kurulu değil.',
    formatNoSfdisk: 'Diskin tamamını biçimlendirmek için gereken "sfdisk" aracı kurulu değil (fdisk paketi).',
    formatLabel: 'Etiket (isteğe bağlı)',
    formatLabelHint: 'En fazla {max} karakter; harf, rakam, "-" ve "_".',
    formatLabelInvalid: 'Etiket geçersiz.',
    formatWholeNote: 'Diskte yeni bir GPT bölüm tablosu ve tek bir bölüm oluşturulur.',
    formatConfirmTitle: 'Son onay: {name}',
    formatConfirmMessage:
      '{path} ({model}, {size}, seri no: {serial}) {fstype} olarak biçimlendirilecek. Üzerindeki veriler geri getirilemez.',
    formatConfirmWarning: 'Onay {seconds} saniye içinde verilmelidir. Yanlış diski seçmediğinizden emin olun.',
    formatExpired: 'Onay süresi doldu. Bilgiler yeniden doğrulandı; lütfen tekrar onaylayın.',
    formatDone: '{name} {fstype} olarak biçimlendirildi.',
    fsExt4: 'ext4 — Linux için önerilen',
    fsXfs: 'xfs — büyük dosyalar için',
    fsBtrfs: 'btrfs — anlık görüntü destekli',
    fsExfat: 'exFAT — Windows/macOS ile uyumlu',
    fsVfat: 'FAT32 — en yaygın uyumluluk, 4 GB dosya sınırı',
    unknown: 'Bilinmiyor',

    smartTitle: 'SMART — {name}',
    smartLoading: 'SMART bilgisi okunuyor…',
    smartOverall: 'Genel durum',
    smartPowerOn: 'Çalışma süresi',
    smartHours: '{hours} saat',
    smartCycles: 'Açılma sayısı',
    smartReallocated: 'Yeniden ayrılmış sektör',
    smartPendingSectors: 'Bekleyen sektör',
    smartUncorrectable: 'Düzeltilemeyen sektör',
    smartPercentUsed: 'Kullanılan ömür',
    smartSpare: 'Yedek alan',
    smartSpareValue: '%{value} (eşik %{threshold})',
    smartMediaErrors: 'Ortam hataları',
    smartCritical: 'Kritik uyarı',
    smartCriticalNone: 'Yok',
    smartCriticalCode: 'Var (kod {code})',
    smartUnsafe: 'Güvensiz kapanma',
    smartFirmware: 'Yazılım sürümü',
    smartProtocol: 'Protokol',
    smartChecked: 'Son denetim',
    smartProblems: 'Saptanan sorunlar',
    smartAttributes: 'Öznitelik tablosu',
    smartNoAttributes: 'Bu disk öznitelik tablosu bildirmiyor.',
    smartAttrId: 'No',
    smartAttrName: 'Öznitelik',
    smartAttrValue: 'Değer',
    smartAttrWorst: 'En kötü',
    smartAttrThresh: 'Eşik',
    smartAttrRaw: 'Ham değer',
    smartAttrState: 'Durum',
    smartAttrOk: 'Normal',
    smartAttrFailing: 'Eşik altında',
    smartAttrPast: 'Geçmişte eşik altında',
    smartRefresh: 'Şimdi Oku',
    smartShortTest: 'Kısa Test',
    smartLongTest: 'Uzun Test',
    smartTestStarted: 'SMART testi başlatıldı. Sonuç, test bitince disk tarafından kaydedilir.',
    smartStandbyNote: 'Disk uykuda olduğu için uyandırılmadı. Güncel değerleri okumak için "Şimdi Oku" düğmesini kullanın.',
    smartUnsupportedNote: 'Bu disk veya USB dönüştürücü SMART bilgisini iletmiyor.',
    smartDisabledNote: 'Bu diskte SMART özelliği kapalı.',
    smartNotInstalledNote: 'Sunucuda "smartmontools" paketi kurulu değil.',

    settingsTitle: 'Depolama',
    settingsSubtitle: 'Disk sağlığı denetimi ve bildirimler',
    settingsInterval: 'SMART denetim aralığı (dakika)',
    settingsIntervalHint: '10 ile 1440 dakika arasında. Uykudaki diskler denetim için uyandırılmaz.',
    settingsTemp: 'Disk sıcaklığı uyarı eşiği (°C)',
    settingsTempHint: '30 ile 90 °C arasında. Eşik aşıldığında SMART uyarısı bildirilir.',
    settingsNotify: 'Çıkarılabilir disk takıldığında bildir',
    settingsNotifyHint: 'USB disk takıldığında bildirim merkezine bilgi düşer.',
    settingsSave: 'Kaydet',
    settingsSaved: 'Depolama ayarları kaydedildi.',
    settingsInvalid: 'Değer {min} ile {max} arasında olmalıdır.',
    settingsAdminOnly: 'Bu ayarları yalnızca yöneticiler değiştirebilir.',
  },
})

export function smartLabel(status: SmartStatus | undefined): string {
  switch (status) {
    case 'passed':
      return t('smartPassed')
    case 'warning':
      return t('smartWarning')
    case 'failed':
      return t('smartFailed')
    case 'standby':
      return t('smartStandby')
    case 'disabled':
      return t('smartDisabled')
    case 'unsupported':
      return t('smartUnsupported')
    case 'not_installed':
      return t('smartNotInstalled')
    case 'unknown':
      return t('smartUnknown')
    default:
      return t('smartPending')
  }
}

export function smartTone(status: SmartStatus | undefined): Tone {
  switch (status) {
    case 'passed':
      return 'success'
    case 'warning':
      return 'warning'
    case 'failed':
      return 'danger'
    default:
      return 'neutral'
  }
}

export function busLabel(transport: string): string {
  switch (transport) {
    case '':
      return ''
    case 'nvme':
      return 'NVMe'
    case 'mmc':
      return 'SD/eMMC'
    default:
      return transport.toUpperCase()
  }
}

export function typeLabel(type: string): string {
  switch (type) {
    case 'disk':
      return t('typeDisk')
    case 'part':
      return t('typePart')
    case 'rom':
      return t('typeRom')
    case 'lvm':
      return t('typeLvm')
    case 'crypt':
      return t('typeCrypt')
    case 'loop':
      return t('typeLoop')
    default:
      return type.startsWith('raid') ? t('typeRaid') : type.toUpperCase()
  }
}

export function fsDescription(type: string): string {
  switch (type) {
    case 'ext4':
      return t('fsExt4')
    case 'xfs':
      return t('fsXfs')
    case 'btrfs':
      return t('fsBtrfs')
    case 'exfat':
      return t('fsExfat')
    case 'vfat':
      return t('fsVfat')
    default:
      return type
  }
}

export const MOUNT_NAME = /^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$/
export const LABEL = /^[A-Za-z0-9_-]*$/

export function isRemovable(d: { removable: boolean; hotplug: boolean; transport: string }): boolean {
  return d.removable || d.hotplug || d.transport === 'usb'
}
