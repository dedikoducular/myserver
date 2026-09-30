# Kurulum betiklerinin sınamaları

`scripts/install.sh`, `update.sh`, `uninstall.sh` ve `backup.sh` betiklerini **gerçekten
çalıştırarak** sınar. Betikler kullanıcı oluşturur, sudoers yazar, paket kurar, güvenlik duvarı
kurallarını ve servisleri değiştirir; bu yüzden **yalnızca tek kullanımlık Docker kapsayıcılarının
içinde** çalıştırılır.

## Güvenlik kuralları

- Betikler host üzerinde (Windows, WSL veya Linux geliştirme makinesi) **asla** çalıştırılmaz.
  `incontainer/` altındaki betikler kapsayıcı dışında başlatılırsa hemen çıkar.
- Host'un Docker soketi hiçbir sınama kapsayıcısına **bağlanmaz**; host'tan dizin de bağlanmaz
  (dosyalar `docker cp` ile kopyalanır). "Tüm uygulamaları kaldır" (düzey 3) sınaması yalnızca
  sınama kapsayıcısının kendi içindeki Docker daemon'ına, o çalışmazsa ayrı bir `docker:dind`
  kapsayıcısına karşı yapılır. Betik ayrıca daemon adının `mstest-` ile başladığını denetler.
- Oluşturulan her kapsayıcı, imaj, birim ve ağ `mstest-inst-` önekini taşır ve çıkışta silinir.
  Bu öneki taşımayan hiçbir Docker nesnesine dokunulmaz. Sınamanın kendisinin çektiği temel
  imajlar (önceden yoksa `debian:12`, `node:24-bookworm-slim`, `docker:dind`) da çıkışta silinir;
  önceden var olan imajlar silinmez.
- Sınama kapsayıcıları `--privileged` çalışır (systemd, ufw ve iç içe Docker için gerekir) ve
  çekirdeği Docker motoruyla paylaşır. Bu yüzden sihirbaz sınamada mevcut saat dilimi ve sunucu
  adıyla tamamlanır; saat/saat dilimi değiştiren yardımcı eylemleri çağrılmaz.

## Gerekenler

- Docker (Linux motoru, amd64). Windows'ta Docker Desktop + Git Bash.
- İnternet bağlantısı (apt paketleri, Docker'ın apt deposu, Go ve npm paketleri).
- Yaklaşık 3 GB boş disk alanı ve 15-25 dakika.
- Host üzerinde Go, Node veya make **gerekmez**; derleme bir kapsayıcıda yapılır.

## Çalıştırma

```bash
bash tests/installer/run.sh                 # hepsi
make test-installer                         # aynısı
bash tests/installer/run.sh --group a,b     # yalnızca seçilen gruplar
bash tests/installer/run.sh --reuse-binaries   # derlemeyi atla, yalnızca yeniden paketle
bash tests/installer/run.sh --keep          # kapsayıcıları inceleme için bırak
bash tests/installer/run.sh --cleanup       # yalnızca mstest-inst-* nesnelerini sil
```

Çıkış kodu: tüm denetimler geçtiyse 0, en az biri kaldıysa 1, ortam hatasında 2.
Ayrıntılı günlükler `tests/installer/.work/logs/` altına yazılır (depoya eklenmez).

## Ne yapar?

1. `shellcheck` ile dört betiği denetler.
2. Deponun bir **kopyasını** derleme kapsayıcısına aktarır ve orada `make release VERSION=1.0.0`,
   ardından `make release-build release-package VERSION=1.1.0` çalıştırır; çıktının düzenini
   denetler. Çalışma ağacına (ör. `backend/internal/webui/dist`) yazılmaz.
3. systemd'nin PID 1 olduğu kapsayıcıları başlatır ve senaryoları çalıştırır:

| Grup | Kapsayıcı | Senaryolar |
| --- | --- | --- |
| a | Ubuntu 24.04 | `s03` kurulum ve doğrulama, `s04` yeniden çalıştırma/yükseltme, `s10` yedek, `s08` güncelleme ve geri dönüş, `s07` güvenlik duvarı, `s09` kaldırma düzey 1-2 |
| b | Ubuntu 24.04 | `s05` hata durumları, indirme kipi (yerel https sunucusu, sınama CA'sı) |
| c | Ubuntu 24.04 | `s06` Docker kurulumu, `s09.l3` kaldırma düzey 3 |
| d | Debian 12 | `s11` kurulum, doğrulama ve yeniden çalıştırma |

4. Sonuç tablosunu yazdırır ve her şeyi siler.

## Dosyalar

| Dosya | İçerik |
| --- | --- |
| `run.sh` | Host tarafı: imajlar, derleme, kapsayıcılar, sonuç tablosu, temizlik |
| `Dockerfile.systemd` | systemd'li sınama imajı (Ubuntu 24.04 / Debian 12) |
| `Dockerfile.build` | `make release` için derleme imajı (Node + Go + make) |
| `incontainer/lib.sh` | Kapsayıcı içi ortak yardımcılar (denetimler, API, https sunucusu, bozuk sürüm üretimi) |
| `incontainer/verify_install.sh` | Kurulumun her iddiasının doğrulanması |
| `incontainer/sNN_*.sh` | Senaryolar |
| `incontainer/https_server.py` | Yerel https sunucusu (yönlendirme sınamaları dahil) |

## Kapsayıcıda sınanamayanlar

Aşağıdakiler gerçek bir sunucuda ayrıca denetlenmelidir:

- Yeniden başlatma (reboot) sonrası servisin ve UFW kuralının kalıcılığı.
- Gerçek ağ arayüzleriyle (birden çok arayüz, VLAN, yalnızca genel IP) yerel alt ağ algılaması;
  kapsayıcıda tek arayüz ve Docker'ın özel alt ağı vardır.
- arm64 ikili dosyalarının çalışması (yalnızca derlendikleri ve arşive girdikleri denetlenir).
- Gerçek yayın adresinden (genel CA'lı https, GitHub yönlendirmeleri) indirme.
- `noexec` bağlanmış `/tmp`, ayrı diske bağlanmış `/var/lib/myserver`, SELinux/AppArmor etkin host.
- Panelin web terminalinden (servis kontrol grubunun içinden) `update.sh` çalıştırma.
- Yardımcının gerçek donanım eylemleri (disk bağlama, SMART) ve saat dilimi/sunucu adı değişikliği.
