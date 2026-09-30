#!/usr/bin/env bash
# Sınama kapsayıcısının İÇİNDE çalışan ortak yardımcılar.
# Çıktı sözleşmesi: her denetim tek satır "RESULT|PASS|<kimlik>|<açıklama>" veya
# "RESULT|FAIL|<kimlik>|<açıklama>|<ayrıntı>" yazar; run.sh bunları toplar.

if [[ ! -f /.dockerenv && "${container:-}" != docker ]]; then
  echo "Bu betik yalnızca sınama kapsayıcısının içinde çalıştırılabilir." >&2
  exit 99
fi

REL_DIR=/opt/rel
INSTALLER_DIR=/root/myserver
ADMIN_USER=admin
ADMIN_PASS='Deneme-Parola-4821'
HTTPS_PORT=8443
HTTPS_BASE="https://localhost:${HTTPS_PORT}"

_clean() { printf '%s' "$1" | tr '\n|' ' /' | cut -c1-400; }
pass() { printf 'RESULT|PASS|%s|%s\n' "$1" "$2"; }
fail() { printf 'RESULT|FAIL|%s|%s|%s\n' "$1" "$2" "$(_clean "${3:-}")"; }
info() { printf 'RESULT|INFO|%s|%s|%s\n' "$1" "$2" "$(_clean "${3:-}")"; }

