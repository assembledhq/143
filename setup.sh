#!/usr/bin/env bash
set -euo pipefail

# 143.dev — one-command setup (no Docker required)
# Usage: git clone https://github.com/assembledhq/143.git && cd 143 && ./setup.sh

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

info()  { echo -e "${GREEN}[143]${NC} $*"; }
warn()  { echo -e "${YELLOW}[143]${NC} $*"; }
fail()  { echo -e "${RED}[143]${NC} $*"; exit 1; }

# ---------------------------------------------------------------------------
# 1. Detect OS
# ---------------------------------------------------------------------------
OS="$(uname -s)"
case "$OS" in
  Darwin) PLATFORM="macos" ;;
  Linux)  PLATFORM="linux" ;;
  *)      fail "Unsupported OS: $OS. Only macOS and Linux are supported." ;;
esac
info "Detected platform: ${BOLD}$PLATFORM${NC}"

# ---------------------------------------------------------------------------
# 2. Check / install prerequisites
# ---------------------------------------------------------------------------
install_prereqs() {
  local missing=()

  command -v go   >/dev/null 2>&1 || missing+=(go)
  command -v node >/dev/null 2>&1 || missing+=(node)
  command -v psql >/dev/null 2>&1 || missing+=(postgresql)
  command -v sops >/dev/null 2>&1 || missing+=(sops)
  command -v age  >/dev/null 2>&1 || missing+=(age)

  if [ ${#missing[@]} -eq 0 ]; then
    info "All prerequisites found (go, node, psql, sops, age)."
    return
  fi

  warn "Missing prerequisites: ${missing[*]}"

  if [ "$PLATFORM" = "macos" ]; then
    if ! command -v brew >/dev/null 2>&1; then
      fail "Homebrew not found. Install it from https://brew.sh then re-run this script."
    fi
    for pkg in "${missing[@]}"; do
      case "$pkg" in
        go)         info "Installing Go...";         brew install go ;;
        node)       info "Installing Node.js...";    brew install node ;;
        postgresql) info "Installing PostgreSQL...";  brew install postgresql@17 && brew services start postgresql@17 ;;
        sops)       info "Installing sops...";        brew install sops ;;
        age)        info "Installing age...";         brew install age ;;
      esac
    done
  elif [ "$PLATFORM" = "linux" ]; then
    if command -v apt-get >/dev/null 2>&1; then
      sudo apt-get update -qq
      for pkg in "${missing[@]}"; do
        case "$pkg" in
          go)         info "Installing Go...";         sudo apt-get install -y golang ;;
          node)       info "Installing Node.js...";    sudo apt-get install -y nodejs npm ;;
          postgresql) info "Installing PostgreSQL...";  sudo apt-get install -y postgresql postgresql-client && sudo systemctl start postgresql ;;
          sops)       info "Installing sops...";        sudo apt-get install -y sops ;;
          age)        info "Installing age...";         sudo apt-get install -y age ;;
        esac
      done
    else
      fail "Unsupported package manager. Install these manually: ${missing[*]}"
    fi
  fi

  # Verify core tools landed (sops/age are optional — warn but don't fail)
  command -v go   >/dev/null 2>&1 || fail "Go installation failed."
  command -v node >/dev/null 2>&1 || fail "Node.js installation failed."
  command -v psql >/dev/null 2>&1 || fail "PostgreSQL installation failed."
  if ! command -v sops >/dev/null 2>&1 || ! command -v age >/dev/null 2>&1; then
    warn "sops/age not available — encrypted secrets (make secrets-*) won't work."
    warn "Install manually: brew install sops age"
  fi

  info "All prerequisites installed."
}

install_prereqs

# ---------------------------------------------------------------------------
# 3. Print detected versions
# ---------------------------------------------------------------------------
info "Go:         $(go version | awk '{print $3}')"
info "Node:       $(node --version)"
info "PostgreSQL: $(psql --version | awk '{print $3}')"

# ---------------------------------------------------------------------------
# 4. Set up environment
# ---------------------------------------------------------------------------
DB_NAME="onefortythree"
DB_USER="onefortythree"
DB_PASSWORD="dev"
DB_HOST="localhost"
DB_PORT="5432"
DATABASE_URL="postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"

# Encrypted dev secrets live in a private sibling checkout, not this repo.
# Default: sibling of the MAIN checkout (worktree-safe via the shared git
# dir). Override with SECRETS_DIR; see docs/secrets/README.md.
if [ -z "${SECRETS_DIR:-}" ]; then
  GIT_COMMON_DIR="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    SECRETS_DIR="$(dirname "$GIT_COMMON_DIR")/../143-infra"
  else
    SECRETS_DIR="../143-infra"
  fi
fi
ENC_FILE="$SECRETS_DIR/.env.enc"

NEED_ENV=false
if [ ! -f .env ]; then
  NEED_ENV=true
elif [ -f "$ENC_FILE" ] && [ "$ENC_FILE" -nt .env ]; then
  # Re-decrypt when the encrypted bundle has been updated since .env was last written
  info "$ENC_FILE is newer than .env — re-decrypting..."
  NEED_ENV=true
fi

