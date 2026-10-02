# Symphony — build orchestration.
#
# The default build runs inside a Docker container (src/build/docker), so the
# host needs only Docker and make: no Go, Node, gcc or GTK/WebKit headers.
#
#   make                 -> make build
#   make build           -> build in the container, for Linux and Windows:
#                             bin/linux/symphony, bin/linux/symphony-cli
#                             bin/windows/symphony.exe, bin/windows/symphony-cli.exe
#                           (symphony is the window, symphony-cli the same app
#                           from a terminal)
#   make cli             -> build only the symphony-cli binaries
#   make dist VERSION=vX.Y.Z
#                        -> zip bin/ into dist/: one archive per platform, plus
#                           SHA256SUMS (what a release publishes)
#   make image           -> (re)build the builder image
#   make shell           -> interactive shell in the builder container
#   make run             -> build for Linux, then launch bin/linux/symphony
#   make clean           -> remove build outputs
#   make clean-docker    -> also remove the builder image and cache volumes
#   make desktop         -> register bin/linux/symphony.desktop with the desktop, so
#                           the app launches from the app grid with no terminal
#   make blocks          -> build the five block images (symphony/<block>:dev)
#   make test            -> Go tests (blocks contract test runs the blocks as processes)
#                           and the client's unit tests
#   make lint            -> gofmt, go vet (window and CLI builds) and the client
#                           typecheck, in the container
#   make integration     -> engine tests against the host's Docker daemon
#   make docs            -> regenerate docs/CLI.md, the CLI's guide (what
#                           `symphony-cli generate-guide` writes)
#   make tag vX.Y.Z      -> tag HEAD and push the tag; CI builds and publishes
#                           the release (.github/workflows/release.yml)
#
# Native targets (need a local Go/Node/Wails toolchain, see README):
#   make build-local     -> wails build on the host, into bin/<host os>/
#   make dev             -> wails dev with hot reload (needs a display, so native only)
#   make deps            -> go mod tidy + npm install (+ wails CLI if missing)
#   make bindings        -> regenerate src/client/wailsjs from the Go API
#   make check           -> go vet + client typecheck (no tests, by design)
#
# Variables:
#   PLATFORM=linux/amd64   build that one platform only (default: linux/amd64
#                          and windows/amd64, both from the container). darwin
#                          must be built natively on a Mac with `make build-local`.

.DEFAULT_GOAL := build
SHELL := /bin/bash

SRC      := src
CLIENT   := $(SRC)/client
APP      := symphony
PLATFORMS := $(or $(PLATFORM),linux/amd64 windows/amd64)

# The per-platform names below are for install-bin, which runs once per
# platform with PLATFORM set. Each platform gets its own directory,
# bin/<os>/, with the window and the CLI side by side (they share the state
# database next to them).
#
# The output must not be called "install": shared-mime-info files that name
# as text/x-install (an INSTALL readme), and file managers then open the
# binary as an executable text file, in an editor or a terminal.
OS := $(firstword $(subst /, ,$(PLATFORM)))
ifeq ($(OS),windows)
  EXE := .exe
endif
BIN    := $(APP)$(EXE)
OUTDIR := bin/$(OS)
OUT    := $(OUTDIR)/$(BIN)
# symphony-cli: the same package built with -tags cli (src/main_cli.go)
CLI := $(APP)-cli$(EXE)

# --- docker build ------------------------------------------------------------

DOCKER   ?= docker
IMAGE    ?= symphony-builder
HOST_UID := $(shell id -u)
HOST_GID := $(shell id -g)

# Named volumes keep Go modules, the Go build cache, the npm cache and the
# container's own node_modules between runs.
DOCKER_RUN = $(DOCKER) run --rm \
  -v "$(CURDIR)/$(SRC):/work" -w /work \
  -v $(IMAGE)-go-cache:/cache/go \
  -v $(IMAGE)-npm-cache:/cache/npm \
  -v $(IMAGE)-node-modules:/work/client/node_modules \
  -e HOST_UID=$(HOST_UID) -e HOST_GID=$(HOST_GID)

.PHONY: build cli dist image shell run clean clean-docker build-local dev deps bindings check desktop blocks test lint integration docs tag

# --- block images ------------------------------------------------------------

BLOCKS := gateway service broker database proxy

