# OpenArcanum build entry points. `make check` runs every check and test.
#
# `check` only verifies: it never rewrites sources, so formatting drift, stale
# generated code or an out-of-date lock fails it (locally and in CI). Use
# `make fmt` and `make proto` to fix what it reports.
#
# uv honours a caller-provided UV_PROJECT_ENVIRONMENT (the default is
# clients/desktop/.venv).

GO        ?= go
UV        ?= uv
BIN       := bin
PROTO_SRC := $(wildcard proto/openarcanum/v1/*.proto)
PY_WIRE   := clients/desktop/src/openarcanum/wire
GO_MODULE := github.com/hyperkubeorg/openarcanum

# The release version (VERSION; scripts/version, docs/release.md "Versions")
# and the commit, linked into every Go binary (internal/version.Release and
# .Revision; `relay version` prints them).
VERSION  := $(shell cat VERSION)
REVISION := $(shell git rev-parse --short=12 HEAD 2>/dev/null)
GO_VERSION_LDFLAGS := -X $(GO_MODULE)/internal/version.Release=$(VERSION) \
                      $(if $(REVISION),-X $(GO_MODULE)/internal/version.Revision=$(REVISION))
# Release builds: static, stripped, reproducible.
GO_RELEASE_LDFLAGS := -s -w -buildid= $(GO_VERSION_LDFLAGS)

# Python for the repo's stdlib-only scripts (scripts/*), from the client's uv
# environment so every OS gets the same interpreter.
PY := $(UV) run --project clients/desktop python

# Fuzzing budget per Go target for `make fuzz-short` (the nightly CI job raises
# it), and the Hypothesis profile for the Python fuzz tests (every decoder
# of untrusted input). The nightly profile runs each test far
# longer than pytest's per-test timeout, so it lifts that timeout; the tests
# bound each example themselves, so a hang still fails.
FUZZTIME           ?= 5s
FUZZMINIMIZETIME   ?= 2s
HYPOTHESIS_PROFILE ?= fuzz-short
PY_FUZZ_TESTS      := tests/test_fuzz_decoders.py tests/test_fuzz_client.py tests/test_fuzz_dm.py \
                      tests/test_session_fuzz.py tests/test_filecrypt.py
PY_FUZZ_TIMEOUT    := $(if $(filter nightly,$(HYPOTHESIS_PROFILE)),--timeout=0)

# The repo's Python scripts (no .py suffix) and the packaging scripts, linted
# with the client's ruff settings.
PY_SCRIPTS := $(addprefix ../../,scripts/check-markers scripts/public-check scripts/check-lock scripts/fuzz-go scripts/check-deploy scripts/check-release-bins scripts/version scripts/strings scripts/ui-icons scripts/dev-client) packaging ../web

# Headless GUI wrapper for anything that opens a window: Xvfb and a session
# bus on Linux; Windows and macOS (CI runners included) have a desktop.
ifeq ($(shell uname -s),Linux)
HEADLESS ?= dbus-run-session -- xvfb-run -a -s "-screen 0 1280x800x24"
else
HEADLESS ?=
endif

.PHONY: version version-check help all load kind-up kind-down kind-proxy kind-claim kind-status kind-partition kind-heal kind-failover client-image client fmt fmt-check lint markers public-check strings strings-check proto proto-check lock-check test test-go test-py \
        fuzz-short fuzz-go fuzz-py audit build client-sync client-smoke storage-guard \
        check clean clean-dev clean-images clean-all dev-up test-js bundle bundle-smoke appimage release-servers \
        deploy-check desktop desktop-appimage bundle-server bundle-licenses

.DEFAULT_GOAL := help

help: ## Show this list
	@echo "Usage: make TARGET   (docs: README.md, docs/kubernetes.md, docs/QA_GUIDE.md)"
	@echo
	@awk 'BEGIN {FS = ":.*## "} /^[a-z][a-z0-9-]*:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

all: build

# ---------------------------------------------------------------- developer fixes

fmt: ## Format Go and Python code (rewrites files)
	gofmt -w cmd internal
	cd clients/desktop && $(UV) run ruff format src tests scripts $(PY_SCRIPTS)

# gen-proto writes Go code under $(1)/internal/wire and Python code into $(2),
# using $(3) as scratch space.
define gen-proto
	@rm -rf $(3) && mkdir -p $(3) $(1)/internal/wire $(2)
	protoc -I proto --go_out=$(1) --go_opt=module=$(GO_MODULE) $(PROTO_SRC)
	protoc -I proto --python_out=$(3) --pyi_out=$(3) $(PROTO_SRC)
	@rm -f $(2)/*_pb2.py $(2)/*_pb2.pyi
	@for f in $(3)/openarcanum/v1/*_pb2.py $(3)/openarcanum/v1/*_pb2.pyi; do \
		sed -e 's/^from openarcanum\.v1 import /from openarcanum.wire import /' "$$f" > $(2)/$$(basename "$$f"); \
	done
	@printf '"""Generated from proto/ by `make proto`. Do not edit."""\n' > $(2)/__init__.py
	@rm -rf $(3)
endef

proto: ## Regenerate the protobuf code
	$(call gen-proto,.,$(PY_WIRE),.proto-tmp)

# ---------------------------------------------------------------- verification

fmt-check:
	@out="$$(gofmt -l cmd internal)"; if [ -n "$$out" ]; then \
		echo "gofmt: these files need formatting (run \`make fmt\`):"; echo "$$out"; exit 1; fi
	cd clients/desktop && $(UV) run ruff format --check src tests scripts $(PY_SCRIPTS)

# Regenerates into a scratch directory and compares with the committed code.
proto-check:
	@rm -rf .proto-check
	$(call gen-proto,.proto-check/go,.proto-check/py,.proto-check/tmp)
	@if diff -r .proto-check/go/internal/wire internal/wire \
		&& diff -r -x __pycache__ .proto-check/py $(PY_WIRE); then \
		rm -rf .proto-check; echo "proto-check: generated code is up to date"; \
	else \
		rm -rf .proto-check; \
		echo "proto-check: internal/wire or $(PY_WIRE) is stale or edited by hand; run \`make proto\` and commit the result"; \
		exit 1; \
	fi

lint: ## Go and Python lint
	golangci-lint run ./...
	golangci-lint run --build-tags load ./internal/loadtest/
	cd clients/desktop && $(UV) run ruff check src tests scripts $(PY_SCRIPTS)

markers:
	$(PY) scripts/check-markers

# The release version and its copies (scripts/version, docs/release.md).
version: ## Show the release version and check that every copy of it agrees
	@$(PY) scripts/version show
	@$(PY) scripts/version check

version-check:
	$(PY) scripts/version check

# What a commit of the current tree would publish: only the product
# (scripts/public-check; docs/release.md).
public-check: ## List what git would publish; fail on files or text that must stay private
	$(PY) scripts/public-check

# UI strings (docs/translations.md): `strings` rewrites the catalog
# clients/shared/ui/strings/en.json from the t()/_() calls;
# `strings-check` fails if it is stale, a variable doesn't match or a
# user-visible literal isn't wrapped.
strings: ## Regenerate the UI strings catalog
	$(PY) scripts/strings

strings-check:
	$(PY) scripts/strings --check

# The lock matches pyproject.toml, and every package is pinned with hashes.
lock-check:
	cd clients/desktop && $(UV) lock --check
	$(PY) scripts/check-lock

storage-guard:
	cd clients/desktop && $(UV) run python -m openarcanum.guard

# The relay, maintainer node (meta) and site are Linux server software; on
# Windows and macOS only the Go packages the desktop app ships (the community
# server it bundles and everything that imports) are tested and fuzzed.
ifeq ($(shell uname -s),Linux)
GO_TEST_PKGS ?= ./...
else
GO_TEST_PKGS ?= $(shell $(GO) list -deps -f '{{.ImportPath}}' ./cmd/community | sed -n 's|^$(GO_MODULE)/|./|p')
endif

test: test-go test-py test-js ## Go, Python and JS tests
test-go:
	$(GO) test -race -count=1 $(GO_TEST_PKGS)

# Python tests include Go<->Python interop, end-to-end flows against the real
# server binaries and the UI test, so they need `build` first.
test-py: build
	cd clients/desktop && OPENARCANUM_BIN=$(CURDIR)/$(BIN) $(UV) run pytest -q
# The same tests in three parts, so CI can run them side by side: the ones
# that open no window (`gui` marker absent), the UI flows, and the other
# windowed tests (calls, the engine probe, the tray, the keychain).
# TEST_K narrows them further (pytest -k terms; CI leaves out the 25- and
# 50-person calls, which need more cores than a hosted runner has).
TEST_K ?=
K_AND = $(if $(TEST_K), and ($(TEST_K)),)
test-py-core: build
	cd clients/desktop && OPENARCANUM_BIN=$(CURDIR)/$(BIN) $(UV) run pytest -q -m "not gui" $(if $(TEST_K),-k "$(TEST_K)",)
test-ui: build
	cd clients/desktop && OPENARCANUM_BIN=$(CURDIR)/$(BIN) $(UV) run pytest -q -m gui -k "test_ui_$(K_AND)"
test-media: build
	cd clients/desktop && OPENARCANUM_BIN=$(CURDIR)/$(BIN) $(UV) run pytest -q -m gui -k "not test_ui_$(K_AND)"

# The UI's JavaScript unit tests (Node's built-in runner; no npm packages).
NODE ?= node
test-js:
	$(NODE) --test 'clients/desktop/tests/js/*.test.js' 'clients/desktop/tests/js/*.test.mjs'

# Bounded fuzzing: every Go native fuzz target for FUZZTIME each, plus the
# Hypothesis decoder tests. Crashing Go inputs land in testdata/fuzz/.
fuzz-short: fuzz-go fuzz-py

fuzz-go:
	$(PY) scripts/fuzz-go --go "$(GO)" --fuzztime $(FUZZTIME) --minimizetime $(FUZZMINIMIZETIME) \
		$(if $(filter-out ./...,$(GO_TEST_PKGS)),--packages "$(GO_TEST_PKGS)")

fuzz-py:
	cd clients/desktop && HYPOTHESIS_PROFILE=$(HYPOTHESIS_PROFILE) $(UV) run pytest -q $(PY_FUZZ_TIMEOUT) $(PY_FUZZ_TESTS)

# Known-vulnerability scans (needs network access to the vulnerability DBs).
audit:
	govulncheck ./...
	@mkdir -p .audit
	cd clients/desktop && $(UV) export --quiet --frozen --format requirements-txt --no-emit-project -o ../../.audit/requirements.txt
	cd clients/desktop && $(UV) run pip-audit -r ../../.audit/requirements.txt --require-hashes --disable-pip --progress-spinner off
	@rm -rf .audit

# ---------------------------------------------------------------- build & run

build: ## Build the server binaries into bin/
	@mkdir -p $(BIN)
	$(GO) build -ldflags "$(GO_VERSION_LDFLAGS)" -o $(BIN)/ ./cmd/... ./internal/testutil/cmd/...

# ---------------------------------------------------------------- server releases
#
# docs/admin-guide.md "Installing the servers". Static binaries (CGO_ENABLED=0;
# SQLite is modernc.org/sqlite, pure Go) for linux/amd64 and linux/arm64
# (Raspberry Pi), in $(RELEASE_DIR)/linux-<arch>/. check-release-bins checks
# each is a static ELF for its machine and runs relay, meta and community init
# natively and, for arm64, under qemu-user when it's installed (on PATH, or
# unpacked without root under ~/.local/sysroot/usr/bin). The OCI images
# (deploy/container/Containerfile) are built in CI (job "servers");
# deploy-check validates them and the systemd units.

RELEASE_DIR   ?= dist/servers
SERVER_ARCHES ?= amd64 arm64
QEMU_AARCH64  ?= $(firstword $(shell command -v qemu-aarch64-static qemu-aarch64 2>/dev/null) \
                   $(wildcard $(HOME)/.local/sysroot/usr/bin/qemu-aarch64-static))

release-servers: ## Static linux/amd64 and linux/arm64 server binaries
	@for arch in $(SERVER_ARCHES); do \
		echo "go build linux/$$arch -> $(RELEASE_DIR)/linux-$$arch/"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "$(GO_RELEASE_LDFLAGS)" \
			-o $(RELEASE_DIR)/linux-$$arch/ ./cmd/relay ./cmd/community ./cmd/meta || exit 1; \
	done
	$(PY) scripts/check-release-bins --version $(VERSION) $(if $(QEMU_AARCH64),--qemu-aarch64 $(QEMU_AARCH64)) $(RELEASE_DIR)

deploy-check: build
	$(PY) scripts/check-deploy --bin $(BIN)

client-sync: ## Install the client's Python environment (uv sync)
	cd clients/desktop && $(UV) sync --locked

client-smoke:
	cd clients/desktop && $(HEADLESS) $(UV) run python -m openarcanum --smoke --home "$$(mktemp -d)"

# ---------------------------------------------------------------- desktop bundles
#
# docs/packaging.md. `bundle` is the PyInstaller onedir app (one spec for every
# OS: clients/desktop/packaging/openarcanum.spec) in clients/desktop/dist/; `bundle-smoke` starts
# what was built with --smoke (tests/test_bundle.py); `appimage` packs the
# Linux bundle into clients/desktop/dist/OpenArcanum-x86_64.AppImage with pinned,
# hash-checked AppImage tools. The Windows installer and the macOS .dmg are
# built in CI (.github/workflows/release.yml).

# The community server goes inside the bundle (servers/), for hosting
# communities on this computer: static and stripped, for the OS and
# architecture the bundle is built on.
bundle-server:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(GO_RELEASE_LDFLAGS)" -o clients/desktop/build/servers/ ./cmd/community

# The licences that ship in the bundle (licenses/ in it; THIRD_PARTY_NOTICES.md):
# the project's, and every bundled Python distribution's and Go module's.
bundle-licenses: bundle-server
	@mkdir -p clients/desktop/build
	cd clients/desktop && $(UV) export --quiet --frozen --no-dev --no-emit-project --no-hashes \
		--format requirements-txt -o build/requirements-runtime.txt
	cd clients/desktop && $(UV) run python packaging/licenses.py build/licenses \
		--requirements build/requirements-runtime.txt --go "$(GO)"

bundle: client-sync bundle-server bundle-licenses ## Build the desktop app bundle (PyInstaller)
	cd clients/desktop && $(UV) run python packaging/icons.py build/icons
	cd clients/desktop && $(UV) run python -m PyInstaller --noconfirm --clean --log-level WARN \
		--distpath dist --workpath build/pyinstaller packaging/openarcanum.spec

bundle-smoke:
	cd clients/desktop && $(UV) run pytest -q tests/test_bundle.py

appimage: bundle ## Build the Linux AppImage
	cd clients/desktop && $(UV) run python packaging/linux/build_appimage.py \
		--bundle dist/OpenArcanum --out dist/OpenArcanum-x86_64.AppImage --tools build/appimage-tools

check: check-go check-py check-ui check-media ## The full gate: every check and test (CI runs the four parts side by side)
check-go: client-sync fmt-check proto-check lint markers version-check strings-check lock-check storage-guard deploy-check test-go fuzz-go ## Static checks, the Go tests and fuzzing
check-py: build test-py-core test-js fuzz-py client-smoke ## The Python tests that open no window, the JavaScript tests, Python fuzzing, the smoke launch
check-ui: build test-ui ## The UI tests (two app windows each)
check-media: build test-media ## The call, engine and other windowed tests outside test_ui_*
# Load tests (docs/load-tests.md): each server binary pinned to
# LOAD_SERVER_CPUS (default 0,1: the 2-vCPU target), the community test's relay on
# LOAD_RELAY_CPUS (2,3), clients on LOAD_CLIENT_CPUS.
# Results are appended to LOAD_RESULTS as JSON lines.
LOAD_CLIENT_CPUS ?= 4-25
LOAD_RESULTS ?= load-results.jsonl
load: build ## Load tests (docs/load-tests.md)
	LOAD_RESULTS=$(abspath $(LOAD_RESULTS)) taskset -c $(LOAD_CLIENT_CPUS) \
		go test -tags load -count=1 -timeout 3h -v -run '$(or $(LOAD_RUN),.)' ./internal/loadtest/

# Local Kubernetes stack on kind (docs/kubernetes.md). Run these on your
# computer (not inside a container): they need rootless podman, kind,
# kubectl and helm. Clients: scripts/dev-client NAME.
kind-up: ## Local Kubernetes stack on kind: cluster, images, chart (on the host)
	scripts/kind.sh up

kind-down: ## Delete the kind cluster and everything in it
	scripts/kind.sh down

kind-proxy: ## Forward the relay and maintainer node to 127.0.0.1 (leave running)
	scripts/kind.sh proxy

kind-claim: ## Print the kind community server's claim code
	scripts/kind.sh claim

kind-status: ## Pods, services and volumes in the kind cluster
	scripts/kind.sh status

# With KIND_META_CLUSTER=1 make kind-up: two maintainer nodes in one cluster
# (docs/kubernetes.md "A maintainer cluster").
kind-partition: ## Cut the kind maintainer cluster's two nodes off from each other
	scripts/kind.sh partition

kind-heal: ## End the partition and start the root node again
	scripts/kind.sh heal

kind-failover: ## Stop the kind maintainer cluster's root node
	scripts/kind.sh failover

# The desktop app in a container, for development copies against the kind
# stack (docs/kubernetes.md "The app copies"). client-image again after
# clients/desktop/uv.lock, clients/desktop/.python-version or the Containerfile change; code changes need no rebuild.
CLIENT_IMAGE ?= localhost/openarcanum-client:dev
client-image: ## Build the app image for development copies
	podman build -f deploy/container/Containerfile.client \
		--ignorefile deploy/container/Containerfile.client.dockerignore \
		--label openarcanum.inputs=$$(scripts/dev-client --inputs-hash) \
		-t $(CLIENT_IMAGE) .

# make client NAME=alice [ARGS="--maintainer URL --fake-media --no-media"]
client: ## Run a development copy of the app: make client NAME=alice
	@test -n "$(NAME)" || { echo "usage: make client NAME=alice"; exit 2; }
	OPENARCANUM_CLIENT_IMAGE=$(CLIENT_IMAGE) scripts/dev-client $(ARGS) $(NAME)

# The app run natively on this computer, no container (docs/kubernetes.md
# "The app on your desktop"): from clients/desktop/src with uv, or the AppImage. Its
# data is in .dev/desktop/NAME (make clean-dev removes it). ARGS go to the
# app; MAINTAINER is the maintainer node; FAKE_MEDIA=1 uses Chromium's fake
# camera and microphone; REBUILD=1 rebuilds the AppImage.
#   make desktop [NAME=me] [ARGS="--debug"] [MAINTAINER=URL] [FAKE_MEDIA=1]
DESKTOP_NAME = $(or $(NAME),me)
MAINTAINER  ?= http://127.0.0.1:7100
DESKTOP_OPTS = --maintainer $(MAINTAINER) $(if $(FAKE_MEDIA),--fake-media)
# Through a variable: a recipe line naming $(MAKE) itself would run even
# under `make -n`.
DESKTOP_MAKE := $(MAKE)
desktop: ## Run the app natively from source (uv): make desktop [NAME=me]
	UV=$(UV) scripts/desktop.sh $(DESKTOP_OPTS) $(DESKTOP_NAME) -- $(ARGS)

desktop-appimage: ## Build (or reuse) the AppImage and run it: make desktop-appimage [NAME=me]
	UV=$(UV) MAKE=$(DESKTOP_MAKE) scripts/desktop.sh --appimage $(if $(REBUILD),--rebuild) $(DESKTOP_OPTS) $(DESKTOP_NAME) -- $(ARGS)

dev-up: build ## Local relay, maintainer node and community server without Kubernetes
	scripts/dev-up.sh

# ---------------------------------------------------------------- cleaning
#
# scripts/clean.sh removes only paths inside the project and prints each one;
# every target is safe to run again. FORCE=1 skips clean-dev's question and
# stops running app copies (clean-dev, clean-images) first.

CLEAN_PATHS = $(BIN) dist .proto-tmp .proto-check .audit clients/desktop/build clients/desktop/dist \
              $(LOAD_RESULTS) .pytest_cache .ruff_cache .hypothesis \
              clients/desktop/.pytest_cache clients/desktop/.ruff_cache clients/desktop/.hypothesis
clean: ## Remove build output and caches (bin/, dist/, clients/desktop/build, clients/desktop/dist…)
	@scripts/clean.sh paths $(CLEAN_PATHS) \
		$$(find clients -path clients/desktop/.venv -prune -o -type d -name __pycache__ -prune -print 2>/dev/null)

clean-dev: ## Remove .dev/: app copies, desktop profiles, dev-up data (asks; FORCE=1)
	@FORCE=$(FORCE) scripts/clean.sh dev

clean-images: ## Remove the podman images this project built (localhost/openarcanum-*)
	@FORCE=$(FORCE) scripts/clean.sh images $(CLIENT_IMAGE)

clean-all: ## kind-down, clean-dev, clean-images, clean, and clients/desktop/.venv
	@if command -v kind >/dev/null; then scripts/kind.sh down; \
	else echo "clean: kind isn't installed here, so there's no cluster to delete"; fi
	@$(MAKE) --no-print-directory clean-dev clean-images clean
	@scripts/clean.sh paths clients/desktop/.venv
