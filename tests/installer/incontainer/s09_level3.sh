#!/usr/bin/env bash
# Senaryo 9 (düzey 3): uygulamaların kaldırılması. YALNIZCA yalıtılmış bir Docker
# daemon'ına karşı çalışır (sınama kapsayıcısının kendi içindeki daemon veya
# run.sh'nin başlattığı mstest-inst-dind). Geliştiricinin gerçek daemon'ına
# ulaşmak mümkün değildir: host soketi hiçbir sınama kapsayıcısına bağlanmaz.
set -uo pipefail
. /opt/tests/lib.sh
P=s09.l3
L=io.myserver.managed=true

# Güvenlik kilidi: daemon adının sınama önekiyle başlaması ZORUNLU.
dname=$(docker info --format '{{.Name}}' 2>/dev/null)
if [[ "$dname" != mstest-* ]]; then
  fail "$P.guard" "yalıtılmış sınama daemon'ı bulunamadı; düzey 3 sınanmadı" "daemon adı=[$dname] DOCKER_HOST=${DOCKER_HOST:-yerel}"
  exit 0
fi
if [[ -S /var/run/docker.sock && -z "${DOCKER_HOST:-}" ]] && ! pgrep -x dockerd >/dev/null; then
  fail "$P.guard" "soket var ama daemon bu kapsayıcıda çalışmıyor (host soketi bağlanmış olabilir); sınama durduruldu" ""
  exit 0
fi
info "$P.daemon" "kullanılan yalıtılmış daemon" "ad=$dname, DOCKER_HOST=${DOCKER_HOST:-yerel unix soketi}"

IMG=busybox:1.36
docker pull -q "$IMG" >/dev/null 2>&1 || { fail "$P.image" "sınama imajı çekilemedi" "$IMG"; exit 0; }

interactive() {
  local input
  input=$(printf '%s\n' "$@")
  OUT=$( { sleep 1; while IFS= read -r l; do printf '%s\n' "$l"; sleep 1; done <<<"$input"; sleep 1; } \
        | script -qec "bash /root/uninstall.sh" /dev/null 2>&1 ); RC=$?
}
reset_objects() {
  docker rm -f ms-app1 ms-app2 other-app >/dev/null 2>&1
  docker network rm ms-net other-net >/dev/null 2>&1
  docker volume rm ms-vol1 ms-vol2 other-vol >/dev/null 2>&1
  docker volume create --label "$L" ms-vol1 >/dev/null
  docker volume create --label "$L" ms-vol2 >/dev/null            # hiçbir kapsayıcıya bağlı değil
  docker volume create other-vol >/dev/null                         # etiketsiz
  docker network create --label "$L" ms-net >/dev/null
  docker network create other-net >/dev/null
  docker run -d --name ms-app1 --label "$L" --network ms-net -v ms-vol1:/data -v other-vol:/other "$IMG" sleep 3600 >/dev/null
  docker create --name ms-app2 --label "$L" "$IMG" true >/dev/null  # durmuş, etiketli
  docker run -d --name other-app --network other-net -v other-vol:/other "$IMG" sleep 3600 >/dev/null
  docker run --rm -v ms-vol1:/d -v other-vol:/o "$IMG" sh -c 'echo veri >/d/f; echo veri >/o/f'
}
names() { docker "$@" --format '{{.Name}}' 2>/dev/null | sort | tr '\n' ' ' | sed 's/ $//'; }
cnames() { docker ps -a --format '{{.Names}}' | sort | tr '\n' ' ' | sed 's/ $//'; }
ensure_installed() {
  if [[ ! -x /usr/local/bin/myserver ]]; then
    fresh_release v1.0.0
    bash "$INSTALLER_DIR/scripts/install.sh" --yes >/tmp/l3-install.log 2>&1 || fail "$P.install" "yeniden kurulum" "$(tail -n 5 /tmp/l3-install.log)"
  fi
  cp /usr/local/share/myserver/scripts/uninstall.sh /root/uninstall.sh
}
common_checks() { # <kimlik>
  expect_eq "$1.containers" "etiketli kapsayıcılar kaldırıldı, etiketsiz kapsayıcı duruyor" "other-app" "$(cnames)"
  expect_eq "$1.running" "etiketsiz kapsayıcı hâlâ çalışıyor" "true" "$(docker inspect -f '{{.State.Running}}' other-app 2>&1)"
  expect_eq "$1.networks" "etiketli ağ kaldırıldı, etiketsiz ağ duruyor" "other-net" \
    "$(docker network ls --format '{{.Name}}' | grep -E '^(ms-net|other-net)$' | tr '\n' ' ' | sed 's/ $//')"
  expect_eq "$1.othervol" "etiketsiz birim ve içeriği duruyor" "veri" "$(docker run --rm -v other-vol:/o "$IMG" cat /o/f 2>&1)"
  expect_ok "$1.docker" "Docker kaldırılmadı" docker version
  expect_fail "$1.panel" "panel kaldırıldı" test -e /usr/local/bin/myserver
  expect_fail "$1.data" "panel verileri silindi" test -e /var/lib/myserver
}

