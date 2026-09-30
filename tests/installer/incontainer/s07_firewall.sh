#!/usr/bin/env bash
# Senaryo 7: güvenlik duvarı davranışı. Kurulu bir panel (8080) gerektirir.
set -uo pipefail
. /opt/tests/lib.sh
P=s07
I="$INSTALLER_DIR/scripts/install.sh"
STATE=/etc/myserver/install.state
fresh_release v1.0.0

added() { LC_ALL=C ufw show added 2>/dev/null | grep '^ufw ' || true; }
panel_rules() { added | grep -E '(port 8080 |8080/tcp|port 9090 |9090/tcp)' || true; }
subnet=$(ip -4 route show dev "$(ip -4 route get 1.1.1.1 | sed -n 's/.* dev \([^ ]*\).*/\1/p')" scope link | awk '{print $1; exit}')

# --- ufw etkin değil ---
bash "$I" --yes --no-docker >/tmp/fw0.log 2>&1
expect_match "$P.off.status" "ufw etkin değilken etkinleştirilmedi" '^Status: inactive' "$(LC_ALL=C ufw status | head -n 1)"
expect_eq "$P.off.rules" "ufw etkin değilken kural eklenmedi" "" "$(added)"
expect_eq "$P.off.state" "ufw etkin değilken kayıt tutulmadı" "" "$(grep -s '^UFW_' "$STATE" || true)"
bash "$I" --yes --no-docker --allow-public >/tmp/fw0p.log 2>&1
expect_eq "$P.off.public" "ufw etkin değilken --allow-public de kural eklemedi" "" "$(added)"

# --- ufw etkin (yönetici SSH'a izin verip etkinleştirmiş) ---
ufw allow 22/tcp >/dev/null
ufw limit 2222/tcp >/dev/null
en=$(ufw --force enable 2>&1)
status=$(LC_ALL=C ufw status | head -n 1)
if [[ "$status" != "Status: active" ]]; then
  info "$P.sandbox" "ufw bu kapsayıcıda etkinleştirilemedi; etkin durum sınamaları atlandı" "$en / $status"
  exit 0
fi
if iptables -S ufw-user-input >/dev/null 2>&1; then
  info "$P.sandbox" "ufw kapsayıcıda etkin ve çekirdek kuralları yüklendi (iptables zinciri ufw-user-input var)" ""
else
  info "$P.sandbox" "ufw 'active' diyor ancak çekirdek zinciri görülemedi; yalnızca ufw'nin kendi kayıtları denetlenir" ""
fi
ssh_before=$(added | grep -E '22/tcp|2222/tcp')

bash "$I" --yes --no-docker >/tmp/fw1.log 2>&1
expect_eq "$P.on.rc" "ufw etkinken kurulum başarılı" "0" "$?"
expect_eq "$P.on.rule" "tam olarak bir panel kuralı, algılanan özel alt ağla sınırlı ($subnet)" \
  "ufw allow from $subnet to any port 8080 proto tcp comment 'MyServer panel'" "$(panel_rules)"
expect_eq "$P.on.noany" "herkese açık kural yok" "" "$(added | grep -E '^ufw allow 8080' || true)"
expect_eq "$P.on.ssh" "SSH kurallarına dokunulmadı" "$ssh_before" "$(added | grep -E '22/tcp|2222/tcp')"
expect_eq "$P.on.state" "kural install.state dosyasına kaydedildi" "8080 $subnet" \
  "$(sed -n 's/^UFW_RULE_PORT=//p' "$STATE") $(sed -n 's/^UFW_RULE_FROM=//p' "$STATE")"
expect_match "$P.on.kernel" "çekirdekte kural: yalnızca alt ağdan 8080" "-s $subnet .*--dport 8080 -j ACCEPT" \
  "$(iptables -S ufw-user-input 2>&1)"
expect_match "$P.on.msg" "kullanıcıya kural bildirildi" 'UFW kuralı eklendi' "$(cat /tmp/fw1.log)"
expect_match "$P.on.status" "ufw hâlâ etkin" '^Status: active' "$(LC_ALL=C ufw status | head -n 1)"

bash "$I" --yes --no-docker >/tmp/fw2.log 2>&1
expect_eq "$P.rerun.rule" "yeniden çalıştırma kuralı yinelemedi" "1" "$(panel_rules | wc -l | tr -d ' ')"
expect_eq "$P.rerun.state" "kayıt yinelenmedi" "1 1" \
  "$(grep -c '^UFW_RULE_PORT=' "$STATE") $(grep -c '^UFW_RULE_FROM=' "$STATE")"
