APP_NAME := say
BUILD_PKG := .
TEST_PKG := ./...
BIN_DIR := bin
GO := go

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
	windows/arm64

.PHONY: build run test tidy fmt clean build-all $(TARGETS:%=build-%)

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=1 $(GO) build -o $(HOST_BIN) $(BUILD_PKG)

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

$(TARGETS:%=build-%):
	$(eval TARGET_OS := $(word 1,$(subst /, ,$*)))
	$(eval TARGET_ARCH := $(word 2,$(subst /, ,$*)))
	$(eval TARGET_DIR := $(BIN_DIR)/$(TARGET_OS)-$(TARGET_ARCH))
	$(eval TARGET_BIN := $(TARGET_DIR)/$(APP_NAME)$(if $(filter windows,$(TARGET_OS)),.exe,))
	@mkdir -p $(TARGET_DIR)
	GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH) CGO_ENABLED=1 $(GO) build -o $(TARGET_BIN) $(BUILD_PKG)
