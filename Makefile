# MyServer - derleme ve paketleme
#
# GNU make gerekir. Linux'ta ve Windows'ta Git Bash altında çalışır (tarifler
# POSIX sh ile yazılmıştır; Windows'ta sh.exe PATH üzerinde olmalıdır).
#
#   make build                 arayüz + Linux/amd64 ikili dosyaları (build/)
#   make release VERSION=1.2.3 dist/ altında amd64 ve arm64 sürüm paketleri
#   make test-installer        kurulum betiklerini Docker kapsayıcılarında sınar
#
# Değişkenler komut satırından değiştirilebilir:
#   make release VERSION=1.2.3 GO=/yol/go

GO      ?= go
NPM     ?= npm
GOOS    ?= linux
GOARCH  ?= amd64
SHA256  ?= sha256sum

# Sürüm: verilmezse en yakın "v1.2.3" biçimli git etiketinden alınır.
VERSION ?= $(shell git describe --tags --match "v[0-9]*" --abbrev=0 2>/dev/null | sed "s/^v//")
ifeq ($(strip $(VERSION)),)
VERSION := 1.0.0-dev
endif

LDFLAGS := -s -w -X myserver/internal/config.Version=$(VERSION)
ARCHES  := amd64 arm64
DIST    := dist
BUILD   := build

# Hedeflerin ön koşulları sırayla çalışmalıdır (frontend -> release-build ->
# release-package); "make -j" ile bile paralel çalıştırılmaz.
.NOTPARALLEL:

.PHONY: all build frontend backend release release-build release-package test-installer test test-backend test-frontend vet fmt clean dev-backend dev-frontend

all: build

build: frontend backend

# Arayüz derlemesi kendisini backend/internal/webui/dist içine kopyalar
# (frontend/scripts/embed.mjs); panel dosyası arayüzü oradan gömer.
frontend:
	cd frontend && $(NPM) ci && $(NPM) run build

backend:
	mkdir -p $(BUILD)
	cd backend && CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BUILD)/myserver ./cmd/myserver
	cd backend && CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BUILD)/myserver-helper ./cmd/myserver-helper

# dist/ düzeni (install.sh yerel kipte bunu bekler):
#   dist/myserver-linux-<mimari>          dist/apps/       dist/VERSION
#   dist/myserver-helper-linux-<mimari>   dist/scripts/    dist/README.md
#   dist/myserver-linux-<mimari>.tar.gz   dist/packaging/  dist/SHA256SUMS
# Her arşiv, aynı düzeni "myserver/" üst dizini altında tek mimari için içerir.
release: frontend release-build release-package

# Yalnızca ikili dosyalar (arayüz daha önce derlenmiş olmalıdır).
release-build:
	rm -rf $(DIST)
	mkdir -p $(DIST)
	set -e; for arch in $(ARCHES); do \
	  echo "==> linux/$$arch derleniyor ($(VERSION))"; \
	  ( cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o ../$(DIST)/myserver-linux-$$arch ./cmd/myserver ); \
	  ( cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o ../$(DIST)/myserver-helper-linux-$$arch ./cmd/myserver-helper ); \
	done

# Paketleme: dist/ içindeki ikili dosyaların yanına betikleri, uygulama
# tanımlarını ve paketleme dosyalarını koyar; arşivleri ve SHA256SUMS üretir.
# Dosya izinleri kaynak ağaçtan bağımsız olarak düzeltilir (Windows'ta veya
# bağlanmış bir dizinde her dosya 0755 görünebilir).
release-package:
	set -e; for arch in $(ARCHES); do \
	  test -f $(DIST)/myserver-linux-$$arch && test -f $(DIST)/myserver-helper-linux-$$arch \
	    || { echo "HATA: $(DIST)/ icinde $$arch dosyalari yok; once 'make release-build' calistirin." >&2; exit 1; }; \
	done
	rm -rf $(DIST)/apps $(DIST)/scripts $(DIST)/packaging $(DIST)/.stage
	cp -R apps scripts packaging $(DIST)/
	cp README.md $(DIST)/README.md
	printf '%s\n' "$(VERSION)" > $(DIST)/VERSION
	set -e; cd $(DIST); \
	find apps scripts packaging -type d -exec chmod 0755 {} +; \
	find apps scripts packaging README.md VERSION -type f -exec chmod 0644 {} +; \
	chmod 0755 scripts/*.sh; \
	for arch in $(ARCHES); do chmod 0755 myserver-linux-$$arch myserver-helper-linux-$$arch; done
	set -e; \
	tarflags=""; \
	if tar --version 2>/dev/null | grep -q "GNU tar"; then tarflags="--owner=0 --group=0 --numeric-owner"; fi; \
	for arch in $(ARCHES); do \
	  stage=$(DIST)/.stage/$$arch/myserver; \
	  mkdir -p $$stage; \
	  cp -p $(DIST)/myserver-linux-$$arch $(DIST)/myserver-helper-linux-$$arch $$stage/; \
	  cp -Rp $(DIST)/apps $(DIST)/scripts $(DIST)/packaging $$stage/; \
	  cp -p $(DIST)/README.md $(DIST)/VERSION $$stage/; \
	  ( cd $$stage && $(SHA256) myserver-linux-$$arch myserver-helper-linux-$$arch > SHA256SUMS ); \
	  tar -czf $(DIST)/myserver-linux-$$arch.tar.gz $$tarflags -C $(DIST)/.stage/$$arch myserver; \
	done; \
	rm -rf $(DIST)/.stage
	set -e; cd $(DIST); files=""; \
	for arch in $(ARCHES); do \
	  files="$$files myserver-linux-$$arch.tar.gz myserver-linux-$$arch myserver-helper-linux-$$arch"; \
	done; \
	$(SHA256) $$files > SHA256SUMS
	@echo "==> Sürüm hazır: $(DIST)/ (sürüm $(VERSION))"

test: test-backend test-frontend

# Kurulum betiklerinin kapsayıcı içi sınamaları (Docker gerekir). Betikler
# yalnızca tek kullanımlık "mstest-inst-" kapsayıcılarında çalıştırılır.
test-installer:
	bash tests/installer/run.sh

test-backend:
	cd backend && $(GO) test ./...

test-frontend:
	cd frontend && $(NPM) test

vet:
	cd backend && GOOS=linux GOARCH=$(GOARCH) $(GO) vet ./...

fmt:
	cd backend && $(GO) fmt ./...

clean:
	rm -rf $(DIST) $(BUILD) frontend/dist
	find backend/internal/webui/dist -mindepth 1 ! -name README.txt -exec rm -rf {} +

# Geliştirme: arka uç yalnızca Linux'ta çalışır (Windows'ta WSL veya bir
# Linux makinesi kullanın). Veriler depo içindeki .devdata/ dizinine yazılır.
dev-backend:
	mkdir -p .devdata
	cd backend && MYSERVER_LISTEN=127.0.0.1:8080 MYSERVER_DATA_DIR=../.devdata \
	  MYSERVER_MANIFEST_DIR=../apps/manifests MYSERVER_LOG_LEVEL=debug \
	  $(GO) run ./cmd/myserver

# Arayüz geliştirme sunucusu (http://localhost:5173). /api istekleri
# MYSERVER_BACKEND adresine yönlendirilir (varsayılan http://127.0.0.1:8080).
dev-frontend:
	cd frontend && $(NPM) run dev