# expect_ok <kimlik> <açıklama> <komut...>
expect_ok() {
  local id=$1 desc=$2 out; shift 2
  if out=$("$@" 2>&1); then pass "$id" "$desc"; else fail "$id" "$desc" "rc=$? $out"; fi
}
expect_fail() {
  local id=$1 desc=$2 out; shift 2
  if out=$("$@" 2>&1); then fail "$id" "$desc" "komut başarılı oldu: $out"; else pass "$id" "$desc"; fi
}
expect_eq() {
  local id=$1 desc=$2 want=$3 got=$4
  if [[ "$want" == "$got" ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "beklenen=[$want] bulunan=[$got]"; fi
}
expect_match() {
  local id=$1 desc=$2 re=$3 got=$4
  if [[ "$got" =~ $re ]]; then pass "$id" "$desc"; else fail "$id" "$desc" "desen=[$re] bulunan=[$got]"; fi
}
expect_nomatch() {
  local id=$1 desc=$2 re=$3 got=$4
  if [[ "$got" =~ $re ]]; then fail "$id" "$desc" "istenmeyen desen=[$re] bulundu: $got"; else pass "$id" "$desc"; fi
}
expect_stat() { # <kimlik> <yol> <sahip:grup> <kip>
  local id=$1 path=$2 owner=$3 mode=$4 got
  got=$(stat -c '%U:%G %a' "$path" 2>&1)
  expect_eq "$id" "$path sahiplik/izin $owner $mode" "$owner $mode" "$got"
}

fresh_release() { # <sürüm dizini, ör. v1.0.0>
  rm -rf "$INSTALLER_DIR"
  tar -xzf "$REL_DIR/$1/myserver-linux-amd64.tar.gz" -C /root
}

primary_ip() { ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.*src \([0-9.]*\).*/\1/p' | head -n 1; }

wait_http() { # <port> [saniye]
  local port=$1 max=${2:-40} i
  for (( i = 0; i < max; i++ )); do
    curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${port}/" && return 0
    sleep 1
  done
  return 1
}

api_status() { curl -s --max-time 5 "http://127.0.0.1:${1}/api/v1/auth/status"; }

api_setup() { # <port>
  # Sunucu adı ve saat dilimi MEVCUT değerlerle gönderilir: sihirbaz böylece
  # hostname-set / timezone-set yardımcı eylemlerini çağırmaz. (Ayrıcalıklı
  # kapsayıcı çekirdeği host ile paylaşır; saat ayarına dokunulmamalıdır.)
  local tz
  tz=$(readlink /etc/localtime 2>/dev/null | sed -n 's|.*zoneinfo/||p')
  [[ -n "$tz" ]] || tz=$(cat /etc/timezone 2>/dev/null)
  [[ -n "$tz" ]] || tz=UTC
  curl -s --max-time 20 -H "Origin: http://127.0.0.1:${1}" -H 'Content-Type: application/json' \
    -X POST "http://127.0.0.1:${1}/api/v1/auth/setup" \
    -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\",\"hostname\":\"$(hostname)\",\"timezone\":\"${tz}\"}"
}

api_login() { # <port>
  curl -s --max-time 20 -H "Origin: http://127.0.0.1:${1}" -H 'Content-Type: application/json' \
    -X POST "http://127.0.0.1:${1}/api/v1/auth/login" \
    -d "{\"username\":\"${ADMIN_USER}\",\"password\":\"${ADMIN_PASS}\"}"
}

db_query() { runuser -u myserver -- sqlite3 -cmd '.timeout 10000' /var/lib/myserver/myserver.db "$1"; }

# Hiçbir şeyin kurulmadığını doğrular.
expect_nothing_installed() { # <kimlik öneki>
  local p=$1 found=""
  local f
  for f in /usr/local/bin/myserver /usr/local/libexec/myserver-helper /etc/sudoers.d/myserver \
           /etc/systemd/system/myserver.service; do
    [[ -e "$f" ]] && found+="$f "
  done
  expect_eq "$p" "hiçbir program/sudoers/servis dosyası kurulmadı" "" "$found"
}

# Yerel https sunucusu (sınama CA'sı kapsayıcıda güvenilir kılınır).
start_https() {
  if [[ ! -f /opt/https/cert.pem ]]; then
    mkdir -p /opt/https /srv/rel
    openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj '/CN=localhost' \
      -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
      -keyout /opt/https/key.pem -out /opt/https/cert.pem >/dev/null 2>&1
    cp /opt/https/cert.pem /usr/local/share/ca-certificates/mstest-inst.crt
    update-ca-certificates >/dev/null 2>&1
  fi
  if ! curl -s -o /dev/null --max-time 2 "${HTTPS_BASE}/"; then
    systemctl reset-failed mstest-https >/dev/null 2>&1 || true
    systemd-run --quiet --unit=mstest-https \
      /usr/bin/python3 /opt/tests/https_server.py /srv/rel "$HTTPS_PORT" 8081 \
      /opt/https/cert.pem /opt/https/key.pem
    local i
    for (( i = 0; i < 20; i++ )); do
      curl -s -o /dev/null --max-time 2 "${HTTPS_BASE}/" && return 0
      sleep 0.5
    done
    echo "https sunucusu başlatılamadı" >&2
    return 1
  fi
}

# /srv/rel/<ad>/<sürüm>/ altına arşiv + SHA256SUMS yayımlar.
publish() { # <yayın adı> <sürüm dizini> <arşiv yolu> [bozuk-özet]
  local d="/srv/rel/$1/$2"
  mkdir -p "$d"
  cp "$3" "$d/myserver-linux-amd64.tar.gz"
  if [[ "${4:-}" == bad ]]; then
    printf '%064d  myserver-linux-amd64.tar.gz\n' 0 >"$d/SHA256SUMS"
  else
    ( cd "$d" && sha256sum myserver-linux-amd64.tar.gz >SHA256SUMS )
  fi
}

# Açılışta hemen çıkan (başlamayan) bir panel içeren sürüm arşivi üretir.
# --version ve --migrate çalışır; böylece betiğin ön sınamalarından geçer ve
# hata ancak servis başlatılırken ortaya çıkar (geri dönüş yolu sınanır).
make_broken_release() { # <temel sürüm dizini> <yeni sürüm> <çıktı arşivi>
  local w
  w=$(mktemp -d /root/broken.XXXXXX)
  tar -xzf "$REL_DIR/$1/myserver-linux-amd64.tar.gz" -C "$w"
  cat >"$w/myserver/myserver-linux-amd64" <<EOF
#!/bin/sh
case "\$1" in
  --version) echo "$2"; exit 0 ;;
  --migrate) exit 0 ;;
esac
echo "mstest: bu surum bilerek baslamaz" >&2
exit 1
EOF
  chmod 0755 "$w/myserver/myserver-linux-amd64"
  printf '%s\n' "$2" >"$w/myserver/VERSION"
  ( cd "$w/myserver" && sha256sum myserver-linux-amd64 myserver-helper-linux-amd64 >SHA256SUMS )
  tar -czf "$3" -C "$w" myserver
  rm -rf "$w"
}