# Same Dockerfile and context the engine uses for its in-app build, so both
# paths produce the same image.
blocks:
	for b in $(BLOCKS); do \
	  $(DOCKER) build -f $(SRC)/blocks/Dockerfile --build-arg BLOCK=$$b -t symphony/$$b:dev $(SRC) || exit 1; \
	done
	$(DOCKER) images "symphony/*" --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}"

# The window embeds client/dist, which only a frontend build creates; on a
# fresh checkout go vet and go test need it to exist.
$(CLIENT)/dist:
	mkdir -p $@ && touch $@/.gitkeep

test: image $(CLIENT)/dist
	$(DOCKER_RUN) $(IMAGE) bash -c '\
	  go test ./... && \
	  cd client && { [ -x node_modules/.bin/vitest ] || npm ci; } && npm test'

# What `make check` does natively, in the container, plus gofmt. The client's
# node_modules volume is filled on first use.
lint: image $(CLIENT)/dist
	$(DOCKER_RUN) $(IMAGE) bash -c '\
	  fmt=$$(gofmt -l blocks engine cmd *.go) && [ -z "$$fmt" ] || { echo "gofmt needed:"; echo "$$fmt"; exit 1; }; \
	  go vet ./... && go vet -tags cli . && \
	  cd client && { [ -d node_modules/.bin ] || npm ci; } && npm run typecheck'
	@$(GUIDE) | cmp -s - docs/CLI.md || \
	  { echo "docs/CLI.md is not what symphony-cli generate-guide writes: run make docs"; exit 1; }

# docs/CLI.md is the guide the CLI generates, checked in so it can be read
# without the binary. It is written from the command table in src/engine/cli.
GUIDE = $(DOCKER_RUN) $(IMAGE) go run -tags cli . generate-guide --out -

docs: image
	$(GUIDE) > docs/CLI.md.tmp && mv docs/CLI.md.tmp docs/CLI.md

# Runs inside the builder container with the host's Docker socket and network,
# so the engine can create real containers and reach their admin ports.
integration: image
	$(DOCKER_RUN) --network host -v /var/run/docker.sock:/var/run/docker.sock $(IMAGE) \
	  go test -tags integration -count=1 -run Integration ./engine/... -v

build: image
	for p in $(PLATFORMS); do \
	  $(DOCKER_RUN) -e PLATFORM=$$p $(IMAGE) && \
	  $(MAKE) --no-print-directory install-bin PLATFORM=$$p || exit 1; \
	done

cli: image
	for p in $(PLATFORMS); do \
	  $(DOCKER_RUN) -e PLATFORM=$$p -e ONLY=cli $(IMAGE) && \
	  $(MAKE) --no-print-directory install-cli PLATFORM=$$p || exit 1; \
	done

# The release archives, from what `make build` left in bin/.
dist:
	@test -n "$(VERSION)" || { echo "usage: make dist VERSION=vX.Y.Z"; exit 1; }
	python3 .github/scripts/package-release.py "$(VERSION)" bin dist

image:
	$(DOCKER) build -t $(IMAGE) $(SRC)/build/docker

shell: image
	$(DOCKER_RUN) -it $(IMAGE) bash

run:
	$(MAKE) --no-print-directory build PLATFORM=linux/amd64
	./bin/linux/$(APP)

clean:
	rm -rf bin dist $(SRC)/build/bin $(CLIENT)/dist
	mkdir -p $(CLIENT)/dist && touch $(CLIENT)/dist/.gitkeep

clean-docker: clean
	-$(DOCKER) volume rm $(IMAGE)-go-cache $(IMAGE)-npm-cache $(IMAGE)-node-modules
	-$(DOCKER) rmi $(IMAGE)

# --- native build ------------------------------------------------------------

GOBIN     ?= $(shell go env GOPATH 2>/dev/null)/bin
WAILS     ?= $(shell command -v wails 2>/dev/null || echo $(GOBIN)/wails)
WAILS_VER ?= v2.16.0
UNAME_S   := $(shell uname -s)

# Modern Debian/Ubuntu only ship webkit2gtk-4.1; Wails links against 4.0
# unless told otherwise.
WAILS_TAGS :=
ifeq ($(UNAME_S),Linux)
  ifeq ($(shell pkg-config --exists webkit2gtk-4.0 2>/dev/null && echo yes),)
    WAILS_TAGS := -tags webkit2_41
  endif
endif

LOCAL_PLATFORM = $(shell cd $(SRC) && go env GOOS)/$(shell cd $(SRC) && go env GOARCH)
LOCAL_EXE      = $(if $(filter windows/%,$(LOCAL_PLATFORM)),.exe)