# --- A. etkileşimli: KALDIR doğru, birim onayı YANLIŞ -> birimler korunur ---
ensure_installed; reset_objects
interactive 3 KALDIR sil
expect_eq "$P.a.rc" "düzey 3 (birim onayı yanlış) çıkış kodu 0" "0" "$RC"
expect_match "$P.a.listed" "birimler onaydan önce listelendi" 'ms-vol1' "$OUT"
common_checks "$P.a"
expect_eq "$P.a.volumes" "ikinci onay verilmedi: etiketli birimler KORUNDU" "ms-vol1 ms-vol2 other-vol" "$(names volume ls)"
expect_eq "$P.a.voldata" "korunan birimin içeriği duruyor" "veri" "$(docker run --rm -v ms-vol1:/d "$IMG" cat /d/f 2>&1)"
expect_match "$P.a.msg" "birimlerin korunduğu bildirildi" 'KORUNDU' "$OUT"

# --- B. etkileşimli: KALDIR yanlış -> hiçbir şeye dokunulmaz ---
ensure_installed; reset_objects
interactive 3 HAYIR
expect_match "$P.b.msg" "ilk onay yanlış: vazgeçildi" 'Onay verilmedi' "$OUT"
expect_eq "$P.b.containers" "ilk onay yanlış: tüm kapsayıcılar duruyor" "ms-app1 ms-app2 other-app" "$(cnames)"
expect_eq "$P.b.volumes" "ilk onay yanlış: tüm birimler duruyor" "ms-vol1 ms-vol2 other-vol" "$(names volume ls)"
expect_ok "$P.b.panel" "ilk onay yanlış: panel duruyor" test -x /usr/local/bin/myserver

# --- C. etkileşimli: KALDIR + SIL -> etiketli birimler silinir ---
interactive 3 KALDIR SIL
expect_eq "$P.c.rc" "düzey 3 (KALDIR + SIL) çıkış kodu 0" "0" "$RC"
common_checks "$P.c"
expect_eq "$P.c.volumes" "ikinci onaydan sonra yalnızca etiketli birimler silindi" "other-vol" "$(names volume ls)"

# --- D. etkileşimsiz: --remove-volumes olmadan ve ile ---
ensure_installed; reset_objects
OUT=$(bash /root/uninstall.sh --level 3 --yes --confirm-data-loss 2>&1); RC=$?
expect_eq "$P.d.rc" "düzey 3 --yes (birimler korunur) çıkış kodu 0" "0" "$RC"
common_checks "$P.d"
expect_eq "$P.d.volumes" "--remove-volumes verilmedi: birimler korundu" "ms-vol1 ms-vol2 other-vol" "$(names volume ls)"
ensure_installed; reset_objects
OUT=$(bash /root/uninstall.sh --level 3 --yes --confirm-data-loss --remove-volumes 2>&1); RC=$?
expect_eq "$P.e.rc" "düzey 3 --yes --remove-volumes çıkış kodu 0" "0" "$RC"
common_checks "$P.e"
expect_eq "$P.e.volumes" "--remove-volumes: yalnızca etiketli birimler silindi" "other-vol" "$(names volume ls)"
expect_match "$P.e.unlabelled" "etiketsiz birime dokunulmadığı bildirildi" 'other-vol' "$OUT"

# --- F. Docker'a ulaşılamıyorsa: uyarı verilir, panel yine kaldırılır ---
ensure_installed
OUT=$(DOCKER_HOST=tcp://127.0.0.1:1 bash /root/uninstall.sh --level 3 --yes --confirm-data-loss 2>&1); RC=$?
expect_eq "$P.f.rc" "Docker'a ulaşılamazken düzey 3 çıkış kodu 0" "0" "$RC"
expect_match "$P.f.msg" "Docker'a ulaşılamadığı bildirildi" "Docker'a ulaşılamıyor" "$OUT"

docker rm -f other-app >/dev/null 2>&1; docker network rm other-net >/dev/null 2>&1; docker volume rm other-vol >/dev/null 2>&1
