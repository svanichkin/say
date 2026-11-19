APP_NAME := say
BUILD_PKG := .
TEST_PKG := ./...
BIN_DIR := bin
GO := go

VERSION ?= $(shell git describe --tags --dirty --always 2>/dev/null)
# GOTAGS=debug makes a debug build (see README, "Debug build").
GOTAGS ?=
BUILD_FLAGS := -tags "$(GOTAGS)"
ifneq ($(strip $(VERSION)),)
	BUILD_FLAGS += -ldflags "-X main.version=$(VERSION)"
endif

HOST_OS := $(shell $(GO) env GOOS)
HOST_ARCH := $(shell $(GO) env GOARCH)
HOST_BIN := $(BIN_DIR)/$(APP_NAME)$(if $(filter windows,$(HOST_OS)),.exe,)

TARGETS := \
	linux/amd64 \
	linux/arm64 \
	darwin/amd64 \
	darwin/arm64 \
	freebsd/amd64 \
	freebsd/arm64 \
	openbsd/amd64 \
	openbsd/arm64 \
	windows/amd64 \
	windows/arm64 \
	plan9/amd64 \
	plan9/386 \
	plan9/arm

.PHONY: build debug app icons install-desktop run test tidy fmt clean build-all $(TARGETS:%=build-%)

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 $(GO) build $(BUILD_FLAGS) -o $(HOST_BIN) $(BUILD_PKG)

# debug: a debug build for this machine (see README, "Debug build").
debug:
	$(MAKE) build GOTAGS=debug

# icons: draws Say's icon (cmd/sayicon) into assets/icon and gui/icon, and
# puts it into the Windows executable (rsrc_windows_amd64.syso; needs mingw).
icons:
	$(GO) run ./cmd/sayicon -out assets/icon -gui gui/icon
	sh scripts/windows-icon.sh

# install-desktop: Say in the desktop's menu (Linux, BSD): its launcher and
# icon for this user. The launcher opens Say in a terminal; say must be on
# the PATH.
DATA_HOME ?= $(HOME)/.local/share
install-desktop:
	mkdir -p $(DATA_HOME)/applications $(DATA_HOME)/icons/hicolor/256x256/apps $(DATA_HOME)/icons/hicolor/48x48/apps
	install -m 644 packaging/linux/say.desktop $(DATA_HOME)/applications/say.desktop
	install -m 644 gui/icon/icon-256.png $(DATA_HOME)/icons/hicolor/256x256/apps/say.png
	install -m 644 gui/icon/icon-48.png $(DATA_HOME)/icons/hicolor/48x48/apps/say.png

# app: Say.app for macOS in bin/: started from it (a double click) Say opens
# its window. GOTAGS=debug for a debug build.
app: build
	sh scripts/macos-app.sh $(HOST_BIN) "$(VERSION)" $(BIN_DIR)

run:
	$(GO) run .

test:
	$(GO) test $(TEST_PKG)

tidy:
	$(GO) mod tidy

fmt:
	$(GO) fmt ./...

clean:
	rm -rf $(BIN_DIR)

build-all: $(TARGETS:%=build-%)

$(TARGETS:%=build-%): build-%:
	$(eval TARGET_OS := $(word 1,$(subst /, ,$*)))
	$(eval TARGET_ARCH := $(word 2,$(subst /, ,$*)))
	$(eval TARGET_DIR := $(BIN_DIR)/$(TARGET_OS)-$(TARGET_ARCH))
	$(eval TARGET_BIN := $(TARGET_DIR)/$(APP_NAME)$(if $(filter windows,$(TARGET_OS)),.exe,))
	@mkdir -p $(TARGET_DIR)
	GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH) CGO_ENABLED=$(if $(filter plan9,$(TARGET_OS)),0,1) $(GO) build $(BUILD_FLAGS) -o $(TARGET_BIN) $(BUILD_PKG)
