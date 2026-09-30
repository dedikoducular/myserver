#!/usr/bin/env bash
# Senaryo 8: update.sh. Kurulu 1.0.0 paneli ve tamamlanmış sihirbaz gerektirir.
set -uo pipefail
. /opt/tests/lib.sh
P=s08
U=/usr/local/share/myserver/scripts/update.sh
STATUS=/var/lib/myserver/update.status
TARBALL=myserver-linux-amd64.tar.gz

start_https || fail "$P.https" "yerel https sunucusu" "başlatılamadı"
publish rel v1.1.0 "$REL_DIR/v1.1.0/$TARBALL"
publish badsum v1.1.0 "$REL_DIR/v1.1.0/$TARBALL" bad
publish rel v1.3.0 "$REL_DIR/v1.1.0/$TARBALL"            # içerik 1.1.0, dizin v1.3.0
make_broken_release v1.1.0 1.2.0 /root/broken-1.2.0.tar.gz
publish rel v1.2.0 /root/broken-1.2.0.tar.gz
GOOD_SHA=$(sha256sum "/srv/rel/rel/v1.1.0/$TARBALL" | awk '{print $1}')

ver() { /usr/local/bin/myserver --version 2>&1; }
st() { sed -n "s/^$1=//p" "$STATUS" 2>/dev/null; }
unchanged() { # <kimlik> <beklenen sürüm>
  expect_eq "$1.ver" "kurulu sürüm değişmedi ($2)" "$2" "$(ver)"
  expect_eq "$1.svc" "servis çalışmaya devam ediyor" "active" "$(systemctl is-active myserver)"
}
run_u() { OUT=$("$@" 2>&1); RC=$?; }
refused() { # <kimlik> <açıklama> <desen>
  if (( RC != 0 )) && grep -Eq -- "$3" <<<"$OUT"; then pass "$1" "$2"
  else fail "$1" "$2" "rc=$RC $(tail -n 5 <<<"$OUT")"; fi
}
pid_before=$(systemctl show -p MainPID --value myserver)
expect_eq "$P.pre" "başlangıç sürümü 1.0.0" "1.0.0" "$(ver)"

# --- A. bağımsız değişkenlerin katı doğrulanması ---
for bad in latest 1.2 v1 'v1.2.3;id' '$(id)' '`id`' 'v1.2.3 ' ' v1.2.3' 'v01.2.3' '1.2.3/../x' 'v1.2.3&&id' "$(printf 'v1.2.3\nid')" "v1.2.3-$(printf 'a%.0s' {1..70})"; do
  run_u bash "$U" --release-url "$HTTPS_BASE/rel" "$bad"
  refused "$P.arg.ver[$(_clean "$bad" | cut -c1-24)]" "geçersiz sürüm reddediliyor" 'Sürüm numarası geçersiz'
done
run_u bash "$U" --release-url "$HTTPS_BASE/rel" -v1.1.0
refused "$P.arg.optlike" "seçenek görünümlü sürüm reddediliyor" 'Bilinmeyen seçenek'
run_u bash "$U" --release-url "$HTTPS_BASE/rel" v1.1.0 v1.2.0
refused "$P.arg.two" "iki sürüm birden reddediliyor" 'Birden fazla sürüm'
run_u bash "$U"
refused "$P.arg.none" "sürüm verilmeden reddediliyor" 'sürüm numarası veya --from'
run_u env MYSERVER_UPDATE_VERSION='1.1.0;reboot' bash "$U" --release-url "$HTTPS_BASE/rel" v1.1.0
refused "$P.env.ver.bad" "geçersiz MYSERVER_UPDATE_VERSION reddediliyor" 'Sürüm numarası geçersiz'
run_u env MYSERVER_UPDATE_VERSION='1.2.0' bash "$U" --release-url "$HTTPS_BASE/rel" v1.1.0
refused "$P.env.ver.mismatch" "bağımsız değişkenle çelişen MYSERVER_UPDATE_VERSION reddediliyor" 'birbirini tutmuyor'

for bad in abc --help '-o/etc/passwd' "${GOOD_SHA:0:63}" "${GOOD_SHA}0" "${GOOD_SHA:0:63} " "${GOOD_SHA:0:60};id;" "${GOOD_SHA:0:63}g" '$(id)'; do
  run_u env MYSERVER_UPDATE_SHA256="$bad" MYSERVER_UPDATE_URL="$HTTPS_BASE/rel/v1.1.0/$TARBALL" bash "$U" v1.1.0
  refused "$P.env.sha[$(_clean "$bad" | cut -c1-20)]" "geçersiz MYSERVER_UPDATE_SHA256 reddediliyor" 'SHA-256 özeti geçersiz'
