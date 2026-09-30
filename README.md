# MyServer

MyServer, **Ubuntu Server 24.04** için geliştirilmiş, kendi sunucunuzda çalışan bir sunucu/NAS
yönetim panelidir. Tek bir Go programı olarak, yerel bir systemd servisi biçiminde çalışır; web
arayüzü (React + TypeScript) programın içine gömülüdür ve arayüz tümüyle Türkçedir. Veriler
SQLite veritabanında tutulur.

Öncelik sırası: kararlılık > güvenlik > hafiflik > tasarım.

## İçindekiler

- [Özellikler](#özellikler)
- [Gereksinimler](#gereksinimler)
- [Kurulum](#kurulum)
- [İlk açılış: kurulum sihirbazı](#i̇lk-açılış-kurulum-sihirbazı)
- [Güncelleme](#güncelleme)
- [Panel yedeği](#panel-yedeği)
- [Kaldırma](#kaldırma)
- [Dizin düzeni](#dizin-düzeni)
- [Yapılandırma](#yapılandırma)
- [Güvenlik modeli](#güvenlik-modeli)
- [Geliştirme](#geliştirme)
- [Sorun giderme](#sorun-giderme)

## Özellikler

Panel şu bölümlerden oluşur:

| Bölüm | Adres | İçerik |
| --- | --- | --- |
| Gösterge paneli | `/` | Sistem durumu ve özet bileşenleri |
| Uygulamalar | `/apps` | Uygulama tanımlarından (manifest) Docker uygulamaları kurma |
| Docker | `/docker` | Kapsayıcı yönetimi |
| Dosyalar | `/files` | Dosya yöneticisi |
| Depolama | `/storage` | Diskler, bağlama işlemleri, SMART bilgisi |
| Ağ | `/network` | Ağ bilgileri ve güvenlik duvarı |
| Servisler | `/services` | systemd servisleri |
| Terminal | `/terminal` | Web terminali |
| Yedekleme | `/backup` | Uygulama düzeyinde yedekler |
| Güncellemeler | `/updates` | Ubuntu paketleri, Docker imajları ve MyServer güncellemeleri |
| Ayarlar | `/settings` | Panel ayarları |

Bir özellik sunucunuzda desteklenmiyorsa (örneğin sıcaklık algılayıcısı veya SMART yoksa) panel
bunu uydurma veriyle doldurmaz, "desteklenmiyor" olarak gösterir.

## Gereksinimler

- **İşletim sistemi:** Ubuntu Server 24.04 LTS (birincil hedef). Diğer Ubuntu LTS sürümleri
  (22.04 ve sonrası) ile Debian 12 ve sonrası uyarıyla kabul edilir; bunlar daha az sınanmıştır.
  Başka dağıtımlarda kurulum betiği durur (`--force` ile zorlanabilir, önerilmez).
- **Mimari:** amd64 (x86_64) veya arm64 (aarch64).
- **systemd** ve **apt**.
- Kurulum sırasında **internet bağlantısı** (eksik paketler ve Docker için).
- root yetkisi (`sudo`).

Kurulum betiği şu paketlerden yalnızca **eksik olanları** kurar; sistem genelinde yükseltme
(`apt upgrade`) yapmaz: `curl`, `ca-certificates`, `sudo`, `smartmontools`, `ufw`, `util-linux`,
`tar`, `unzip`, `zip`, `iproute2`, `coreutils`, `passwd`.

## Kurulum

Kurulum betiği tekrar çalıştırılabilir: mevcut bir kurulumu yerinde yükseltir, verileri ve
yapılandırmayı korur.

### 1. Yerel kip (depodan veya açılmış sürüm arşivinden)

Bir geliştirme makinesinde sürümü derleyin (ayrıntı için [Geliştirme](#geliştirme)):

```bash
make release VERSION=1.0.0
```

`dist/myserver-linux-<mimari>.tar.gz` arşivini sunucuya kopyalayın ve kurun:

```bash
tar -xzf myserver-linux-amd64.tar.gz
cd myserver
sudo bash scripts/install.sh
```

Betik depo içinden de çalışır; bu durumda dosyaları `dist/` dizininden alır:

```bash
sudo bash scripts/install.sh
```

### 2. İndirme kipi (tek komut)

Sürümler GitHub Releases üzerinden yayımlanır (https://github.com/dedikoducular/myserver/releases). Kurulum için sunucuda:

```bash
curl -fsSL https://raw.githubusercontent.com/dedikoducular/myserver/main/scripts/install.sh | sudo bash
```

Belirli bir sürümü kurmak için:

```bash
curl -fsSL https://raw.githubusercontent.com/dedikoducular/myserver/main/scripts/install.sh | sudo bash -s -- --version v1.0.0
```

Betik en son sürümün `myserver-linux-<mimari>.tar.gz` arşivini ve `SHA256SUMS` dosyasını indirir.

**Yeni sürüm yayımlamak (proje sahibi için):**

```bash
make release VERSION=1.1.0
gh release create v1.1.0 dist/myserver-linux-amd64.tar.gz dist/myserver-linux-arm64.tar.gz dist/SHA256SUMS \
  --title "MyServer 1.1.0" --notes "Değişiklikler"
```

Panel, Ayarlar > Güncellemeler bölümünde bu depoyu varsayılan güncelleme kaynağı olarak kullanır.

İndirme kipinde arşivin SHA-256 özeti, yayımlanan `SHA256SUMS` dosyasıyla karşılaştırılır; özet
tutmazsa hiçbir şey kurulmaz. Yalnızca `https://` adresleri kabul edilir; yönlendirmeler izlenir,
ancak `https` olmayan bir adrese yönlendirme reddedilir. Ne yerel dosya ne de
yayın adresi varsa betik açıklayıcı bir iletiyle durur.

### Seçenekler

| Seçenek | Açıklama |
| --- | --- |
| `--port <numara>` | Panel portu (1024-65535, varsayılan 8080) |
| `--release-url <url>` | Sürüm dosyalarının temel https adresi (`MYSERVER_RELEASE_URL`) |
| `--version <sürüm>` | İndirilecek sürüm: `latest` veya `v1.2.3` (`MYSERVER_VERSION`) |
| `--no-docker` | Docker kurulumunu atla |
| `--force` | Desteklenmeyen işletim sisteminde de devam et |
| `--allow-public` | UFW etkinse panel portunu **tüm adreslere** aç (önerilmez) |
| `--yes` | Soru sormadan devam et |
| `--help` | Yardım |

### Kurulum betiği ne yapar?

1. root ve systemd denetimi
2. İşletim sistemi denetimi
3. Mimari denetimi
4. İnternet denetimi
5. Eksik paketlerin kurulması
6. Docker: **zaten kuruluysa olduğu gibi kullanılır**, yeniden kurulmaz ve ayarı değiştirilmez.
   Kurulu değilse Docker Engine ve Compose eklentisi Docker'ın resmi apt deposundan kurulur
   (imza anahtarı `/etc/apt/keyrings/docker.asc`). Docker TCP soketi hiçbir koşulda açılmaz.
7. `myserver` sistem kullanıcısı ve grubu; `docker` grubuna üyelik
8. Dizinler ve `/etc/myserver/myserver.env`
9. Panel, root yardımcısı, uygulama tanımları, betikler ve sudoers kuralı
   (sudoers dosyası geçici dosyaya yazılır, `visudo -cf` ile doğrulanır, sonra yerine taşınır)
10. `myserver --version` ile doğrulama (arayüz programın içindedir)
11. systemd servisi
12. Güvenlik duvarı ([ayrıntı](#güvenlik-duvarı-davranışı))
13. Veritabanının hazırlanması (`myserver --migrate`, `myserver` kullanıcısıyla)
14. Sahiplik ve izinler; yardımcının sudo üzerinden sınanması
15. Servisin etkinleştirilip başlatılması ve panelin yanıt vermesinin beklenmesi
    (yanıt gelmezse günlük kayıtlarının sonu gösterilir)
16. Birincil yerel ağ adresinin algılanması

Kurulumun sonunda panelin adresi yazdırılır: `http://SUNUCU_IP:8080`

## İlk açılış: kurulum sihirbazı

Panel ilk kez açıldığında kurulum sihirbazı görüntülenir. Sihirbaz:

- depolama, Docker ve yetkili yardımcının kullanılabilirliğini denetler ve sonucu gösterir,
- **ilk yönetici hesabını** (kullanıcı adı ve parola) oluşturur,
- sunucu adını ve saat dilimini ayarlar.

Sihirbaz yalnızca bir kez çalışır; yönetici hesabı oluşturulduktan sonra yeniden kullanılamaz.
Sunucu adı veya saat dilimi değiştirilemezse hesap yine de oluşturulur ve neyin başarısız olduğu
bildirilir. Sihirbaz tamamlanana kadar paneli güvenmediğiniz ağlara açmayın: ilk yönetici
hesabını panele ilk ulaşan kişi oluşturur.

## Güncelleme

```bash
# Yayın adresinden belirli bir sürüme
sudo /usr/local/share/myserver/scripts/update.sh v1.2.3

# Yerel bir arşivden veya açılmış sürüm dizininden
sudo /usr/local/share/myserver/scripts/update.sh --from /yol/myserver-linux-amd64.tar.gz
sudo /usr/local/share/myserver/scripts/update.sh --from /yol/myserver
```

Betiğin yaptıkları:

1. Sürüm numarasını katı biçimde doğrular (`v1.2.3` veya `1.2.3`).
2. Arşivi indirir ve SHA-256 özetini doğrular. İndirilen sürümlerde yayımlanan `SHA256SUMS`
   her zaman denetlenir; ayrıca bir özet verilmişse (`--sha256` / `MYSERVER_UPDATE_SHA256`) ikisi
   de tutmalıdır. Herhangi bir uyumsuzlukta kurulum yapılmaz.
3. Yeni dosyaların bu makinede çalıştığını ve sürümünün istenenle eşleştiğini sınar.
4. Önceki programları `/usr/local/share/myserver/rollback/` altına saklar.
5. Servisi durdurur, veritabanının güncelleme öncesi kopyasını alır.
6. Dosyaları atomik olarak kurar (hedefin yanına yazıp yeniden adlandırır).
7. Veritabanını günceller ve servisi başlatır.
8. Panel 60 saniye içinde yanıt vermezse **otomatik olarak önceki sürüme ve güncelleme öncesi
   veritabanına geri döner**.

Sonuç `/var/lib/myserver/update.status` dosyasına yazılır (`state=running|success|failed|rolled_back`).
Güncelleme sırasında `/etc/myserver/myserver.env` ve sudoers kuralı değiştirilmez.

Yayın adresi `--release-url` seçeneğinden, `MYSERVER_RELEASE_URL` ortam değişkeninden veya
`/etc/myserver/myserver.env` dosyasındaki `MYSERVER_RELEASE_URL` satırından okunur. Bu adres
proje sahibinin sürüm deposu hazır olduğunda ayarlanmalıdır; ayarlanmadıysa `--from` kullanın.

Panelin Güncellemeler bölümü aynı betiği root yardımcısı üzerinden, ayrı bir systemd biriminde
(`myserver-update`) çalıştırır. İlerlemeyi izlemek için: `journalctl -u myserver-update -f`

Kurulum betiğini yeni bir sürüm diziniyle yeniden çalıştırmak da yerinde yükseltme yapar.

## Panel yedeği

`backup.sh`, panelin **kendi** durumunu yedekler: veritabanı ve `/etc/myserver`. Uygulama
verilerinin yedeği panelin Yedekleme bölümünden alınır; bu betik onları kapsamaz.

```bash
sudo /usr/local/share/myserver/scripts/backup.sh            # son 10 yedek saklanır
sudo /usr/local/share/myserver/scripts/backup.sh --keep 30
```

Yedekler `/var/lib/myserver/backups/panel/myserver-panel-<tarih>-<saat>.tar.gz` olarak,
`root:myserver 0640` izinleriyle yazılır.

Veritabanı WAL kipinde çalıştığı için servis çalışırken dosyayı düz kopyalamak bozuk bir kopya
üretebilir. Betik bu yüzden:

- `sqlite3` komutu kuruluysa SQLite'ın kendi yedekleme düzeneğini kullanır (servis durmaz) ve
  kopyanın bütünlüğünü denetler;
- kurulu değilse servisi kısa süreliğine durdurur, dosyaları kopyalar ve servisi yeniden başlatır
  (`--no-stop` ile bu davranış engellenebilir).

Geri yükleme adımları için: `backup.sh --help`

## Kaldırma

```bash
sudo /usr/local/share/myserver/scripts/uninstall.sh
```

Betik hangi düzeyde kaldırma yapılacağını sorar:

| Düzey | Kaldırılanlar |
| --- | --- |
| 1. Yalnızca paneli kaldır | Program, yardımcı, servis, sudoers kuralı. `/var/lib/myserver` ve `/etc/myserver` **korunur**. |
| 2. Panel + config | Ek olarak `/etc/myserver` ve `/var/lib/myserver` silinir — panel veritabanı, uygulama verileri ve **yedekler dahil**. `KALDIR` yazarak onaylamanız istenir. |
| 3. Panel + tüm uygulamalar | Ek olarak `io.myserver.managed=true` etiketli kapsayıcılar ve ağlar kaldırılır. Aynı etiketi taşıyan Docker birimleri listelenir ve yalnızca **ayrı bir onayla** (`SIL`) silinir. |

Her düzeyde kurulum betiğinin eklediği UFW kuralı, yardımcının paket güncellemesi günlükleri
(`/var/lib/myserver-updates`) ve `myserver` sistem kullanıcısı kaldırılır, systemd yeniden
yüklenir. Aynı kuralı kurulumdan önce siz eklediyseniz betik onu sahiplenmez ve silmez.

Betik **hiçbir zaman**:

- Docker'ı kaldırmaz,
- MyServer'ın oluşturmadığı (etiket taşımayan) kapsayıcılara, ağlara veya birimlere dokunmaz,
- açık onay olmadan veri silmez,
- diğer güvenlik duvarı kurallarına veya UFW'nin açık/kapalı durumuna dokunmaz.

Etkileşimsiz kullanım:

```bash
sudo uninstall.sh --level 1 --yes
sudo uninstall.sh --level 2 --yes --confirm-data-loss
sudo uninstall.sh --level 3 --yes --confirm-data-loss --remove-volumes
```

## Dizin düzeni

| Yol | İçerik | Sahip / izin |
| --- | --- | --- |
| `/usr/local/bin/myserver` | Panel (arayüz içinde gömülü) | root:root 0755 |
| `/usr/local/libexec/myserver-helper` | Root yardımcısı (setuid **değil**, yalnızca sudo ile) | root:root 0755 |
| `/etc/sudoers.d/myserver` | Tek sudoers kuralı | root:root 0440 |
| `/etc/systemd/system/myserver.service` | systemd birimi | root:root 0644 |
| `/etc/myserver/myserver.env` | Servisin ortam dosyası | root:myserver 0640 |
| `/etc/myserver/install.state` | Kurulum betiğinin kayıtları (eklenen UFW kuralı vb.) | root:root 0600 |
| `/usr/local/share/myserver/apps/manifests` | Uygulama tanımları | root:root |
| `/usr/local/share/myserver/apps/icons` | Uygulama simgeleri | root:root |
| `/usr/local/share/myserver/scripts/` | `install.sh`, `update.sh`, `uninstall.sh`, `backup.sh` | root:root 0755 |
| `/usr/local/share/myserver/rollback/` | Önceki sürümün dosyaları | root:root 0700 |
| `/var/lib/myserver` | Veri dizini | myserver:myserver 0750 |
| `/var/lib/myserver/myserver.db` | SQLite veritabanı (WAL kipi) | myserver:myserver 0600 |
| `/var/lib/myserver/backups/` | Yedekler (`panel/` altı `backup.sh` çıktısıdır) | myserver:myserver 0750 |
| `/var/lib/myserver/apps/` | Uygulama verileri | myserver:myserver 0750 |
| `/var/lib/myserver/tmp/` | Geçici yükleme dosyaları | myserver:myserver 0750 |

## Yapılandırma

Servis ayarları `/etc/myserver/myserver.env` dosyasından okunur. Değişiklikten sonra
`sudo systemctl restart myserver` çalıştırın.

| Değişken | Varsayılan | Anlamı |
| --- | --- | --- |
| `MYSERVER_LISTEN` | `:8080` | Dinlenen adres ve port |
| `MYSERVER_DATA_DIR` | `/var/lib/myserver` | Veri dizini |
| `MYSERVER_MANIFEST_DIR` | `/usr/local/share/myserver/apps/manifests` | Uygulama tanımları |
| `MYSERVER_HELPER` | `/usr/local/libexec/myserver-helper` | Root yardımcısının yolu |
| `MYSERVER_DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker soketi |
| `MYSERVER_LOG_LEVEL` | `info` | Günlük düzeyi |
| `MYSERVER_COOKIE_SECURE` | (boş) | TLS ters vekil arkasında `true` yapın |
| `MYSERVER_TERMINAL_DEFAULT_USER` | kurulumu yapan kullanıcı | Web terminalinin varsayılan kullanıcısı |
| `MYSERVER_RELEASE_URL` | (boş) | Güncelleme betiğinin kullandığı yayın adresi |

Panel düz HTTP ile yayın yapar. İnternet üzerinden erişecekseniz paneli TLS sonlandıran bir ters
vekil sunucusunun veya bir VPN'in arkasına alın.

## Güvenlik modeli

### Yetkisiz servis kullanıcısı

Panel root olarak **çalışmaz**. `myserver` adlı, oturum açamayan (`/usr/sbin/nologin`) bir sistem
kullanıcısıyla çalışır.

### Yetenekler (capabilities)

Dosya yöneticisinin kullanıcı dizinlerinde çalışabilmesi için panel sürecine yalnızca şu dört
yetenek verilir: `CAP_DAC_OVERRIDE`, `CAP_DAC_READ_SEARCH`, `CAP_CHOWN`, `CAP_FOWNER`.

Bunun anlamını açıkça belirtmek gerekir: bu yetenekler panelin dosya izinlerini aşarak sistemdeki
**her dosyayı okuyup yazabilmesi** demektir. Panel root değildir, ancak dosya sistemi açısından
root'a yakın bir yetkiye sahiptir.

### Root yardımcısı

Disk bağlama, güvenlik duvarı, paket güncelleme gibi root gerektiren işlemler ayrı bir program
olan `myserver-helper` ile yapılır:

- Panel onu yalnızca `sudo -n /usr/local/libexec/myserver-helper <eylem> <değerler>` biçiminde
  çağırır; hiçbir şey kabuk (shell) tarafından yorumlanmaz.
- sudoers dosyasında **tek** kural vardır: `myserver` kullanıcısı yalnızca bu programı
  parolasız çalıştırabilir.
- Yardımcı, **sabit bir eylem listesi** dışında hiçbir şey yapmaz ve her değeri kendisi yeniden
  doğrular. Eylem listesini görmek için: `sudo /usr/local/libexec/myserver-helper list-actions`

### systemd birimi hakkında dürüst bir not

Birim dosyasında `NoNewPrivileges`, `ProtectSystem`, `ProtectHome`, `PrivateDevices` ve seccomp
süzgeçleri gibi yaygın sıkılaştırma yönergeleri **bilerek kullanılmaz**, çünkü:

- `NoNewPrivileges` ve `RestrictSUIDSGID` sudo'yu (setuid) çalışmaz hale getirir;
- seccomp tabanlı yönergeler root olmayan serviste `NoNewPrivileges` ayarını zorunlu kılar;
- bağlama ad alanı oluşturan yönergeler yardımcının yaptığı `mount` işlemlerinin sisteme
  yansımasını engeller ve dosya yöneticisinin `/home`, `/mnt`, `/media` erişimini keser;
- `CapabilityBoundingSet` alt süreçlere miras kaldığı için dört yetenekle sınırlansaydı, sudo ile
  başlatılan yardımcı da `mount` veya `ufw` için gereken yeteneklerden yoksun kalırdı. Bu yüzden
  sınır kümesi tam bırakılmıştır; panelin **sahip olduğu** yetenekler yine yalnızca yukarıdaki
  dört yetenektir.

Her kararın gerekçesi `packaging/myserver.service` dosyasındaki açıklamalarda yazılıdır.

### docker grubu uyarısı

`myserver` kullanıcısı `docker` grubunun üyesidir. **Docker soketine erişim, host üzerinde root
yetkisine eşdeğerdir** (örneğin kök dosya sistemini bağlayan bir kapsayıcı başlatılabilir). Yani
panelde yönetici olarak oturum açabilen biri veya panel sürecini ele geçiren bir saldırgan,
sunucuda fiilen root yetkisi elde edebilir. Bu, Docker yöneten her panelin doğasında vardır.
Buna göre davranın:

- güçlü bir yönetici parolası kullanın,
- paneli doğrudan internete açmayın,
- panele erişebilen ağı sınırlayın.

### Varsayılan olarak kapalı servisler

Kurulum betiği SMB veya NFS paylaşımı açmaz, SSH yapılandırmasına dokunmaz ve Docker TCP
soketini etkinleştirmez.

### Güvenlik duvarı davranışı

Kurulum betiği güvenlik duvarının durumunu sizden habersiz değiştirmez:

- **UFW etkinse:** yalnızca algılanan özel yerel ağ alt ağından (örneğin `192.168.1.0/24`) panel
  portuna izin veren tek bir kural eklenir ve bu size bildirilir. Özel bir alt ağ algılanamazsa
  (örneğin sunucunun yalnızca genel IP adresi varsa) **kural eklenmez** ve portu nasıl
  açabileceğiniz gösterilir.
- **UFW kurulu ama etkin değilse:** etkinleştirilmez ve kural eklenmez.
- Panel portu tüm adreslere yalnızca `--allow-public` açıkça verilirse açılır.
- Eklenen kural `/etc/myserver/install.state` dosyasına kaydedilir ve kaldırma sırasında silinir.

UFW'yi elle etkinleştirecekseniz SSH erişiminizi kaybetmemek için **önce** SSH'a izin verin:

```bash
sudo ufw allow OpenSSH
sudo ufw enable
```

## Geliştirme

### Gerekenler

- Go (sürüm için `backend/go.mod` dosyasına bakın)
- Node.js ve npm
- GNU make; Windows'ta Git Bash (`sh`, `tar`, `sha256sum` içerir)

### Derleme

| Hedef | Yaptığı |
| --- | --- |
| `make frontend` | `npm ci && npm run build`; çıktı `backend/internal/webui/dist` içine kopyalanır |
| `make backend` | `build/myserver` ve `build/myserver-helper` (CGO kapalı, durağan; varsayılan linux/amd64) |
| `make build` | Önce arayüz, sonra arka uç |
| `make release` | `dist/` altında linux/amd64 ve linux/arm64 dosyaları, mimari başına arşiv ve `SHA256SUMS` |
| `make release-build` | Yalnızca `dist/` altındaki ikili dosyalar (arayüz önceden derlenmiş olmalı) |
| `make release-package` | `dist/` içindeki ikili dosyaları yeniden paketler (betikler, arşivler, `SHA256SUMS`) |
| `make test` | Arka uç ve arayüz sınamaları |
| `make test-installer` | Kurulum betiklerini tek kullanımlık Docker kapsayıcılarında sınar (`tests/installer/README.md`) |
| `make vet` | `go vet` (GOOS=linux) |
| `make fmt` | `go fmt` |
| `make clean` | Derleme çıktılarını siler |
| `make dev-backend` | Arka ucu `.devdata/` veri diziniyle çalıştırır (yalnızca Linux) |
| `make dev-frontend` | Vite geliştirme sunucusu |

Sürüm numarası `VERSION` değişkeniyle verilir (`make release VERSION=1.2.3`); verilmezse en yakın
`v1.2.3` biçimli git etiketi, o da yoksa `1.0.0-dev` kullanılır. Güncelleme betiği, programın
bildirdiği sürümün istenen sürümle aynı olmasını bekler; yayımlanacak sürümleri `VERSION` değeri
sürüm diziniyle (`v1.2.3`) eşleşecek biçimde derleyin.

### Windows'ta çapraz derleme

Ürün yalnızca Linux'ta çalışır, ancak Windows'ta derlenebilir. Go çapraz derlemeyi yerleşik
olarak destekler ve CGO kapalı olduğu için ek bir derleyici gerekmez. Git Bash içinde:

```bash
make release VERSION=1.0.0
# Go PATH üzerinde değilse:
make release VERSION=1.0.0 GO="$LOCALAPPDATA/myserver-toolchain/go/bin/go"
```

make kullanmadan elle:

```bash
cd frontend && npm ci && npm run build && cd ..
cd backend
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X myserver/internal/config.Version=1.0.0" \
  -o ../dist/myserver-linux-amd64 ./cmd/myserver
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X myserver/internal/config.Version=1.0.0" \
  -o ../dist/myserver-helper-linux-amd64 ./cmd/myserver-helper
```

Windows'ta oluşturulan arşivlerde dosyaların çalıştırma izni bulunmayabilir. Kurulum betiği
dosyaları doğru izinlerle kendisi kurduğu için bu sorun yaratmaz; betiği `sudo bash
scripts/install.sh` biçiminde çalıştırmanız yeterlidir.

### Arayüzü bir arka uca karşı çalıştırma

Arka uç Windows'ta çalışmaz. Bir Linux makinesinde (veya WSL'de) arka ucu başlatın, sonra arayüz
geliştirme sunucusunu o adrese yönlendirin:

```bash
# Linux makinesinde
make dev-backend                      # 127.0.0.1:8080

# Geliştirme makinesinde
cd frontend
MYSERVER_BACKEND=http://192.168.1.50:8080 npm run dev     # http://localhost:5173
```

`/api` istekleri (WebSocket dahil) `MYSERVER_BACKEND` adresine yönlendirilir; varsayılan
`http://127.0.0.1:8080` adresidir. Arka ucun başka bir makineden erişilebilir olması için
`MYSERVER_LISTEN` değerini buna göre ayarlayın.

Modül yazım kuralları: `docs/MODULE_CONTRACT.md`

## Sorun giderme

**Panel açılmıyor**

```bash
systemctl status myserver
journalctl -u myserver -n 100 --no-pager
ss -ltn | grep 8080
```

**Servis sürekli yeniden başlıyor ve sonunda duruyor**
systemd, 5 dakika içinde 10 başarısız başlatmadan sonra denemeyi bırakır. Günlükteki hatayı
giderdikten sonra:

```bash
sudo systemctl reset-failed myserver
sudo systemctl start myserver
```

**Port kullanımda**
Kurulum betiği port doluysa hiçbir şey kurmadan durur (çalışan panelin portunu dolu bir porta
değiştirmeye çalıştığınızda da). Başka bir portla kurun: `sudo bash scripts/install.sh --port 9090`

**Başka bir bilgisayardan panele ulaşılamıyor**
UFW etkinse panel portuna izin verilmiş olmalıdır: `sudo ufw status`. Yalnızca kendi ağınıza
açmak için: `sudo ufw allow from 192.168.1.0/24 to any port 8080 proto tcp`

**"Yetkili yardımcı servis kullanılamıyor"**

```bash
sudo -u myserver sudo -n /usr/local/libexec/myserver-helper ping
sudo visudo -cf /etc/sudoers.d/myserver
grep includedir /etc/sudoers
```

`/etc/sudoers` dosyasında `@includedir /etc/sudoers.d` satırı bulunmalıdır.

**Docker bölümü çalışmıyor**

```bash
systemctl status docker
id myserver            # "docker" grubu listede olmalı
sudo systemctl restart myserver
```

**Güncelleme başarısız oldu**

```bash
cat /var/lib/myserver/update.status
journalctl -u myserver-update -n 100 --no-pager
```

Betik başarısızlıkta önceki sürüme kendiliğinden döner. Önceki dosyalar
`/usr/local/share/myserver/rollback/` altındadır.

**Kurulum yarıda kaldı**
Betik hangi adımda ve hangi satırda durduğunu bildirir. Sorunu giderip betiği yeniden
çalıştırın; tekrar çalıştırmak güvenlidir.
