#!/usr/bin/env bash
# Senaryo 4: yeniden çalıştırma (idempotency) ve yerinde yükseltme.
set -uo pipefail
. /opt/tests/lib.sh
P=${1:-s04}
ENVF=/etc/myserver/myserver.env

# Sihirbazı API üzerinden tamamla, veri ve yönetici değişiklikleri oluştur.
expect_match "$P.setup" "kurulum sihirbazı API ile tamamlandı" '"success":true' "$(api_setup 8080)"
expect_match "$P.login.before" "yönetici oturum açabiliyor" '"authenticated":true' "$(api_login 8080)"
echo "mstest-marker" >/var/lib/myserver/MARKER
install -d -o 12345 -g 12345 /var/lib/myserver/apps/ornek
echo "uygulama verisi" >/var/lib/myserver/apps/ornek/veri.txt
chown 12345:12345 /var/lib/myserver/apps/ornek/veri.txt
sed -i 's/^MYSERVER_LOG_LEVEL=.*/MYSERVER_LOG_LEVEL=debug/' "$ENVF"
printf '# yönetici notu\nMYSERVER_COOKIE_SECURE=false\n' >>"$ENVF"
env_before=$(sha256sum <"$ENVF")
users_before=$(db_query 'SELECT COUNT(*) FROM users;')

fresh_release v1.0.0
bash "$INSTALLER_DIR/scripts/install.sh" --yes --no-docker >/tmp/install2.log 2>&1
rc=$?
if (( rc == 0 )); then pass "$P.rerun" "ikinci çalıştırma çıkış kodu 0"
else fail "$P.rerun" "ikinci çalıştırma çıkış kodu 0" "rc=$rc $(tail -n 15 /tmp/install2.log)"; fi
expect_match "$P.rerun.upgrade" "betik mevcut kurulumu algıladı" 'yerinde yükseltilecek' "$(cat /tmp/install2.log)"

expect_eq "$P.env.same" "myserver.env bayt bayt aynı (yönetici değişiklikleri korundu)" "$env_before" "$(sha256sum <"$ENVF")"
expect_eq "$P.env.loglevel" "MYSERVER_LOG_LEVEL=debug korundu" "debug" "$(sed -n 's/^MYSERVER_LOG_LEVEL=//p' "$ENVF")"
expect_eq "$P.marker" "veri dizinindeki işaret dosyası duruyor" "mstest-marker" "$(cat /var/lib/myserver/MARKER 2>&1)"
expect_eq "$P.apps.owner" "apps/ altındaki uygulama verisinin sahibi değiştirilmedi" "12345:12345" \
  "$(stat -c '%u:%g' /var/lib/myserver/apps/ornek/veri.txt 2>&1)"
expect_eq "$P.users" "kullanıcı tablosu aynı" "$users_before" "$(db_query 'SELECT COUNT(*) FROM users;')"
expect_match "$P.login.after" "yönetici hesabı yeniden kurulumdan sonra da çalışıyor" '"authenticated":true' "$(api_login 8080)"
expect_match "$P.setup.locked" "sihirbaz ikinci kez kullanılamıyor" '"success":false' "$(api_setup 8080)"
expect_ok "$P.rollback.saved" "önceki sürüm geri dönüş dizinine saklandı" test -x /usr/local/share/myserver/rollback/myserver
expect_eq "$P.groups.dup" "grup üyeliği yinelenmedi" "1" "$(getent group docker | tr ':,' '\n\n' | grep -cx myserver)"

bash /opt/tests/verify_install.sh "$P.v" 8080

# Port değişikliği: yalnızca ilgili satır değişmeli.
bash "$INSTALLER_DIR/scripts/install.sh" --yes --no-docker --port 9090 >/tmp/install3.log 2>&1
rc=$?
expect_eq "$P.port.rc" "--port 9090 ile yeniden çalıştırma başarılı" "0" "$rc"
expect_eq "$P.port.env" "MYSERVER_LISTEN tek satır ve :9090" ":9090" "$(sed -n 's/^MYSERVER_LISTEN=//p' "$ENVF")"
expect_eq "$P.port.rest" "diğer ortam satırları korundu" \
  "$(printf 'MYSERVER_COOKIE_SECURE=false\nMYSERVER_LOG_LEVEL=debug')" \
  "$(grep -E '^MYSERVER_(COOKIE_SECURE|LOG_LEVEL)=' "$ENVF" | sort)"
expect_ok "$P.port.http" "panel 9090 portunda yanıt veriyor" wait_http 9090 20
expect_match "$P.port.summary" "özet yeni portu gösteriyor" ':9090' "$(tail -n 12 /tmp/install3.log)"

# Çalışan panelin portu başka bir programın kullandığı bir porta değiştirilemez.
systemd-run --quiet --unit=mstest-blocker /usr/bin/python3 -m http.server 9191 --bind 0.0.0.0
sleep 1
out=$(bash "$INSTALLER_DIR/scripts/install.sh" --yes --no-docker --port 9191 2>&1); rc=$?
systemctl stop mstest-blocker >/dev/null 2>&1
if (( rc != 0 )) && grep -q 'portu başka bir program' <<<"$out"; then
  pass "$P.port.busy" "panel çalışırken dolu bir porta geçiş reddedildi"
else fail "$P.port.busy" "panel çalışırken dolu bir porta geçiş reddedildi" "rc=$rc $(tail -n 5 <<<"$out")"; fi
expect_eq "$P.port.busy.env" "reddedilen port ortam dosyasına yazılmadı" ":9090" "$(sed -n 's/^MYSERVER_LISTEN=//p' "$ENVF")"
expect_eq "$P.port.busy.alive" "panel çalışmaya devam ediyor" "active" "$(systemctl is-active myserver)"

bash "$INSTALLER_DIR/scripts/install.sh" --yes --no-docker --port 8080 >/tmp/install4.log 2>&1
expect_eq "$P.port.back" "port 8080'e geri alındı" ":8080" "$(sed -n 's/^MYSERVER_LISTEN=//p' "$ENVF")"
expect_ok "$P.port.back.http" "panel 8080 portunda yanıt veriyor" wait_http 8080 20
