#!/usr/bin/env bash
# MyServer kurulum betiklerinin kapsayıcı içi sınamaları.
#
# GÜVENLİK: install.sh / uninstall.sh / update.sh / backup.sh bu betik tarafından
# YALNIZCA burada oluşturulan tek kullanımlık Docker kapsayıcılarının içinde
# çalıştırılır; host üzerinde asla. Host'un Docker soketi hiçbir kapsayıcıya
# bağlanmaz, host'tan hiçbir dizin bağlanmaz (dosyalar "docker cp" ile kopyalanır).
# Oluşturulan her kapsayıcı, imaj, birim ve ağ "mstest-inst-" önekini taşır ve
# çıkışta silinir. Başka hiçbir Docker nesnesine dokunulmaz.
#
# Kullanım: tests/installer/run.sh [seçenekler]     (ayrıntı: --help veya README.md)

set -uo pipefail
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'

PFX="mstest-inst-"
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd -- "$HERE/../.." && pwd)
WORK="$HERE/.work"
# Git Bash: docker.exe Windows biçimli yol ister.
hostpath() { if (cd "$1" && pwd -W) >/dev/null 2>&1; then (cd "$1" && pwd -W); else (cd "$1" && pwd); fi; }

GROUPS_ALL="a b c d"
GROUPS_SEL=""
REUSE_BIN=0
KEEP=0
SERIAL=0
V1=1.0.0
V2=1.1.0

usage() {
  cat <<'EOF'
Kullanım: tests/installer/run.sh [seçenekler]

  --group <a|b|c|d>[,..]  Yalnızca seçilen grupları çalıştır (varsayılan: hepsi)
                            a  Ubuntu 24.04: kurulum, yeniden çalıştırma, yedek,
                               güncelleme, güvenlik duvarı, kaldırma (düzey 1-2)
                            b  Ubuntu 24.04: hata durumları ve indirme kipi
                            c  Ubuntu 24.04: Docker kurulumu ve kaldırma düzey 3
                            d  Debian 12: kurulum ve yeniden çalıştırma
  --reuse-binaries        Önceki çalıştırmada derlenen ikili dosyaları kullan;
                          yalnızca paketleme (make release-package) yinelenir
  --serial                Grupları sırayla çalıştır (varsayılan: paralel)
  --keep                  Çıkışta kapsayıcıları ve imajları SİLME (hata ayıklama).
                          Sonradan silmek için: tests/installer/run.sh --cleanup
  --cleanup               Yalnızca "mstest-inst-" nesnelerini sil ve çık
  --help                  Bu yardım
EOF
}

CLEANUP_ONLY=0
while (( $# )); do
  case "$1" in
    --group)          GROUPS_SEL=${2//,/ }; shift 2 ;;
    --reuse-binaries) REUSE_BIN=1; shift ;;
    --serial)         SERIAL=1; shift ;;
    --keep)           KEEP=1; shift ;;
    --cleanup)        CLEANUP_ONLY=1; shift ;;
    --help|-h)        usage; exit 0 ;;
    *) usage >&2; echo "Bilinmeyen seçenek: $1" >&2; exit 2 ;;
  esac
done
[[ -n "$GROUPS_SEL" ]] || GROUPS_SEL=$GROUPS_ALL

say() { printf '\n==> %s\n' "$*"; }

