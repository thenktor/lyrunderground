NAME := lyrunderground
APP_NAME := LyricsFromTheUnderground

SRCS := $(shell find . -type f -name '*.go' | sort)
CSVS := $(wildcard *.csv)

FILES_DARWIN := $(wildcard scripts/*-macos.sh)

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
$(warning Git working tree is dirty)
GIT_SHA := $(GIT_SHA)-dirty
endif

# Detect the operating system
ifeq ($(OS),Windows_NT)
	RSYNC = rsync.exe
else
	RSYNC = rsync
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

.PHONY: build
build: darwin windows

.PHONY: sign
sign: sign-windows sign-darwin

.PHONY: darwin windows
darwin: bin/darwin-arm64/${NAME}
windows: bin/windows-amd64/${NAME}.exe

.PHONY: sign-darwin sign-windows
sign-darwin:  bin/darwin-arm64/${NAME}-signed
sign-windows: bin/windows-amd64/${NAME}-signed.exe

#
# BUILDING
#
bin/darwin-arm64/${NAME}: $(SRCS) ${GIT_STATE}
	@mkdir -p bin/darwin-arm64/
	@echo "Building darwin/arm64..."
	env GOOS=darwin GOARCH=arm64 go build -ldflags "-X '${BUILDINFO}.GitSha=${GIT_SHA}' -X '${BUILDINFO}.GitTag=${GIT_TAG}' -s -w" -o bin/darwin-arm64/${NAME}

bin/windows-amd64/${NAME}.exe: $(SRCS) ${GIT_STATE} rsrc_windows_amd64.syso
	@mkdir -p bin/windows-amd64/
	@echo "Building windows/amd64..."
	env GOOS=windows GOARCH=amd64 go build -ldflags "-X '${BUILDINFO}.GitSha=${GIT_SHA}' -X '${BUILDINFO}.GitTag=${GIT_TAG}' -s -w" -o bin/windows-amd64/${NAME}.exe

.PHONY: run-darwin
run-darwin: darwin
	@echo "Running darwin/arm64..."
	@./bin/darwin-arm64/${NAME}

#
# PACKAGING
#
package/darwin/$(APP_NAME).app: $(SRCS) Icon.png
	@mkdir -p package/darwin/
	@echo "Packaging for darwin..."
	@rm -rf package/darwin/$(APP_NAME).app
	fyne package -os darwin -name $(APP_NAME) -release -use-raw-icon
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
icon.ico: images/icon-fullsize.webp
	@mkdir -p tmp/
	magick images/icon-fullsize.webp -gravity center -crop 80%x80%+0-50 +repage -resize 16x16 -background none -alpha on -write tmp/icon-16px.png null:
	magick images/icon-fullsize.webp -gravity center -crop 80%x80%+0-50 +repage -resize 32x32 -background none -alpha on -write tmp/icon-32px.png null:
	magick images/icon-fullsize.webp -gravity center -crop 80%x80%+0-50 +repage -resize 48x48 -background none -alpha on -write tmp/icon-48px.png null:
	magick images/icon-fullsize.webp -gravity center -crop 85%x85%+0-40 +repage -resize 64x64 -background none -alpha on -write tmp/icon-64px.png null:
	magick images/icon-fullsize.webp -gravity center -crop 90%x90%+0-30 +repage -resize 128x128 -background none -alpha on -write tmp/icon-128px.png null:
	magick images/icon-fullsize.webp -resize 256x256 -write tmp/icon-256px.png null:
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
		@echo "WARNING: rsrc not found in PATH or at $(HOME)/go/bin/rsrc"; \
		@echo "WARNING: will not create an Windows app icon."; \
		@echo "WARNING: install with: go install github.com/akavel/rsrc@latest"; \
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
	rm -rf Icon.png