done
for bad in "http://localhost:8081/rel/v1.1.0/$TARBALL" "file:///srv/rel/rel/v1.1.0/$TARBALL" '--help' '-o /etc/cron.d/x' \
           'https://localhost:8443/a b' 'https://localhost:8443/$(id)' 'https://localhost:8443/`id`' \
           'https://localhost:8443/x;id' 'https://localhost:8443/x|id' "https://localhost:8443/x'y" \
           'https://localhost:8443/x?a=1&b=2' 'HTTPS://localhost:8443/x' ' https://localhost:8443/x'; do
  run_u env MYSERVER_UPDATE_SHA256="$GOOD_SHA" MYSERVER_UPDATE_URL="$bad" bash "$U" v1.1.0
  refused "$P.env.url[$(_clean "$bad" | cut -c1-34)]" "geçersiz MYSERVER_UPDATE_URL reddediliyor" 'MYSERVER_UPDATE_URL geçersiz'
done
run_u env MYSERVER_UPDATE_URL="$HTTPS_BASE/rel/v1.1.0/$TARBALL" bash "$U" v1.1.0
refused "$P.env.url.nosha" "özet verilmeden MYSERVER_UPDATE_URL reddediliyor" 'SHA-256 özeti de verilmelidir'
for bad in 'http://localhost:8081/rel' 'https://x/$(id)' '-x'; do
  run_u bash "$U" --release-url "$bad" v1.1.0
  refused "$P.arg.relurl[$bad]" "geçersiz --release-url reddediliyor" 'Yayın adresi geçersiz'
done
unchanged "$P.arg.after" 1.0.0
expect_eq "$P.arg.pid" "doğrulama hataları servisi yeniden başlatmadı" "$pid_before" "$(systemctl show -p MainPID --value myserver)"
expect_ok "$P.arg.nopwn" "kabuk özel karakterleri yorumlanmadı (yan etki dosyası yok)" test ! -e /etc/cron.d/x

# root olmayan kullanıcı
OUT=$(runuser -u nobody -- bash "$U" --release-url "$HTTPS_BASE/rel" v1.1.0 2>&1); RC=$?
refused "$P.nonroot" "root olmayan kullanıcıyla reddediliyor" 'root yetkisiyle'

# --- B. özet doğrulaması ---
run_u bash "$U" --release-url "$HTTPS_BASE/badsum" v1.1.0
refused "$P.sum.bad" "yanlış SHA256SUMS ile güncelleme yapılmıyor" 'SHA-256 doğrulaması BAŞARISIZ'
unchanged "$P.sum.bad" 1.0.0
expect_eq "$P.sum.bad.status" "durum dosyası: failed" "failed" "$(st state)"
run_u bash "$U" --release-url "$HTTPS_BASE/rel" --sha256 "$(printf '%064d' 1)" v1.1.0
refused "$P.sum.conflict" "verilen özet yayımlanan özetle çelişince güncelleme yapılmıyor" 'çelişiyor'
unchanged "$P.sum.conflict" 1.0.0
expect_eq "$P.sum.workdir" "başarısız denemelerden sonra geçici dizin kalmadı" "" "$(ls -d /var/lib/myserver-update.* 2>/dev/null)"

# --- C. sürüm denetimi ---
run_u bash "$U" --release-url "$HTTPS_BASE/rel" v1.3.0
refused "$P.vercheck" "arşivdeki sürüm istenenle eşleşmeyince güncelleme yapılmıyor" 'eşleşmiyor'
unchanged "$P.vercheck" 1.0.0

# --- D. yardımcının kullandığı çağrı biçimiyle güncelleme (ayrı systemd birimi) ---
run_helper_style() { # <sürüm> <özet> <url>; birim bitene kadar bekler
  systemctl reset-failed myserver-update.service >/dev/null 2>&1
  systemd-run --unit=myserver-update --collect --quiet --no-block \
    "--description=MyServer güncellemesi" \
    "--setenv=MYSERVER_UPDATE_VERSION=$1" "--setenv=MYSERVER_UPDATE_SHA256=$2" "--setenv=MYSERVER_UPDATE_URL=$3" \
    -- "$U" "$1" || return 1
  local i
  for (( i = 0; i < 150; i++ )); do
    sleep 1
    case "$(systemctl is-active myserver-update.service 2>/dev/null)" in active|activating|deactivating) ;; *) return 0 ;; esac
  done
  return 1
}