# --- temizlik: yalnızca önekimizi taşıyan nesneler -------------------------
PULLED_FILE="$WORK/pulled-images.txt"   # bu çalıştırmanın çektiği temel imajlar
cleanup_objects() {
  local ids
  ids=$(docker ps -aq --filter "name=^/${PFX}" 2>/dev/null)
  # shellcheck disable=SC2086  # kimlik listesi bilerek sözcüklere bölünür
  [[ -z "$ids" ]] || docker rm -f -v $ids >/dev/null 2>&1
  ids=$(docker volume ls -q --filter "name=^${PFX}" 2>/dev/null)
  # shellcheck disable=SC2086
  [[ -z "$ids" ]] || docker volume rm -f $ids >/dev/null 2>&1
  ids=$(docker network ls -q --filter "name=^${PFX}" 2>/dev/null)
  # shellcheck disable=SC2086
  [[ -z "$ids" ]] || docker network rm $ids >/dev/null 2>&1
}
cleanup_images() {
  local img
  for img in $(docker images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null | grep "^${PFX}" || true); do
    docker rmi -f "$img" >/dev/null 2>&1
  done
  # Bu çalıştırmanın çektiği (önceden bulunmayan) temel imajlar.
  if [[ -f "$PULLED_FILE" ]]; then
    while IFS= read -r img; do
      [[ -n "$img" ]] && docker rmi "$img" >/dev/null 2>&1
    done <"$PULLED_FILE"
    rm -f "$PULLED_FILE"
  fi
}
leftovers() {
  docker ps -a --filter "name=^/${PFX}" --format 'kapsayıcı {{.Names}}'
  docker volume ls --filter "name=^${PFX}" --format 'birim {{.Name}}'
  docker network ls --filter "name=^${PFX}" --format 'ağ {{.Name}}'
  docker images --format 'imaj {{.Repository}}:{{.Tag}}' | grep " ${PFX}" || true
}
on_exit() {
  local code=$?
  trap - EXIT INT TERM
  if (( KEEP )); then
    say "--keep verildi: kapsayıcılar ve imajlar bırakıldı (silmek için: run.sh --cleanup)"
  else
    say "Temizlik"
    cleanup_objects
    cleanup_images
    local left; left=$(leftovers)
    if [[ -z "$left" ]]; then echo "Tüm ${PFX}* kapsayıcı, imaj, birim ve ağları silindi."
    else echo "UYARI: silinemeyen nesneler:"; echo "$left"; fi
  fi
  exit "$code"
}

command -v docker >/dev/null 2>&1 || { echo "HATA: docker bulunamadı." >&2; exit 2; }
[[ "$(docker version --format '{{.Server.Os}}' 2>/dev/null)" == linux ]] \
  || { echo "HATA: Docker çalışmıyor veya Linux motoru kullanılmıyor." >&2; exit 2; }

if (( CLEANUP_ONLY )); then
  cleanup_objects; cleanup_images
  left=$(leftovers); [[ -z "$left" ]] && echo "Temiz." || { echo "$left"; exit 1; }
  exit 0
fi

