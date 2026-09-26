# Symphony — build orchestration.
#
# The default build runs inside a Docker container (src/build/docker), so the
# host needs only Docker and make: no Go, Node, gcc or GTK/WebKit headers.
#
#   make                 -> make build
#   make build           -> build in the container, place a runnable binary at ./bin/symphony
#   make image           -> (re)build the builder image
#   make shell           -> interactive shell in the builder container
#   make run             -> build then launch ./bin/symphony
#   make clean           -> remove build outputs
#   make clean-docker    -> also remove the builder image and cache volumes
#   make desktop         -> register bin/symphony.desktop with the desktop, so the
#                           app launches from the app grid with no terminal
#   make blocks          -> build the five block images (symphony/<block>:dev)
#   make test            -> Go tests (blocks contract test runs the blocks as processes)
#   make integration     -> engine tests against the host's Docker daemon
#   make tag vX.Y.Z      -> tag HEAD and push the tag; CI builds and publishes
#                           the release (.github/workflows/release.yml)
#
# Native targets (need a local Go/Node/Wails toolchain, see README):
#   make build-local     -> wails build on the host
#   make dev             -> wails dev with hot reload (needs a display, so native only)
#   make deps            -> go mod tidy + npm install (+ wails CLI if missing)
#   make bindings        -> regenerate src/client/wailsjs from the Go API
#   make check           -> go vet + client typecheck (no tests, by design)
#
# Variables:
#   PLATFORM=linux/amd64   wails target; windows/amd64 cross-compiles from the
#                          container too (output bin/symphony.exe). darwin must
#                          be built natively on a Mac with `make build-local`.

.DEFAULT_GOAL := build
SHELL := /bin/bash

SRC      := src
CLIENT   := $(SRC)/client
APP      := symphony
PLATFORM ?= linux/amd64

# The output must not be called "install": shared-mime-info files that name
# as text/x-install (an INSTALL readme), and file managers then open the
# binary as an executable text file, in an editor or a terminal.
ifeq ($(findstring windows,$(PLATFORM)),windows)
  BIN := $(APP).exe
else
  BIN := $(APP)
endif
OUT := bin/$(BIN)

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
  -e HOST_UID=$(HOST_UID) -e HOST_GID=$(HOST_GID) -e PLATFORM=$(PLATFORM)

.PHONY: build image shell run clean clean-docker build-local dev deps bindings check desktop blocks test integration tag

# --- block images ------------------------------------------------------------

BLOCKS := gateway service broker database proxy

# Same Dockerfile and context the engine uses for its in-app build, so both
# paths produce the same image.
blocks:
	for b in $(BLOCKS); do \
	  $(DOCKER) build -f $(SRC)/blocks/Dockerfile --build-arg BLOCK=$$b -t symphony/$$b:dev $(SRC) || exit 1; \
	done
	$(DOCKER) images "symphony/*" --format "table {{.Repository}}\t{{.Tag}}\t{{.Size}}"

test: image
	$(DOCKER_RUN) $(IMAGE) go test ./...

# Runs inside the builder container with the host's Docker socket and network,
# so the engine can create real containers and reach their admin ports.
integration: image
	$(DOCKER_RUN) --network host -v /var/run/docker.sock:/var/run/docker.sock $(IMAGE) \
	  go test -tags integration -count=1 -run Integration ./engine/... -v

build: image
	$(DOCKER_RUN) $(IMAGE)
	$(MAKE) --no-print-directory install-bin

image:
	$(DOCKER) build -t $(IMAGE) $(SRC)/build/docker

shell: image
	$(DOCKER_RUN) -it $(IMAGE) bash

run: build
	./$(OUT)

clean:
	rm -rf bin $(SRC)/build/bin $(CLIENT)/dist
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

build-local:
	cd $(SRC) && $(WAILS) build $(WAILS_TAGS)
	$(MAKE) --no-print-directory install-bin

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
	cd $(SRC) && go vet ./...
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

# --- shared: copy the wails output to ./bin ----------------------------------

# A Linux build also writes a .desktop entry next to the binary, so `make
# desktop` can put Symphony in the app grid. The binary is a plain GUI program
# and never needs a terminal either way.
DESKTOP := bin/$(APP).desktop

.PHONY: install-bin
install-bin:
	mkdir -p bin
ifeq ($(UNAME_S),Darwin)
	rm -rf bin/$(APP).app
	cp -R $(SRC)/build/bin/$(APP).app bin/$(APP).app
	printf '#!/bin/sh\nexec "$$(dirname "$$0")/$(APP).app/Contents/MacOS/$(APP)" "$$@"\n' > $(OUT)
else
	cp $(SRC)/build/bin/$(BIN) $(OUT)
endif
	chmod +x $(OUT)
ifeq ($(UNAME_S)$(findstring linux,$(PLATFORM)),Linuxlinux)
	printf '[Desktop Entry]\nType=Application\nName=Symphony\nComment=Orchestration vs. choreography fault-injection benchmark\nExec=%s\nIcon=%s\nTerminal=false\nCategories=Development;\nStartupWMClass=$(APP)\n' \
	  "$(CURDIR)/$(OUT)" "$(CURDIR)/$(SRC)/build/appicon.png" > $(DESKTOP)
	chmod +x $(DESKTOP)
endif
	@echo "built $(OUT)"

# Registers the launcher for the current user (GNOME, KDE, XFCE all read
# ~/.local/share/applications). Run again after moving the repository.
desktop: $(DESKTOP)
	mkdir -p $(HOME)/.local/share/applications
	cp $(DESKTOP) $(HOME)/.local/share/applications/$(APP).desktop
	-update-desktop-database $(HOME)/.local/share/applications 2>/dev/null
	@echo "installed $(HOME)/.local/share/applications/$(APP).desktop"

$(DESKTOP):
	$(MAKE) --no-print-directory install-bin
