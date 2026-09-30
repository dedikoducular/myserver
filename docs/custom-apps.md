# Özel uygulamalar (docker-compose dosyasından)

Mağazada olmayan bir uygulamayı, projenin yayımladığı `docker-compose.yml` dosyasıyla
ekleyebilirsiniz: **Uygulamalar → Mağaza → Özel Uygulama Ekle**. Bu işlemi yalnızca
yöneticiler yapabilir; ekleme ve silme işlemleri denetim kaydına (`apps.custom_create`,
`apps.custom_delete`) yazılır.

## Nasıl çalışır?

1. Uygulamaya bir ad verin, compose dosyasını yapıştırın veya `.yml` / `.yaml` dosyası olarak
   yükleyin (en fazla 64 KB).
2. **Önizle**: Panel dosyayı kendi uygulama tanımı (manifest) biçimine dönüştürür ve mağazadaki
   uygulamalarla aynı sıkı doğrulamadan geçirir. Önizlemede servisler, görüntüler, portlar,
   birimler, klasörler, kurulumda sorulacak ayarlar, güvenlik uyarıları ve dönüştürme notları
   gösterilir. Kabul edilmeyen bir dosyada **bütün** sorunlar tek seferde listelenir.
3. **Kaydet**: Önizlemede gördüğünüz tanımın kendisi (bir parmak iziyle doğrulanarak) panelin
   veritabanına kaydedilir. Compose dosyasının kendisi saklanmaz.
4. Uygulama mağazada **Özel** kategorisinde ve **Özel** etiketiyle görünür. Kurulum, portlar,
   ayarlar, klasör seçimi, güvenlik uyarılarının kabulü, erişim adresi, güncelleme, loglar,
   yedekleme ve geri yükleme mağaza uygulamalarıyla tamamen aynı şekilde çalışır.

Özel uygulamanın kısa adı `custom-<ad>` biçimindedir (örneğin "Uptime Kuma" →
`custom-uptime-kuma`). Mağazadaki bir uygulamayla veya kurulu bir uygulamayla aynı kısa ad
kullanılamaz. Tanım, panel güncellemelerinden ve yeniden başlatmalardan etkilenmez.
Kurulu bir özel uygulamanın tanımı silinemez; önce uygulamayı kaldırın, sonra kartın "…"
menüsünden **Tanımı Sil**'i seçin.

## Desteklenen compose özellikleri

| Anahtar | Karşılığı |
|---|---|
| `name` (en üstte) | Ad alanını boş bırakırsanız uygulama adı olur. |
| `services.<ad>` | Servis. Ad küçük harf, rakam ve `-` içermelidir; diğer servisler ona bu adla ulaşır. |
| `image` | Zorunlu. Görüntü adı mağaza uygulamalarındaki doğrulayıcıyla denetlenir. |
| `ports` | Kısa (`8080:80`, `53:53/udp`, `127.0.0.1:8080:80`, `80`) ve uzun (`target`, `published`, `protocol`, `host_ip`, `name`) biçim. Sunucu portunu kurulumda değiştirebilirsiniz. İlk TCP portu web arayüzü ("Aç") kabul edilir. |
| `volumes` | Adlandırılmış birimler (en üstteki `volumes` altında tanımlı olmalı), mutlak yollu klasörler, adsız birimler, `type: tmpfs`. `:ro` / `read_only` desteklenir. |
| `environment` | Liste (`A=b`, `A`) ve eşleme biçimi. Her değer kurulum formunda düzenlenebilir bir alan olur. |
| `${DEĞİŞKEN}`, `${DEĞİŞKEN:-varsayılan}`, `${DEĞİŞKEN:?ileti}` | Yalnızca bir ortam değişkeninin değerinin **tamamı** olarak. Kurulumda doldurulan bir alan olur; aynı değişken birden fazla serviste tek alan olarak sorulur (örneğin ortak veritabanı parolası). Adında PASSWORD/PASS/SECRET geçen ve varsayılanı olmayan değişkenler gizli alan olur ve boş bırakılırsa rastgele üretilir; TOKEN/API_KEY gibi olanlar gizli alan olur. `$$` düz `$` işaretidir. |
| `command` | Metin (kabuk gibi sözcüklere ayrılır, genişletme yapılmaz) veya liste. |
| `user`, `restart` (`no`, `always`, `unless-stopped`, `on-failure`) | Aynen. |
| `depends_on` | Liste veya eşleme; `condition: service_started` ve `service_healthy`. Panel, bağımlı olunan servisin sağlık denetimi varsa her iki durumda da sağlıklı olmasını bekler. |
| `healthcheck` | `test` (metin → `CMD-SHELL`, veya `["CMD", ...]` / `["CMD-SHELL", "..."]`), `interval`, `timeout`, `start_period` (1 sn – 1 saat), `retries`. |
| `cap_add`, `devices`, `privileged`, `network_mode: host / bridge / none` | Mağaza uygulamalarıyla aynı güvenlik uyarılarını üretir; ayrıcalıklı kip ve Docker soketi kurulumdan önce ayrıca kabul edilmelidir. |
| `shm_size`, `tmpfs` (`/yol` veya `/yol:size=64m`) | Aynen. |
| `networks` | Yalnızca ayarsız ağ adları (`networks: [arka]`); tüm servisler uygulamanın tek özel ağına bağlanır. |
| YAML çapaları (`&`, `*`) ve birleştirme anahtarı (`<<`) | Desteklenir; `x-` ile başlayan uzantı alanları yok sayılır. |
| `container_name`, `expose`, `version`, port `mode` | Yok sayılır ve bu durum önizlemede not olarak belirtilir. |

### Klasörler ve birimler

