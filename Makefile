# Project Makefile for lol_custom_skill_matching


.PHONY: help setup clean \
	front-install front-dev front-build front-preview front-lint \
	back-download back-run back-run-app back-build back-test \
	dev dev-app

# Detect package manager: prefer pnpm, fallback to npm
PKG_MANAGER := $(shell if command -v pnpm >/dev/null 2>&1; then echo pnpm; elif command -v npm >/dev/null 2>&1; then echo npm; else echo none; fi)

help:
	@echo "Available targets:"
	@echo "  setup          - Install front deps and Go modules"
	@echo "  dev            - Run backend and frontend in parallel"
	@echo "  front-install  - Install frontend dependencies (pnpm/npm)"
	@echo "  front-dev      - Start Vite dev server"
	@echo "  front-build    - Build frontend"
	@echo "  front-preview  - Preview built frontend"
	@echo "  front-lint     - Lint frontend"
	@echo "  back-download  - Download Go modules"
	@echo "  back-run       - Run Go backend (loads backend/.env)"
	@echo "  back-build     - Build Go backend binary to backend/bin/server"
	@echo "  back-test      - Run Go tests"
	@echo "  clean          - Remove build artifacts"

setup: front-install back-download

dev:
	@echo "Starting backend and frontend..."
	@$(MAKE) -j2 back-run front-dev

dev-app:
	@echo "Starting API backend and frontend..."
	@$(MAKE) -j2 back-run-app front-dev

# ---------- Frontend ----------
front-install:
ifeq ($(PKG_MANAGER),none)
	@echo "Error: No package manager (pnpm or npm) found." && exit 1
else ifeq ($(PKG_MANAGER),pnpm)
	@echo "[front] Installing with pnpm..."
	@cd front && pnpm install
else
	@echo "[front] Installing with npm..."
	@cd front && npm install
endif

front-dev:
ifeq ($(PKG_MANAGER),none)
	@echo "Error: No package manager (pnpm or npm) found." && exit 1
else ifeq ($(PKG_MANAGER),pnpm)
	@cd front && pnpm dev
else
	@cd front && npm run dev
endif

front-build:
ifeq ($(PKG_MANAGER),none)
	@echo "Error: No package manager (pnpm or npm) found." && exit 1
else ifeq ($(PKG_MANAGER),pnpm)
	@cd front && pnpm build
else
	@cd front && npm run build
endif

front-preview:
ifeq ($(PKG_MANAGER),none)
	@echo "Error: No package manager (pnpm or npm) found." && exit 1
else ifeq ($(PKG_MANAGER),pnpm)
	@cd front && pnpm preview
else
	@cd front && npm run preview
endif

front-lint:
ifeq ($(PKG_MANAGER),none)
	@echo "Error: No package manager (pnpm or npm) found." && exit 1
else ifeq ($(PKG_MANAGER),pnpm)
	@cd front && pnpm lint
else
	@cd front && npm run lint
endif

# ---------- Backend (Go) ----------
back-download:
	@echo "[backend] go mod download..."
	@cd backend && go mod download

back-run:
	@echo "[backend] running (env from backend/.env via godotenv)..."
	@cd backend && go run ./cmd/main.go

# Run backend Web API server (app)
back-run-app:
	@echo "[backend] running Web API (requires RIOT_API_KEY env) ..."
	@cd backend && go run ./cmd/app

back-build:
	@echo "[backend] building to backend/bin/server..."
	@mkdir -p backend/bin
	@cd backend && go build -o bin/server ./cmd/main.go

back-test:
	@echo "[backend] running tests..."
	@cd backend && go test ./...

clean:
	@echo "Cleaning build artifacts..."
	@rm -rf front/dist backend/bin

# ---------- Docker (backend) ----------
.PHONY: docker-build docker-run docker-build-local docker-run-local docker-build-release docker-run-release

# Images
IMAGE_LOCAL := lol-skill-backend:local
IMAGE_RELEASE := lol-skill-backend:release

# Backward-compatible aliases
docker-build: docker-build-release
docker-run: docker-run-local

# Build local dev image
docker-build-local:
	@echo "[docker] Building LOCAL image: $(IMAGE_LOCAL) ..."
	@docker build -f Dockerfile.local -t $(IMAGE_LOCAL) .

# Build release image (public)
docker-build-release:
	@echo "[docker] Building RELEASE image: $(IMAGE_RELEASE) ..."
	@docker build -f Dockerfile -t $(IMAGE_RELEASE) .

# Runs with envs from backend/.env, mounts backend/ as working /data
# Output files (e.g., team_result.json) will appear under backend/
# Run LOCAL image with mounted backend folder
docker-run-local:
	@echo "[docker] Running LOCAL $(IMAGE_LOCAL) ..."
	@docker run --rm \
		--name lol-skill-backend \
		--env-file backend/.env \
		-e PLAYERS_FILE=/data/players.json \
		-v $(PWD)/backend:/data \
		-w /data \
		$(IMAGE_LOCAL)

# Run RELEASE image (built locally or pulled)
docker-run-release:
	@echo "[docker] Running RELEASE $(IMAGE_RELEASE) ..."
	@docker run --rm \
		--name lol-skill-backend \
		--env-file backend/.env \
		-e PLAYERS_FILE=/data/players.json \
		-v $(PWD)/backend:/data \
		-w /data \
		$(IMAGE_RELEASE)