if [ "$NEED_ENV" = true ]; then
  # If an encrypted bundle exists and the developer has sops+age, decrypt it.
  # This gives returning devs (or anyone with the age key) a seamless setup.
  if [ -f "$ENC_FILE" ] && command -v sops >/dev/null 2>&1; then
    SOPS_AGE_KEY_FILE="${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}"
    if [ -f "$SOPS_AGE_KEY_FILE" ]; then
      info "Found $ENC_FILE and age key — decrypting..."
      if SOPS_AGE_KEY_FILE="$SOPS_AGE_KEY_FILE" sops --decrypt --input-type dotenv --output-type dotenv "$ENC_FILE" > .env 2>/dev/null; then
        info "Decrypted $ENC_FILE → .env"
      else
        warn "Could not decrypt $ENC_FILE (wrong key?). Falling back to .env.example."
        cp .env.example .env
        info "Created .env from .env.example — edit it to add your API keys."
      fi
    else
      warn "Found $ENC_FILE but no age key at $SOPS_AGE_KEY_FILE. Falling back to .env.example."
      cp .env.example .env
      info "Created .env from .env.example — edit it to add your API keys."
    fi
  elif [ -f .env.example ]; then
    cp .env.example .env
    info "Created .env from .env.example"
  else
    cat > .env <<EOF
DATABASE_URL=${DATABASE_URL}
PORT=8080
LOG_LEVEL=debug
SESSION_SECRET=$(openssl rand -hex 32)
SANDBOX_IMAGE=143-sandbox:latest
EOF
    info "Created .env with development defaults"
  fi
else
  info ".env already exists and is up to date, skipping."
fi


# ---------------------------------------------------------------------------
# 5. Set up PostgreSQL database
# ---------------------------------------------------------------------------
setup_database() {
  info "Setting up PostgreSQL database..."

  # Try to create the user (ignore error if it already exists)
  if [ "$PLATFORM" = "macos" ]; then
    psql postgres -tc "SELECT 1 FROM pg_roles WHERE rolname='${DB_USER}'" | grep -q 1 \
      || psql postgres -c "CREATE USER ${DB_USER} WITH PASSWORD '${DB_PASSWORD}' CREATEDB;"
  else
    sudo -u postgres psql -tc "SELECT 1 FROM pg_roles WHERE rolname='${DB_USER}'" | grep -q 1 \
      || sudo -u postgres psql -c "CREATE USER ${DB_USER} WITH PASSWORD '${DB_PASSWORD}' CREATEDB;"
  fi

  # Create the database (ignore error if it already exists)
  if [ "$PLATFORM" = "macos" ]; then
    psql postgres -tc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'" | grep -q 1 \
      || createdb -O "${DB_USER}" "${DB_NAME}"
  else
    sudo -u postgres psql -tc "SELECT 1 FROM pg_database WHERE datname='${DB_NAME}'" | grep -q 1 \
      || sudo -u postgres createdb -O "${DB_USER}" "${DB_NAME}"
  fi

  info "Database '${DB_NAME}' is ready."
}

setup_database

# ---------------------------------------------------------------------------
# 6. Install Go dependencies and run migrations
# ---------------------------------------------------------------------------
if [ -f go.mod ]; then
  info "Installing Go dependencies..."
  go mod download

  if [ -d cmd/migrate ]; then
    info "Running database migrations..."
    go run cmd/migrate/main.go up
  elif [ -d migrations ]; then
    info "Migrations directory found — run migrations after the migrate tool is built."
  fi
else
  warn "No go.mod found yet — skipping Go dependency install."
fi

# ---------------------------------------------------------------------------
# 7. Install frontend dependencies
# ---------------------------------------------------------------------------
if [ -d frontend ]; then
  info "Installing frontend dependencies..."
  cd frontend
  npm install
  cd ..
else
  warn "No frontend/ directory yet — skipping frontend install."
fi

# ---------------------------------------------------------------------------
# 8. Build sandbox image when Docker is available
# ---------------------------------------------------------------------------
if command -v docker >/dev/null 2>&1; then
  info "Building sandbox image (143-sandbox:latest)..."
  docker build -t 143-sandbox:latest -f sandbox/Dockerfile .
else
  warn "Docker not found — skipping sandbox image build."
  warn "Docker-backed agent runs need 143-sandbox:latest built from sandbox/Dockerfile."
fi

# ---------------------------------------------------------------------------
# 8.5 Install git pre-commit hooks (unless core.hooksPath is already set
#     elsewhere — e.g. a monorepo parent or a custom dev setup).
# ---------------------------------------------------------------------------
if [ -d .git ] || git rev-parse --git-dir >/dev/null 2>&1; then
  existing_hooks_path=$(git config --get core.hooksPath 2>/dev/null || true)
  if [ -z "$existing_hooks_path" ] || [ "$existing_hooks_path" = ".githooks" ]; then
    info "Installing git pre-commit hooks (tenancy lints + gofmt)..."
    git config core.hooksPath .githooks
    info "Hooks installed. Skip a commit's hooks with: git commit --no-verify"
  else
    warn "core.hooksPath is already set to '$existing_hooks_path' — leaving alone."
    warn "To use 143's hooks, run: git config core.hooksPath .githooks"
  fi
else
  warn "Not a git repo — skipping hook install."
fi

# ---------------------------------------------------------------------------
# 9. Done
# ---------------------------------------------------------------------------
echo ""
echo -e "${GREEN}${BOLD}============================================${NC}"
echo -e "${GREEN}${BOLD}  143.dev is ready!${NC}"
echo -e "${GREEN}${BOLD}============================================${NC}"
echo ""
echo -e "  ${BOLD}Start the Go server:${NC}"
echo -e "    go run cmd/server/main.go"
echo ""
echo -e "  ${BOLD}Start the frontend:${NC}"
echo -e "    cd frontend && npm run dev"
echo ""
echo -e "  ${BOLD}Or start both at once:${NC}"
echo -e "    make dev"
echo ""
echo -e "  ${BOLD}Database:${NC}"
echo -e "    postgresql://${DB_HOST}:${DB_PORT}/${DB_NAME}"
echo ""
echo -e "  ${BOLD}API:${NC}       http://localhost:8080"
echo -e "  ${BOLD}Frontend:${NC}  http://localhost:3000"
echo ""