# düz http'ye yönlendirme reddedilmeli
run_helper_style 1.1.0 "$GOOD_SHA" "$HTTPS_BASE/redir-http/rel/v1.1.0/$TARBALL"
expect_eq "$P.redir.http.status" "düz http'ye yönlendiren adres: güncelleme başarısız" "failed" "$(st state)"
unchanged "$P.redir.http" 1.0.0
# yanlış özet (yardımcı biçimi)
run_helper_style 1.1.0 "$(printf '%064d' 2)" "$HTTPS_BASE/rel/v1.1.0/$TARBALL"
expect_eq "$P.helper.badsha.status" "yardımcı biçimi, yanlış özet: güncelleme başarısız" "failed" "$(st state)"
unchanged "$P.helper.badsha" 1.0.0

# başarılı güncelleme: https -> https yönlendirmesi (GitHub browser_download_url gibi)
users_before=$(db_query 'SELECT COUNT(*) FROM users;')
echo "guncelleme-oncesi" >/var/lib/myserver/MARKER
expect_ok "$P.helper.run" "yardımcı biçimiyle güncelleme birimi başlatıldı ve bitti" \
  run_helper_style 1.1.0 "$GOOD_SHA" "$HTTPS_BASE/redir/rel/v1.1.0/$TARBALL"
journalctl -u myserver-update --no-pager -n 30 >/tmp/update-unit.log 2>&1
expect_eq "$P.helper.state" "durum dosyası: success" "success" "$(st state)"
expect_eq "$P.helper.from" "durum dosyası: from_version=1.0.0" "1.0.0" "$(st from_version)"
expect_eq "$P.helper.to" "durum dosyası: to_version=1.1.0" "1.1.0" "$(st to_version)"
expect_eq "$P.helper.target" "durum dosyası: target=v1.1.0" "v1.1.0" "$(st target)"
expect_stat "$P.helper.status.stat" "$STATUS" myserver:myserver 640
expect_ok "$P.helper.status.read" "panel kullanıcısı durum dosyasını okuyabiliyor" runuser -u myserver -- cat "$STATUS"
expect_match "$P.helper.log.sum" "günlükte özet doğrulaması görülüyor (SHA256SUMS arşivin yanında bulundu)" 'SHA-256 özeti doğrulandı' "$(cat /tmp/update-unit.log)"
expect_eq "$P.helper.ver" "kurulu sürüm 1.1.0" "1.1.0" "$(ver)"
expect_eq "$P.helper.helpersum" "kurulu yardımcı, 1.1.0 arşivindeki yardımcıyla aynı" \
  "$(tar -xzOf "$REL_DIR/v1.1.0/$TARBALL" myserver/myserver-helper-linux-amd64 | sha256sum)" \
  "$(sha256sum </usr/local/libexec/myserver-helper)"
wait_http 8080 30
expect_eq "$P.helper.svc" "servis yeniden başlatıldı ve çalışıyor" "active" "$(systemctl is-active myserver)"
pid_after=$(systemctl show -p MainPID --value myserver)
if [[ "$pid_after" != "$pid_before" && "$pid_after" != 0 ]]; then pass "$P.helper.restart" "servis yeni süreçle çalışıyor (yeniden başlatıldı)"
else fail "$P.helper.restart" "servis yeni süreçle çalışıyor" "önce=$pid_before sonra=$pid_after"; fi
expect_match "$P.helper.api" "API yeni sürümü bildiriyor" '"version":"1.1.0"' "$(api_status 8080)"
expect_match "$P.helper.login" "yönetici hesabı güncellemeden sonra çalışıyor" '"authenticated":true' "$(api_login 8080)"
expect_eq "$P.helper.users" "kullanıcı tablosu aynı" "$users_before" "$(db_query 'SELECT COUNT(*) FROM users;')"
expect_eq "$P.helper.marker" "veri dizini korundu" "guncelleme-oncesi" "$(cat /var/lib/myserver/MARKER)"
expect_eq "$P.helper.rollbackver" "önceki sürüm saklandı (rollback/VERSION=1.0.0)" "1.0.0" "$(cat /usr/local/share/myserver/rollback/VERSION 2>&1)"
expect_eq "$P.helper.sharever" "share/VERSION güncellendi" "1.1.0" "$(cat /usr/local/share/myserver/VERSION)"
expect_stat "$P.helper.bin" /usr/local/bin/myserver root:root 755
expect_stat "$P.helper.helper" /usr/local/libexec/myserver-helper root:root 755
expect_ok "$P.helper.visudo" "sudoers değişmedi ve geçerli" visudo -c
expect_eq "$P.helper.ping" "yardımcı güncellemeden sonra çalışıyor" "pong" "$(sudo -u myserver sudo -n /usr/local/libexec/myserver-helper ping 2>&1)"
expect_eq "$P.helper.workdir" "geçici dizin kalmadı" "" "$(ls -d /var/lib/myserver-update.* 2>/dev/null)"

