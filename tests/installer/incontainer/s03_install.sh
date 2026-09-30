#!/usr/bin/env bash
# Senaryo 3 / 11: yerel kipte ilk kurulum (--yes --no-docker) ve tam doğrulama.
# Kullanım: s03_install.sh <kimlik öneki>
set -uo pipefail
. /opt/tests/lib.sh
P=${1:-s03}

expect_eq "$P.pre.ufw" "kurulumdan önce ufw kurulu değil (betik kuracak)" "" "$(command -v ufw || true)"

fresh_release v1.0.0
bash "$INSTALLER_DIR/scripts/install.sh" --yes --no-docker >/tmp/install1.log 2>&1
rc=$?
log=$(cat /tmp/install1.log)
if (( rc == 0 )); then pass "$P.run" "install.sh --yes --no-docker çıkış kodu 0"
else fail "$P.run" "install.sh --yes --no-docker çıkış kodu 0" "rc=$rc $(tail -n 15 /tmp/install1.log)"; fi

ip=$(primary_ip)
expect_match "$P.summary.url" "özet doğru IP ve portu yazdırıyor (http://$ip:8080)" "http://${ip//./\\.}:8080" "$log"
expect_match "$P.summary.banner" "özet başlığı yazdırıldı" 'MyServer başarıyla kuruldu' "$log"
expect_match "$P.summary.wizard" "ilk kurulumda sihirbaz notu yazdırıldı" 'kurulum sihirbazı' "$log"
expect_match "$P.steps" "16 adımın tümü çalıştı" '\[16/16\]' "$log"

for pkg in curl ca-certificates sudo smartmontools ufw util-linux tar unzip zip iproute2 coreutils passwd; do
  st=$(dpkg-query -W -f='${Status}' "$pkg" 2>/dev/null)
  expect_eq "$P.pkg.$pkg" "paket kurulu: $pkg" "install ok installed" "$st"
done

# UFW: kurulu ama etkin değil -> etkinleştirilmemeli, kural eklenmemeli.
expect_match "$P.ufw.inactive" "ufw etkinleştirilmedi" '^Status: inactive' "$(LC_ALL=C ufw status 2>&1 | head -n 1)"
expect_eq "$P.ufw.norules" "ufw etkin değilken kural eklenmedi" "" "$(LC_ALL=C ufw show added 2>/dev/null | grep '^ufw ' || true)"
expect_eq "$P.ufw.nostate" "install.state içinde UFW kaydı yok" "" "$(grep -s '^UFW_' /etc/myserver/install.state || true)"

bash /opt/tests/verify_install.sh "$P" 8080
