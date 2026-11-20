NAME := lyrunderground
APP_NAME := LyricsFromTheUnderground

# Test files are excluded: they do not affect the binaries.
SRCS := $(shell find . -type f -name '*.go' ! -name '*_test.go' | sort)

BUILDINFO := codeberg.org/unixery/buildinfo
RSRC := $(shell command -v rsrc 2>/dev/null)

.DEFAULT_GOAL := build

# if inside a git working tree, set some vars
ifeq ($(shell git rev-parse --is-inside-work-tree 2>/dev/null),true)
GIT_SHA := $(shell git rev-parse --short HEAD)
GIT_TAG := $(shell git describe --tags 2>/dev/null)
GIT_DIRTY := $(if $(strip $(shell git status --porcelain)),true,false)
else
GIT_SHA := n/a
GIT_DIRTY := n/a
GIT_TAG := n/a
endif
# optional: if empty, set dummy version
ifeq ($(strip $(GIT_TAG)),)
GIT_TAG := v0.0.0
endif
# optional: print warning warning on dirty working tree, append -dirty to commit id
ifeq ($(GIT_DIRTY),true)
#$(warning Git working tree is dirty)
GIT_SHA := $(GIT_SHA)-dirty
endif

# The binaries embed GIT_SHA/GIT_TAG via ldflags, but make only compares
# file mtimes, so a commit or tag alone would not trigger a rebuild.
# A state file bridges the gap; all binaries depend on it:
#
#   git-state (phony) -> bin/.git-state -> binaries
#
# git-state is .PHONY, so it is always out-of-date, which makes the
# bin/.git-state recipe run on every build. The recipe only rewrites
# the file when the value actually changed, so the file's mtime only
# changes then. The binaries depend on the real file, not the phony
# target: make compares mtimes, so they are rebuilt only when the file
# was rewritten. (Depending on the phony target directly would rebuild
# on every invocation.)
GIT_STATE := bin/.git-state

.PHONY: git-state
git-state:

${GIT_STATE}: git-state
	@mkdir -p bin
	@echo "${GIT_SHA} ${GIT_TAG}" > ${GIT_STATE}.tmp
	@if cmp -s ${GIT_STATE}.tmp ${GIT_STATE} 2>/dev/null; then \
		rm -f ${GIT_STATE}.tmp; \
	else \
		mv ${GIT_STATE}.tmp ${GIT_STATE}; \
	fi

# Fyne renders through OpenGL/GLFW, so every build is a cgo build. Building a
# Windows binary therefore needs a C toolchain for the target platform, which is
# not part of a plain local build. Windows binaries come either from
# `make windows-cross` (containerised, no host toolchain) or from `make windows`
# on a Windows host / CI runner with mingw-w64 installed.
.PHONY: build
build: darwin

.PHONY: sign
sign: sign-windows sign-darwin

.PHONY: darwin windows windows-cross
darwin: bin/darwin-arm64/${NAME}
windows: bin/windows-amd64/${NAME}.exe

# Container engine used by fyne-cross; pass e.g. FYNE_CROSS_ENGINE=docker to
# override (an empty value lets fyne-cross autodetect).
FYNE_CROSS_ENGINE ?= podman

# Package metadata version. `git describe` yields values like v1.2.3-4-gabcde,
# which fyne rejects, so anything but a plain tag falls back to 0.0.0.
APP_VERSION := $(firstword $(subst -, ,$(patsubst v%,%,${GIT_TAG})))
ifneq (,$(or $(filter n/a,${APP_VERSION}),$(if ${APP_VERSION},,x)))
APP_VERSION := 0.0.0
endif

# Monotonic build number for CFBundleVersion: the commit count only grows, which
# is what macOS expects for bundle versions.
APP_BUILD := $(shell git rev-list --count HEAD 2>/dev/null)
ifeq (${APP_BUILD},)
APP_BUILD := 1
endif

# Linker flags that embed the git metadata into the binary.
GO_LDFLAGS := -X '${BUILDINFO}.GitSha=${GIT_SHA}' -X '${BUILDINFO}.GitTag=${GIT_TAG}' -s -w
# `fyne package` builds the binary itself and only picks up linker flags from
# GOFLAGS. Every -X has to be a separate "-ldflags=" field inside GOFLAGS,
# because fyne reads them from there and joins them again.
FYNE_GOFLAGS := -ldflags=-X=${BUILDINFO}.GitSha=${GIT_SHA} -ldflags=-X=${BUILDINFO}.GitTag=${GIT_TAG}
FYNE := GOFLAGS="${FYNE_GOFLAGS}" fyne
FYNE-CROSS := GOFLAGS="${FYNE_GOFLAGS}" fyne-cross

.PHONY: sign-darwin sign-windows
sign-darwin:  bin/darwin-arm64/${NAME}-signed
sign-windows: bin/windows-amd64/${NAME}-signed.exe

#
# BUILDING
#
bin/darwin-arm64/${NAME}: $(SRCS) ${GIT_STATE}
	@mkdir -p bin/darwin-arm64/
	@echo "Building darwin/arm64..."
	env GOOS=darwin GOARCH=arm64 go build -ldflags "${GO_LDFLAGS}" -o bin/darwin-arm64/${NAME}