# --- E. geri dönüş: açılışta hemen çıkan sürüm ---
env_before=$(sha256sum </etc/myserver/myserver.env)
t0=$(date +%s)
run_u bash "$U" --release-url "$HTTPS_BASE/rel" v1.2.0
t1=$(date +%s)
if (( RC != 0 )); then pass "$P.rb.rc" "başlamayan sürümde update.sh sıfırdan farklı kodla çıktı ($((t1 - t0)) sn)"
else fail "$P.rb.rc" "başlamayan sürümde update.sh sıfırdan farklı kodla çıktı" "$(tail -n 5 <<<"$OUT")"; fi
expect_match "$P.rb.msg" "geri dönüş bildirildi" 'Geri dönülüyor' "$OUT"
expect_eq "$P.rb.state" "durum dosyası: rolled_back" "rolled_back" "$(st state)"
expect_eq "$P.rb.ver" "önceki sürüm (1.1.0) geri yüklendi" "1.1.0" "$(ver)"
expect_ok "$P.rb.elf" "geri yüklenen dosya gerçek program (sahte betik değil)" bash -c 'head -c 4 /usr/local/bin/myserver | grep -q ELF'
wait_http 8080 30
expect_eq "$P.rb.svc" "önceki sürüm çalışıyor" "active" "$(systemctl is-active myserver)"
expect_match "$P.rb.api" "API önceki sürümü bildiriyor" '"version":"1.1.0"' "$(api_status 8080)"
expect_eq "$P.rb.integrity" "veritabanı sağlam (PRAGMA integrity_check)" "ok" "$(db_query 'PRAGMA integrity_check;')"
expect_eq "$P.rb.users" "kullanıcı tablosu aynı" "$users_before" "$(db_query 'SELECT COUNT(*) FROM users;')"
expect_match "$P.rb.login" "yönetici hesabı geri dönüşten sonra çalışıyor" '"authenticated":true' "$(api_login 8080)"
expect_stat "$P.rb.db" /var/lib/myserver/myserver.db myserver:myserver 600
expect_eq "$P.rb.env" "myserver.env değişmedi" "$env_before" "$(sha256sum </etc/myserver/myserver.env)"
expect_eq "$P.rb.failed" "servis 'failed' durumunda bırakılmadı" "" "$(systemctl --failed --no-legend 2>/dev/null | grep -E 'myserver\.service' || true)"
expect_eq "$P.rb.workdir" "geçici dizin kalmadı" "" "$(ls -d /var/lib/myserver-update.* 2>/dev/null)"

# --- F. --from ---
rm -rf /root/from && mkdir -p /root/from && tar -xzf "$REL_DIR/v1.0.0/$TARBALL" -C /root/from
run_u bash "$U" --from /root/from/myserver
expect_eq "$P.from.dir" "--from <dizin> (SHA256SUMS ile) başarılı: 1.0.0'a dönüş" "0 1.0.0" "$RC $(ver)"
rm -f /root/from/myserver/SHA256SUMS
run_u bash "$U" --from /root/from/myserver
refused "$P.from.nosum" "--from <dizin>, SHA256SUMS yokken --skip-verify olmadan reddediliyor" 'SHA256SUMS yok'
cp "$REL_DIR/v1.1.0/$TARBALL" /root/from/ && rm -f /root/from/SHA256SUMS
run_u bash "$U" --from "/root/from/$TARBALL"
refused "$P.from.tar.nosum" "--from <arşiv>, özet yokken reddediliyor" 'doğrulanamıyor'
run_u bash "$U" --from "/root/from/$TARBALL" --sha256 "$GOOD_SHA"
expect_eq "$P.from.tar" "--from <arşiv> --sha256 ile başarılı: 1.1.0" "0 1.1.0" "$RC $(ver)"
run_u bash "$U" --from /root/yok
refused "$P.from.missing" "--from var olmayan yol reddediliyor" 'Kaynak bulunamadı'
wait_http 8080 30
expect_eq "$P.from.svc" "servis çalışıyor" "active" "$(systemctl is-active myserver)"

# --- G. aynı anda tek güncelleme ---
( flock -n 9 && sleep 8 ) 9>/run/myserver-update.lock &
sleep 1
run_u bash "$U" --release-url "$HTTPS_BASE/rel" v1.1.0
refused "$P.lock" "başka bir güncelleme sürerken ikinci güncelleme reddediliyor" 'zaten çalışıyor'
wait