expect_eq "$P.rerun.numbered" "ufw status içinde 8080 için tek satır" "1" "$(LC_ALL=C ufw status | grep -c '^8080')"

# Yönetici kuralı elle silmişse yeniden çalıştırma onu geri ekler (kayda körü körüne güvenmez).
ufw delete allow from "$subnet" to any port 8080 proto tcp >/dev/null
bash "$I" --yes --no-docker >/tmp/fw3.log 2>&1
expect_eq "$P.heal" "elle silinen kural yeniden eklendi (tek kural)" "1" "$(panel_rules | wc -l | tr -d ' ')"

# Port değişince eski kural kaldırılır, yenisi eklenir.
bash "$I" --yes --no-docker --port 9090 >/tmp/fw4.log 2>&1
expect_eq "$P.port.rule" "port değişti: yalnızca 9090 kuralı var" \
  "ufw allow from $subnet to any port 9090 proto tcp comment 'MyServer panel'" "$(panel_rules)"
bash "$I" --yes --no-docker --port 8080 >/tmp/fw5.log 2>&1

# --allow-public
bash "$I" --yes --no-docker --allow-public >/tmp/fw6.log 2>&1
expect_eq "$P.public.rule" "--allow-public: tek kural, herkese açık" "ufw allow 8080/tcp comment 'MyServer panel'" "$(panel_rules)"
expect_match "$P.public.warn" "--allow-public uyarısı gösterildi" 'TÜM adreslere açıldı' "$(cat /tmp/fw6.log)"
expect_eq "$P.public.state" "kayıt 'any' olarak güncellendi" "any" "$(sed -n 's/^UFW_RULE_FROM=//p' "$STATE")"
bash "$I" --yes --no-docker >/tmp/fw7.log 2>&1
expect_eq "$P.public.back" "--allow-public olmadan yeniden çalıştırma kuralı alt ağa daralttı" \
  "ufw allow from $subnet to any port 8080 proto tcp comment 'MyServer panel'" "$(panel_rules)"
expect_eq "$P.ssh.final" "SSH kuralları bütün bu adımlardan sonra aynı" "$ssh_before" "$(added | grep -E '22/tcp|2222/tcp')"

# Yöneticinin kendi eklediği, aynı porta ait farklı bir kural.
ufw allow from 10.99.0.0/16 to any port 8080 proto tcp >/dev/null

# --- kaldırma: yalnızca betiğin eklediği kural silinir ---
bash /usr/local/share/myserver/scripts/uninstall.sh --level 1 --yes >/tmp/fw-uninst.log 2>&1
expect_eq "$P.un.rc" "kaldırma (düzey 1) başarılı" "0" "$?"
expect_eq "$P.un.rule" "betiğin eklediği kural silindi, yöneticinin kuralı duruyor" \
  "ufw allow from 10.99.0.0/16 to any port 8080 proto tcp" "$(panel_rules)"
expect_eq "$P.un.ssh" "kaldırma SSH kurallarına dokunmadı" "$ssh_before" "$(added | grep -E '22/tcp|2222/tcp')"
expect_match "$P.un.status" "kaldırma ufw durumunu değiştirmedi (etkin)" '^Status: active' "$(LC_ALL=C ufw status | head -n 1)"
expect_eq "$P.un.state" "kayıt temizlendi" "" "$(grep -s '^UFW_' "$STATE" || true)"

# Yönetici betiğin ekleyeceği kuralın aynısını ÖNCEDEN eklemişse betik sahiplenmez.
ufw delete allow from 10.99.0.0/16 to any port 8080 proto tcp >/dev/null
ufw allow from "$subnet" to any port 8080 proto tcp >/dev/null
bash "$I" --yes --no-docker >/tmp/fw8.log 2>&1
expect_eq "$P.own.rc" "yeniden kurulum başarılı" "0" "$?"
expect_eq "$P.own.state" "yöneticinin kuralı sahiplenilmedi (kayıt yok)" "" "$(grep -s '^UFW_' "$STATE" || true)"
bash /usr/local/share/myserver/scripts/uninstall.sh --level 1 --yes >/tmp/fw-uninst2.log 2>&1
expect_eq "$P.own.kept" "kaldırma yöneticinin kuralını silmedi" \
  "ufw allow from $subnet to any port 8080 proto tcp" "$(panel_rules)"

# Sonraki senaryolar için temiz durum.
ufw delete allow from "$subnet" to any port 8080 proto tcp >/dev/null
ufw --force disable >/dev/null
bash "$I" --yes --no-docker >/tmp/fw9.log 2>&1
expect_eq "$P.restore" "panel sonraki senaryolar için yeniden kuruldu" "0" "$?"