windows-cross: ${GIT_STATE} Icon.png
	@command -v fyne-cross >/dev/null || { \
		echo "fyne-cross not found: go install github.com/fyne-io/fyne-cross@latest"; \
		exit 1; \
	}
	@echo "Building windows/amd64 via fyne-cross (engine: $(if $(FYNE_CROSS_ENGINE),$(FYNE_CROSS_ENGINE),autodetect))..."
	$(FYNE-CROSS) windows -arch amd64 \
		-engine "$(FYNE_CROSS_ENGINE)" \
		-name ${NAME} -icon Icon.png \
		-app-id de.unixery.${NAME} \
		-app-version ${APP_VERSION} -app-build ${APP_BUILD}
	@mkdir -p bin/windows-amd64
	mv fyne-cross/bin/windows-amd64/${NAME}.exe bin/windows-amd64/${NAME}.exe

bin/windows-amd64/${NAME}.exe: $(SRCS) ${GIT_STATE} rsrc_windows_amd64.syso
	@mkdir -p bin/windows-amd64/
	@echo "Building windows/amd64..."
	env GOOS=windows GOARCH=amd64 go build -ldflags "${GO_LDFLAGS}" -o bin/windows-amd64/${NAME}.exe

.PHONY: run-darwin
run-darwin: darwin
	@echo "Running darwin/arm64..."
	@./bin/darwin-arm64/${NAME}

#
# PACKAGING
#
package/darwin/$(APP_NAME).app: $(SRCS) Icon.png ${GIT_STATE}
	@mkdir -p package/darwin/
	@echo "Packaging for darwin ($(APP_NAME) $(APP_VERSION) build $(APP_BUILD))..."
	@rm -rf package/darwin/$(APP_NAME).app
	@# `fyne package` calls `go build` without linker flags. It only picks them
	@# up from GOFLAGS, so without this the bundle reports version "dev".
	$(FYNE) package -os darwin \
		-name $(APP_NAME) \
		-app-id de.unixery.${NAME} \
		-appVersion $(APP_VERSION) -appBuild $(APP_BUILD) \
		-use-raw-icon \
		-release
	mv $(APP_NAME).app package/darwin/

#
# SIGNING
#
codesign-cert.pfx:
	@echo "You need a code signing certificate to sign a binary!"
	@exit 1

.PHONY: sign-darwin sign-windows
bin/darwin-arm64/${NAME}-signed: bin/darwin-arm64/${NAME} codesign-cert.pfx
# https://github.com/anchore/quill
	@echo "Signing darwin/arm64..."
	@cp -a bin/darwin-arm64/${NAME} bin/darwin-arm64/${NAME}-signed
	quill sign --p12 codesign-cert.pfx --timestamp-server http://timestamp.digicert.com --identity de.pdnx.${NAME} bin/darwin-arm64/${NAME}-signed || (rm -f bin/darwin-arm64/${NAME}-signed && exit 1)

bin/windows-amd64/${NAME}-signed.exe: bin/windows-amd64/${NAME}.exe codesign-cert.pfx
# https://github.com/mtrojnar/osslsigncode
	@echo "Signing windows/amd64..."
	@rm -f bin/windows-amd64/${NAME}-signed.exe
	osslsigncode sign -pkcs12 codesign-cert.pfx $(if $(CODESIGN_PASS),-pass $(CODESIGN_PASS)) -n "${NAME}" -t http://timestamp.digicert.com -in bin/windows-amd64/${NAME}.exe -out bin/windows-amd64/${NAME}-signed.exe

#
# ICON
#
icon.ico: images/icon-fullsize.svg
	@mkdir -p tmp/
	@for s in 16 32 48 64 128 256; do \
		magick -background none "images/icon-fullsize.svg[$${s}x$${s}]" -alpha on "tmp/icon-$${s}px.png" 2>/dev/null || exit 1; \
	done
	magick tmp/icon-16px.png tmp/icon-32px.png tmp/icon-48px.png tmp/icon-64px.png tmp/icon-128px.png tmp/icon-256px.png -background none -alpha on icon.ico

#Icon.png: images/icon-fullsize.webp
#	magick images/icon-fullsize.webp -resize 1024x1024 -write Icon.png null:
Icon.png: images/icon-fullsize.svg
	magick -background none "images/icon-fullsize.svg[1024x1024]" -define png:compression-level=9 "Icon.png" 2>/dev/null

rsrc_windows_amd64.syso: icon.ico
	@if [ -n "$(RSRC)" ]; then \
		"$(RSRC)" -arch amd64 -ico icon.ico; \
	elif [ -x "$(HOME)/go/bin/rsrc" ]; then \
		"$(HOME)/go/bin/rsrc" -arch amd64 -ico icon.ico; \
	else \
		echo "WARNING: rsrc not found in PATH or at $(HOME)/go/bin/rsrc"; \
		echo "WARNING: will not create a Windows app icon."; \
		echo "WARNING: install with: go install github.com/akavel/rsrc@latest"; \
	fi

#
# CLEANING
#
.PHONY: clean
clean:
	go clean
	rm -rf bin/
	rm -rf package/
	rm -rf tmp/
	rm -rf icon.ico rsrc_windows_amd64.syso
	rm -rf fyne-cross/
	rm -rf Icon.png