- **Mutlak yol** (`/data/uygulama:/config`): Kurulum formunda bir klasör alanı olur; varsayılanı
  compose dosyasındaki yoldur. Yol, Dosyalar bölümünün izin verilen klasörlerinin
  (`files.allowed_roots`) içinde olmalıdır; kurulumda başka bir klasör seçebilirsiniz.
- **Sistem klasörleri her zaman reddedilir**: `/`, `/etc`, `/proc`, `/sys`, `/dev`, `/boot`,
  `/root`, `/run`, `/var/run`, `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/var/lib/docker`,
  `/var/lib/myserver`, `/var/lib/myserver-updates`, panelin veri klasörü ve bunların altındaki
  her şey. `/etc/localtime` yerine `TZ=Europe/Istanbul` ortam değişkenini kullanın.
- **Docker soketi** (`/var/run/docker.sock` veya `/run/docker.sock`): İzin verilen tek sistem
  yoludur. Bu erişim sunucunun tamamının kontrolünü verebileceği için tehlike uyarısı gösterilir
  ve kurulumdan önce ayrıca kabul edilmesi gerekir.
- **Göreli yol** (`./veri:/data`): Bilgisayarınızdaki klasör sunucuda olmadığı için panelin
  yönettiği bir Docker birimine (`myserver-custom-<ad>-yerel-veri`) dönüştürülür. Birim **boş
  başlar**; dosyalar kopyalanmaz. Aynı göreli klasör birden fazla serviste aynı birim olur.
  Tek bir dosyaya benzeyen göreli yollar (`./nginx.conf`) reddedilir: dosyayı izin verilen bir
  klasöre koyup mutlak yolla bağlayın.
- **Adsız birim** (`- /cache`): Kalıcı, adlandırılmış bir birime dönüştürülür.

### Erişim adresi

Compose dosyası bir portu `127.0.0.1` adresinde yayınlıyorsa, kurulum formunda "Yalnızca bu
sunucudan erişilsin" seçeneği açık gelir. Bu seçim uygulamanın **tüm** portları için geçerlidir.

## Reddedilen özellikler

Aşağıdakiler sessizce atlanmaz; dosya reddedilir ve nedeni gösterilir:

| Anahtar | Neden |
|---|---|
| `build` | Panel görüntü derlemez; hazır bir görüntü (`image`) gerekir. |
| `env_file` | Dosya okunamaz; değişkenleri `environment` altına yazın. |
| `entrypoint` | Görüntünün kendi giriş noktası kullanılır; gerekirse `command` kullanın. |
| `configs`, `secrets`, `include`, `extends`, `profiles` | Tanım tek dosyada ve düz olmalıdır. |
| `deploy`, kaynak sınırları (`mem_limit`, `cpus` …), `ulimits`, `logging` | Desteklenmez. |
| `pid`, `ipc`, `uts`, `userns_mode`, `security_opt`, `sysctls`, `cap_drop`, `cgroup_parent`, `runtime` | Konteyner yalıtımını değiştirir. |
| `volumes_from`, `links`, `external_links`, `extra_hosts`, `labels`, `hostname`, `working_dir`, `dns` | Desteklenmez; `links` zaten gerekmez. |
| Ayarlı ağlar (sürücü, `external`, sabit IP, takma ad) | Her uygulamanın tek bir özel ağı vardır. |
| `external: true`, `driver_opts`, `name` olan birimler | Panel yalnızca kendi oluşturduğu birimleri bağlar; `driver_opts` herhangi bir klasörü bağlamak için kullanılabilir. |
| Port aralıkları, IPv6 veya belirli bir IP'de yayın | Portları tek tek yazın; yalnızca 0.0.0.0 ve 127.0.0.1. |
| `restart: on-failure:3`, `network_mode: service:…`, `cap_add: [ALL]`, `healthcheck: NONE`/`disable`, `condition: service_completed_successfully` | Panelin modelinde karşılığı yok. |
| Başka metinle birleşen değişkenler (`http://${HOST}:8080`), `${A:+b}`, görüntü/port/birimlerde değişken | Tanım bunu ifade edemez. |
| Özel YAML etiketleri (`!secret`), birden fazla YAML belgesi | Desteklenmez. |

Listede olmayan bilinmeyen her anahtar da "Bu anahtar desteklenmiyor" diye reddedilir.

## Sınırlar

Dosya en fazla 64 KB, 12 servis, 64 port, 64 birim ve 256 ortam değişkeni içerebilir. Takma ad
(alias) açılımı ve iç içe geçme sınırlıdır; "billion laughs" türü YAML bombaları reddedilir.

## Örnek

```yaml
name: uptime-kuma
services:
  uptime-kuma:
    image: louislam/uptime-kuma:1
    restart: unless-stopped
    ports:
      - "3001:3001"
    volumes:
      - veri:/app/data
    environment:
      TZ: ${TZ:-Europe/Istanbul}
volumes:
  veri:
```

Bu dosya `custom-uptime-kuma` adlı, 3001 portunda web arayüzü olan, verisini
`myserver-custom-uptime-kuma-veri` Docker biriminde tutan ve kurulumda saat dilimini soran bir
uygulamaya dönüşür.

## Yedekten geri yükleme

Özel bir uygulamanın yedeği, o uygulamanın kayıtlı tanımına göre doğrulanır: görüntü deposu,
servisler, ortam değişkeni adları ve birimler tanımla aynı olmalıdır; ayrıcalıklar tanımda
olmayan hiçbir şey eklenemez. Tanımı silinmiş (veya başka bir panelde hiç eklenmemiş) bir özel
uygulamanın yedeğini geri yüklemek için önce aynı compose dosyasını aynı adla yeniden ekleyin.