build-local:
	cd $(SRC) && $(WAILS) build $(WAILS_TAGS)
	cd $(SRC) && CGO_ENABLED=0 go build -tags cli -trimpath -ldflags "-s -w" -o build/bin/$(APP)-cli$(LOCAL_EXE) .
	$(MAKE) --no-print-directory install-bin PLATFORM=$(LOCAL_PLATFORM)

dev:
	cd $(SRC) && $(WAILS) dev $(WAILS_TAGS)

deps:
	cd $(SRC) && go mod tidy
	cd $(CLIENT) && npm install
	@command -v wails >/dev/null 2>&1 || [ -x $(GOBIN)/wails ] || \
	  go install github.com/wailsapp/wails/v2/cmd/wails@$(WAILS_VER)

bindings:
	cd $(SRC) && $(WAILS) generate module

check:
	cd $(SRC) && go vet $(WAILS_TAGS) ./... && go vet -tags cli .
	cd $(CLIENT) && npm run typecheck

# --- release -----------------------------------------------------------------

# `make tag v1.2.3`: the version is the second goal on the command line, so it
# is read from there and given a no-op rule of its own.
ifeq (tag,$(firstword $(MAKECMDGOALS)))
  VERSION := $(word 2,$(MAKECMDGOALS))
  ifneq ($(VERSION),)
    $(eval $(VERSION):;@:)
  endif
endif

# Tags HEAD and pushes only the tag; the push starts the release workflow.
# HEAD must already be on the remote, so the release is built from a commit
# the branch history keeps.
tag:
	@echo "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$$' || \
	  { echo "usage: make tag vX.Y.Z (got '$(VERSION)')"; exit 1; }
	@git diff --quiet && git diff --cached --quiet || \
	  { echo "uncommitted changes: commit them first, the tag would not include them"; exit 1; }
	@git fetch --quiet --tags origin
	@! git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null || \
	  { echo "tag $(VERSION) already exists"; exit 1; }
	@test -n "$$(git branch -r --contains HEAD)" || \
	  { echo "HEAD is not on origin yet: push the branch first"; exit 1; }
	git tag -a "$(VERSION)" -m "$(VERSION)"
	git push origin "$(VERSION)"

# --- shared: copy the build output to bin/<os>/ ------------------------------

# A Linux build also writes a .desktop entry next to the binary, so `make
# desktop` can put Symphony in the app grid. The binary is a plain GUI program
# and never needs a terminal either way.
DESKTOP := bin/linux/$(APP).desktop

.PHONY: install-bin install-cli
install-bin: install-cli
ifeq ($(OS),darwin)
	rm -rf $(OUTDIR)/$(APP).app
	cp -R $(SRC)/build/bin/$(APP).app $(OUTDIR)/$(APP).app
	printf '#!/bin/sh\nexec "$$(dirname "$$0")/$(APP).app/Contents/MacOS/$(APP)" "$$@"\n' > $(OUT)
else
	cp $(SRC)/build/bin/$(BIN) $(OUT)
endif
	chmod +x $(OUT)
ifeq ($(OS),linux)
	printf '[Desktop Entry]\nType=Application\nName=Symphony\nComment=Orchestration vs. choreography fault-injection benchmark\nExec=%s\nIcon=%s\nTerminal=false\nCategories=Development;\nStartupWMClass=$(APP)\n' \
	  "$(CURDIR)/$(OUT)" "$(CURDIR)/$(SRC)/build/appicon.png" > $(DESKTOP)
	chmod +x $(DESKTOP)
endif
	@echo "built $(OUT) and $(OUTDIR)/$(CLI)"

install-cli:
	mkdir -p $(OUTDIR)
	cp $(SRC)/build/bin/$(CLI) $(OUTDIR)/$(CLI)
	chmod +x $(OUTDIR)/$(CLI)

# Registers the launcher for the current user (GNOME, KDE, XFCE all read
# ~/.local/share/applications). Run again after moving the repository.
desktop: $(DESKTOP)
	mkdir -p $(HOME)/.local/share/applications
	cp $(DESKTOP) $(HOME)/.local/share/applications/$(APP).desktop
	-update-desktop-database $(HOME)/.local/share/applications 2>/dev/null
	@echo "installed $(HOME)/.local/share/applications/$(APP).desktop"

$(DESKTOP):
	$(MAKE) --no-print-directory install-bin PLATFORM=linux/amd64