trap on_exit EXIT
trap 'exit 130' INT TERM
mkdir -p "$WORK/logs" "$WORK/rel" "$WORK/bin"
rm -f "$WORK"/logs/*.log "$WORK/results.txt"
cleanup_objects   # önceki yarım kalmış çalıştırmadan kalanlar

ensure_base() { # temel imaj yoksa çek ve kaydet (çıkışta silinir)
  if ! docker image inspect "$1" >/dev/null 2>&1; then
    say "Temel imaj çekiliyor: $1"
    docker pull -q "$1" >/dev/null || { echo "HATA: $1 çekilemedi" >&2; exit 2; }
    echo "$1" >>"$PULLED_FILE"
  fi
}

# --- imajlar ---------------------------------------------------------------
say "Sınama imajları oluşturuluyor"
ensure_base ubuntu:24.04
docker build -q -t "${PFX}ubuntu" --build-arg BASE=ubuntu:24.04 -f "$(hostpath "$HERE")/Dockerfile.systemd" "$(hostpath "$HERE")" >/dev/null \
  || { echo "HATA: ubuntu sınama imajı oluşturulamadı" >&2; exit 2; }
if [[ " $GROUPS_SEL " == *" d "* ]]; then
  ensure_base debian:12
  docker build -q -t "${PFX}debian" --build-arg BASE=debian:12 -f "$(hostpath "$HERE")/Dockerfile.systemd" "$(hostpath "$HERE")" >/dev/null \
    || { echo "HATA: debian sınama imajı oluşturulamadı" >&2; exit 2; }
fi

# --- sürüm paketleri: "make release" deponun KOPYASI üzerinde çalışır -------
build_releases() {
  local c="${PFX}build-run" v
  ensure_base node:24-bookworm-slim
  docker build -q -t "${PFX}build" -f "$(hostpath "$HERE")/Dockerfile.build" "$(hostpath "$HERE")" >/dev/null || return 1
  docker run -d --name "$c" "${PFX}build" sleep 7200 >/dev/null || return 1
  docker exec "$c" mkdir -p /work/repo
  # Çalışma ağacına yazılmaz: depo tar akışıyla kapsayıcıya kopyalanır.
  tar -C "$REPO" --exclude=node_modules --exclude=./dist --exclude=./build --exclude=./.git \
      --exclude=./tests/installer/.work --exclude='*.png' -cf - . \
    | docker exec -i "$c" tar -xf - -C /work/repo || return 1

  if (( REUSE_BIN )) && [[ -f "$WORK/bin/$V1/myserver-linux-amd64" && -f "$WORK/bin/$V2/myserver-linux-amd64" ]]; then
    say "Önceki ikili dosyalar kullanılıyor; yalnızca paketleme yapılacak"
    for v in "$V1" "$V2"; do
      docker exec "$c" rm -rf /work/repo/dist
      docker cp "$(hostpath "$WORK/bin/$v")" "$c:/work/repo/dist" >/dev/null || return 1
      docker exec -w /work/repo "$c" make release-package "VERSION=$v" >"$WORK/logs/build-$v.log" 2>&1 || return 1
      export_release "$c" "$v" || return 1
    done
  else
    say "make release VERSION=$V1 (arayüz + ikili dosyalar + paket)"
    docker exec -w /work/repo "$c" make release "VERSION=$V1" >"$WORK/logs/build-$V1.log" 2>&1 || return 1
    export_release "$c" "$V1" || return 1
    say "make release-build release-package VERSION=$V2"
    docker exec -w /work/repo "$c" make release-build release-package "VERSION=$V2" >"$WORK/logs/build-$V2.log" 2>&1 || return 1
    export_release "$c" "$V2" || return 1
  fi
  docker rm -f "$c" >/dev/null
}
export_release() { # <kapsayıcı> <sürüm>
  local c=$1 v=$2 f
  # Paket düzeninin denetimi (install.sh'nin beklediği dosyalar).
  docker exec -w /work/repo/dist "$c" bash -c '
    set -e
    sha256sum -c --quiet SHA256SUMS
    for a in amd64 arm64; do
      t=$(tar -tzf myserver-linux-$a.tar.gz)
      for f in myserver-linux-$a myserver-helper-linux-$a SHA256SUMS VERSION README.md \
               scripts/install.sh scripts/update.sh scripts/uninstall.sh scripts/backup.sh \
               packaging/myserver.service packaging/myserver.sudoers packaging/myserver.env \
               apps/manifests/jellyfin.yaml apps/icons/jellyfin.svg; do
        grep -qx "myserver/$f" <<<"$t" || { echo "arşivde eksik: $a $f"; exit 1; }
      done
    done
    file_ok() { head -c 4 "$1" | grep -q ELF; }
    file_ok myserver-linux-amd64 && file_ok myserver-helper-linux-arm64
    [ "$(cat VERSION)" = "'"$v"'" ]
    tar -tvzf myserver-linux-amd64.tar.gz | awk "\$2 != \"0/0\" {bad=1} END {exit bad}"
    tar -tvzf myserver-linux-amd64.tar.gz | grep -E "packaging/myserver.sudoers$" | grep -q "^-rw-r--r--"
    tar -tvzf myserver-linux-amd64.tar.gz | grep -E "scripts/install.sh$" | grep -q "^-rwxr-xr-x"
  ' >>"$WORK/logs/build-$v.log" 2>&1 || { echo "RESULT|FAIL|s02.layout.$v|make release çıktısı beklenen düzende|bkz. logs/build-$v.log" >>"$WORK/results.txt"; return 1; }
  echo "RESULT|PASS|s02.layout.$v|make release çıktısı beklenen düzende (sürüm $v: ikili dosyalar, apps, scripts, packaging, VERSION, arşivler, SHA256SUMS)" >>"$WORK/results.txt"
  rm -rf "$WORK/rel/v$v" "$WORK/bin/$v"; mkdir -p "$WORK/rel/v$v" "$WORK/bin/$v"
  docker cp "$c:/work/repo/dist/myserver-linux-amd64.tar.gz" "$(hostpath "$WORK/rel/v$v")/" >/dev/null || return 1
  docker cp "$c:/work/repo/dist/SHA256SUMS" "$(hostpath "$WORK/rel/v$v")/" >/dev/null || return 1
  for f in myserver-linux-amd64 myserver-helper-linux-amd64 myserver-linux-arm64 myserver-helper-linux-arm64; do
    docker cp "$c:/work/repo/dist/$f" "$(hostpath "$WORK/bin/$v")/" >/dev/null || return 1
  done
}

# --- statik çözümleme --------------------------------------------------------
shellcheck_run() {
  local c="${PFX}shellcheck" out
  ensure_base koalaman/shellcheck:stable
  docker create --name "$c" koalaman/shellcheck:stable -s bash -f gcc \
    /s/install.sh /s/uninstall.sh /s/update.sh /s/backup.sh >/dev/null || return 1
  docker cp "$(hostpath "$REPO/scripts")" "$c:/s" >/dev/null
  out=$(docker start -a "$c" 2>&1); local rc=$?
  docker rm -f "$c" >/dev/null
  printf '%s\n' "$out" >"$WORK/logs/shellcheck.log"
  if (( rc == 0 )); then echo "RESULT|PASS|s01.shellcheck|shellcheck: dört betikte bulgu yok" >>"$WORK/results.txt"
  else echo "RESULT|FAIL|s01.shellcheck|shellcheck: dört betikte bulgu yok|$(printf '%s' "$out" | head -n 5 | tr '\n|' ' /')" >>"$WORK/results.txt"; fi
}

# --- kapsayıcı grupları --------------------------------------------------------
start_container() { # <ad> <imaj> [ek docker seçenekleri...]
  local name=$1 image=$2 i; shift 2
  docker run -d --name "$name" --hostname "${name/inst-/}" --network "${PFX}net" \
    --privileged --cgroupns=private --tmpfs /run --tmpfs /run/lock --tmpfs /tmp:exec "$@" "$image" >/dev/null || return 1
  for (( i = 0; i < 60; i++ )); do
    case "$(docker exec "$name" systemctl is-system-running 2>/dev/null)" in running|degraded) break ;; esac
    sleep 1
  done
  docker exec "$name" mkdir -p /opt/tests /opt/rel
  docker cp "$(hostpath "$HERE/incontainer")/." "$name:/opt/tests/" >/dev/null || return 1
  docker cp "$(hostpath "$WORK/rel")/." "$name:/opt/rel/" >/dev/null || return 1
  docker exec "$name" bash -c 'sed -i "s/\r$//" /opt/tests/*.sh /opt/tests/*.py'
}
scenario() { # <kapsayıcı> <günlük adı> <betik> [bağımsız değişkenler...]; ortam: SCEN_ENV
  local c=$1 name=$2 log="$WORK/logs/$2.log" script=$3 n; shift 3
  # shellcheck disable=SC2086  # SCEN_ENV bilerek sözcüklere bölünür ("-e A=B")
  docker exec ${SCEN_ENV:-} "$c" bash "/opt/tests/$script" "$@" >"$log" 2>&1
  n=$(grep -c '^RESULT|' "$log")
  if (( n == 0 )); then
    echo "RESULT|FAIL|$name.crash|senaryo hiç sonuç üretmedi|$(tail -n 3 "$log" | tr '\n|' ' /')" >>"$WORK/results.txt"
  fi
  grep '^RESULT|' "$log" >>"$WORK/results.txt"
  echo "    [$c] $name: $(grep -c '^RESULT|PASS' "$log") geçti, $(grep -c '^RESULT|FAIL' "$log") kaldı"
}

group_a() {
  local c="${PFX}a"
  start_container "$c" "${PFX}ubuntu" || { echo "RESULT|FAIL|a.start|kapsayıcı başlatılamadı|" >>"$WORK/results.txt"; return; }
  scenario "$c" s03-install    s03_install.sh s03
  scenario "$c" s04-idempotent s04_idempotent.sh
  scenario "$c" s10-backup     s10_backup.sh
  scenario "$c" s08-update     s08_update.sh
  scenario "$c" s07-firewall   s07_firewall.sh
  scenario "$c" s09-uninstall  s09_uninstall.sh
}
group_b() {
  local c="${PFX}b"
  start_container "$c" "${PFX}ubuntu" || { echo "RESULT|FAIL|b.start|kapsayıcı başlatılamadı|" >>"$WORK/results.txt"; return; }
  scenario "$c" s05-failures s05_failures.sh
}
group_c() {
  local c="${PFX}c"
  # İç içe Docker için /var/lib/docker overlay üzerinde olmamalı: adlandırılmış birim.
  start_container "$c" "${PFX}ubuntu" -v "${PFX}c-docker:/var/lib/docker" \
    -v "${PFX}c-containerd:/var/lib/containerd" \
    || { echo "RESULT|FAIL|c.start|kapsayıcı başlatılamadı|" >>"$WORK/results.txt"; return; }
  scenario "$c" s06-docker s06_docker.sh
  # İç daemon çalışıyor VE gerçekten kapsayıcı başlatabiliyorsa o kullanılır.
  if [[ "$(docker exec "$c" systemctl is-active docker 2>/dev/null)" == active ]] \
     && docker exec "$c" docker run --rm busybox:1.36 true >/dev/null 2>&1; then
    echo "RESULT|INFO|s09.l3.approach|düzey 3 yaklaşımı|sınama kapsayıcısının kendi içindeki (yalıtılmış) Docker daemon'ı" >>"$WORK/results.txt"
    SCEN_ENV="" scenario "$c" s09-level3 s09_level3.sh
  else
    # Yedek yol: ayrı bir docker:dind kapsayıcısı (yalnızca sınama ağında, TLS'siz).
    ensure_base docker:dind
    docker run -d --name "${PFX}dind" --hostname "${PFX}dind" --network "${PFX}net" --privileged \
      -e DOCKER_TLS_CERTDIR= -v "${PFX}dind-data:/var/lib/docker" docker:dind >/dev/null
    local i
    for (( i = 0; i < 40; i++ )); do
      docker exec "${PFX}dind" docker info >/dev/null 2>&1 && break
      sleep 1
    done
    echo "RESULT|INFO|s09.l3.approach|düzey 3 yaklaşımı|ayrı docker:dind kapsayıcısı (${PFX}dind), DOCKER_HOST ile" >>"$WORK/results.txt"
    SCEN_ENV="-e DOCKER_HOST=tcp://${PFX}dind:2375" scenario "$c" s09-level3 s09_level3.sh
  fi
}
group_d() {
  local c="${PFX}d"
  start_container "$c" "${PFX}debian" || { echo "RESULT|FAIL|d.start|kapsayıcı başlatılamadı|" >>"$WORK/results.txt"; return; }
  scenario "$c" s11-debian-install    s03_install.sh s11
  scenario "$c" s11-debian-idempotent s04_idempotent.sh s11.s04
}

shellcheck_run
build_releases || { echo "HATA: sürüm paketleri oluşturulamadı (bkz. $WORK/logs/build-*.log)" >&2; tail -n 20 "$WORK"/logs/build-*.log >&2; exit 2; }
docker network create "${PFX}net" >/dev/null || exit 2

say "Senaryolar çalıştırılıyor (gruplar: $GROUPS_SEL)"
for g in $GROUPS_SEL; do
  case "$g" in a|b|c|d) ;; *) echo "Bilinmeyen grup: $g" >&2; exit 2 ;; esac
  if (( SERIAL )); then "group_$g"; else "group_$g" & fi
done
wait

# --- sonuç tablosu ------------------------------------------------------------
say "Sonuçlar"
awk -F'|' '
  $1 != "RESULT" { next }
  { split($3, id, "."); s = id[1]; if (!(s in seen)) { seen[s] = 1; order[++n] = s } }
  $2 == "PASS" { p[s]++; tp++ }
  $2 == "FAIL" { f[s]++; tf++; fails[++nf] = $3 " - " $4 (length($5) ? "  [" $5 "]" : "") }
  $2 == "INFO" { infos[++ni] = $3 " - " $4 (length($5) ? ": " $5 : "") }
  END {
    printf "%-10s %8s %8s  %s\n", "SENARYO", "GEÇTİ", "KALDI", "DURUM"
    for (i = 1; i <= n; i++) { s = order[i]; printf "%-10s %8d %8d  %s\n", s, p[s], f[s], (f[s] ? "KALDI" : "GEÇTİ") }
    printf "%-10s %8d %8d  %s\n", "TOPLAM", tp, tf, (tf ? "KALDI" : "GEÇTİ")
    if (ni) { print "\nBilgi:"; for (i = 1; i <= ni; i++) print "  * " infos[i] }
    if (nf) { print "\nBaşarısız denetimler:"; for (i = 1; i <= nf; i++) print "  - " fails[i] }
    exit (tf ? 1 : 0)
  }' "$WORK/results.txt"
rc=$?
echo
echo "Ayrıntılı günlükler: $WORK/logs/"
exit "$rc"
