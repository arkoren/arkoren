# Shared contributor and CI commands. Compatible with GNU Make 3.81.
# MODULE is a checkout-relative module root, PKG a module-relative Go pattern,
# and RUN an optional Go test regexp. Example: make test MODULE=core PKG=./http/...
.DEFAULT_GOAL := help
SHELL := /bin/sh
export GOWORK := off
MODULE ?=
PKG ?= ./...
RUN ?=
# Preserve regex dollars and quote apostrophes as data through Make and sh.
shellquote = '$(subst ','"'"',$(1))'
run_flags = $(if $(value RUN),-run $(call shellquote,$(value RUN)))

.PHONY: help module-root module compiler test lint check race integration services cleanup docs generate workflow-check release-preview-check vet
help:
	@printf '%s\n' 'make test MODULE=core [PKG=./...] [RUN=regexp]  cached short tests with default vet' 'make check MODULE=core                       lint and cached short race tests' 'make integration MODULE=driver/redis          unique project, fresh race tests, cleanup' 'make services MODULE=driver/redis             start arkoren-manual-driver-redis' 'make cleanup MODULE=driver/redis              remove manual project and volumes' 'make docs                                     Astro check, build, and website tests' 'make generate                                 update guide index (website Node dependencies required)' 'make workflow-check                           shell fixtures and actionlint' 'make vet MODULE=core                          standalone toolchain vet backstop' 'Integrations use -count=1; ordinary module tests retain caching.' 'COMPOSE_PROJECT_NAME overrides integration and manual project defaults.'

# module-root rejects aliases, traversal, package paths and missing manifests
# without loading Go packages. Cleanup uses it even if the toolchain is broken.
# Example: make module-root MODULE=driver/redis
module-root:
	@printf '%s\n' '$(MODULE)' | grep -Eq '^(core|cmd/arkoren|(driver|foundation|ark)/[a-z0-9][a-z0-9-]*)$$' || { echo 'MODULE must name a canonical checkout-relative module root' >&2; exit 2; }
	@test -f '$(MODULE)/go.mod' || { echo 'MODULE has no go.mod' >&2; exit 2; }

# Package validation needs Go; teardown deliberately uses only module-root.
module: module-root
	@case '$(PKG)' in .|./*) ;; *) echo 'PKG must be module-relative (. or ./...)' >&2; exit 2;; esac
	@case '$(PKG)' in ../*|*/../*|*/..|*' '*) echo 'PKG must stay inside MODULE' >&2; exit 2;; esac
	@cd '$(MODULE)' && packages=$$(go list '$(PKG)') && test -n "$$packages" || { echo 'PKG did not resolve to packages' >&2; exit 2; }

# Race instrumentation needs a working C compiler even when local CGO is off.
compiler:
	@command -v "$$(go env CC | cut -d ' ' -f 1)" >/dev/null || { echo 'Race tests require a C compiler (go env CC)' >&2; exit 2; }

test: module
	@cd '$(MODULE)' && go test -short $(run_flags) '$(PKG)'

lint: module
	@cd '$(MODULE)' && golangci-lint run --allow-parallel-runners '$(PKG)'

check: lint race

race: module compiler
	@cd '$(MODULE)' && CGO_ENABLED=1 go test -vet=off -short -race $(run_flags) '$(PKG)'

# Preserve the primary test/signal status when teardown also fails. A teardown
# failure becomes the exit status only when all preceding integration work passed.
integration: module compiler
	@test -f '$(MODULE)/compose.yaml' || { echo 'integration requires MODULE/compose.yaml' >&2; exit 2; }
	@set -eu; cd '$(MODULE)'; \
	: "$${COMPOSE_PROJECT_NAME:=arkoren-integration-$$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')}"; export COMPOSE_PROJECT_NAME; \
	printf 'Integration Compose project: %s\n' "$$COMPOSE_PROJECT_NAME"; \
	trap 'exit 130' INT; trap 'exit 143' TERM; \
	trap 'status=$$?; if [ $$status -ne 0 ]; then docker compose logs --no-color --tail 200 || true; fi; cleanup=0; docker compose down --volumes --remove-orphans || cleanup=$$?; if [ $$cleanup -ne 0 ]; then echo "Compose cleanup failed ($$cleanup)" >&2; fi; if [ $$status -ne 0 ]; then exit $$status; fi; exit $$cleanup' EXIT; \
	docker compose up -d --wait --wait-timeout 180; \
	CGO_ENABLED=1 go test -vet=off -race -count=1 $(run_flags) '$(PKG)'

services: module
	@test -f '$(MODULE)/compose.yaml' || { echo 'services requires MODULE/compose.yaml' >&2; exit 2; }
	@cd '$(MODULE)' && COMPOSE_PROJECT_NAME="$${COMPOSE_PROJECT_NAME:-arkoren-manual-$(subst /,-,$(MODULE))}" docker compose up -d --wait --wait-timeout 180

cleanup: module-root
	@test -f '$(MODULE)/compose.yaml' || { echo 'cleanup requires MODULE/compose.yaml' >&2; exit 2; }
	@cd '$(MODULE)' && COMPOSE_PROJECT_NAME="$${COMPOSE_PROJECT_NAME:-arkoren-manual-$(subst /,-,$(MODULE))}" docker compose down --volumes --remove-orphans

docs:
	@cd website && npm run check && npm run build && npm test

generate:
	@cd website && npm run guides:generate

workflow-check:
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/release_notes_pr_test.py
	bash scripts/ci-modules_test.sh
	bash scripts/ci-gate_test.sh
	bash scripts/audit_test.sh
	bash scripts/make_test.sh
	actionlint .github/workflows/*.yml

# Requires checksum-pinned git-cliff on PATH (or an explicit GIT_CLIFF path).
# Kept separate so ordinary workflow fixtures do not download Rust tooling.
release-preview-check:
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/release_preview_test.py
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/release_evidence_test.py
	PYTHONDONTWRITEBYTECODE=1 python3 scripts/release_publish_test.py

# golangci-lint's configured govet is authoritative for routine checks. This
# standalone command bypasses golangci-lint's diagnostic/exclusion/version
# filtering on scheduled/manual audits; it does not enable shadow/fieldalignment.
vet: module
	@cd '$(MODULE)' && go vet '$(PKG)'
