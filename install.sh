#!/bin/sh
# Carryon installer - https://github.com/carryon-dev/cli
# Usage: curl -fsSL https://carryon.dev/get | sh
set -eu

REPO="carryon-dev/cli"
BINARY="carryon"
INSTALL_DIR="${CARRYON_INSTALL_DIR:-}"

# Colors (only when stdout is a terminal)
if [ -t 1 ]; then
  BOLD='\033[1m'
  DIM='\033[2m'
  GREEN='\033[0;32m'
  YELLOW='\033[0;33m'
  RED='\033[0;31m'
  RESET='\033[0m'
else
  BOLD='' DIM='' GREEN='' YELLOW='' RED='' RESET=''
fi

info()  { printf "${GREEN}>${RESET} %s\n" "$@"; }
warn()  { printf "${YELLOW}!${RESET} %s\n" "$@"; }
error() { printf "${RED}x${RESET} %s\n" "$@" >&2; exit 1; }

# Detect OS
detect_os() {
  case "$(uname -s)" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "darwin" ;;
    MINGW*|MSYS*|CYGWIN*) echo "windows" ;;
    *) error "Unsupported operating system: $(uname -s)" ;;
  esac
}

# Detect architecture
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  echo "amd64" ;;
    aarch64|arm64)  echo "arm64" ;;
    *) error "Unsupported architecture: $(uname -m)" ;;
  esac
}

# Pick install directory
pick_install_dir() {
  if [ -n "$INSTALL_DIR" ]; then
    echo "$INSTALL_DIR"
    return
  fi

  # Prefer ~/.local/bin (XDG), fall back to ~/.carryon/bin, use /usr/local/bin if root
  if [ "$(id -u)" = "0" ]; then
    echo "/usr/local/bin"
  elif [ -d "$HOME/.local/bin" ]; then
    echo "$HOME/.local/bin"
  else
    echo "$HOME/.carryon/bin"
  fi
}

# Find a download tool
download() {
  url="$1"
  output="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$output"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$output" "$url"
  else
    error "Neither curl nor wget found. Install one and try again."
  fi
}

# Get latest version from GitHub
get_latest_version() {
  url="https://api.github.com/repos/${REPO}/releases/latest"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"//;s/".*//'
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- "$url" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"//;s/".*//'
  fi
}

verify_checksum() {
  file="$1"
  expected="$2"

  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$file" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$file" | awk '{print $1}')
  else
    warn "No sha256sum or shasum found - skipping checksum verification"
    return 0
  fi

  if [ "$actual" != "$expected" ]; then
    error "Checksum mismatch!\n  Expected: $expected\n  Got:      $actual"
  fi
}

# Ensure directory is in PATH, suggest shell config update if not
ensure_in_path() {
  dir="$1"
  case ":$PATH:" in
    *":$dir:"*) return 0 ;;
  esac

  shell_config=""
  case "${SHELL:-}" in
    */zsh)  shell_config="$HOME/.zshrc" ;;
    */bash)
      if [ -f "$HOME/.bashrc" ]; then
        shell_config="$HOME/.bashrc"
      elif [ -f "$HOME/.bash_profile" ]; then
        shell_config="$HOME/.bash_profile"
      fi
      ;;
    */fish) shell_config="$HOME/.config/fish/config.fish" ;;
  esac

  if [ -n "$shell_config" ] && [ -f "$shell_config" ]; then
    # Check if already added
    if ! grep -q "$dir" "$shell_config" 2>/dev/null; then
      case "${SHELL:-}" in
        */fish)
          printf '\nfish_add_path "%s"\n' "$dir" >> "$shell_config"
          ;;
        *)
          printf '\nexport PATH="%s:$PATH"\n' "$dir" >> "$shell_config"
          ;;
      esac
      info "Added $dir to PATH in $shell_config"
    fi
  fi

  warn "$dir is not in your PATH. Run:"
  printf "  ${DIM}export PATH=\"%s:\$PATH\"${RESET}\n" "$dir"
}

main() {
  printf "\n"
  printf "  ${BOLD}Carryon Installer${RESET}\n"
  printf "  ${DIM}Terminal sessions that persist${RESET}\n"
  printf "\n"

  OS=$(detect_os)
  ARCH=$(detect_arch)
  DIR=$(pick_install_dir)
  VERSION="${CARRYON_VERSION:-$(get_latest_version)}"

  if [ -z "$VERSION" ]; then
    error "Could not determine latest version. Set CARRYON_VERSION and retry."
  fi

  info "Platform: ${OS}/${ARCH}"
  info "Version:  ${VERSION}"
  info "Target:   ${DIR}/${BINARY}"

  # Build download URL - GoReleaser produces .tar.gz archives
  # Archive name: carryon-{version}-{os}-{arch}.tar.gz
  # Strip leading 'v' from version for the archive name
  BARE_VERSION="${VERSION#v}"
  ARCHIVE="${BINARY}-${BARE_VERSION}-${OS}-${ARCH}.tar.gz"
  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
  CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

  # Create temp directory
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT

  info "Downloading ${ARCHIVE}..."
  download "$DOWNLOAD_URL" "$TMP/$ARCHIVE"

  # Verify checksum
  info "Verifying checksum..."
  download "$CHECKSUMS_URL" "$TMP/checksums.txt" 2>/dev/null || true
  if [ -f "$TMP/checksums.txt" ]; then
    expected=$(grep "$ARCHIVE" "$TMP/checksums.txt" | awk '{print $1}' | head -1)
    if [ -n "$expected" ]; then
      verify_checksum "$TMP/$ARCHIVE" "$expected"
      info "Checksum verified"
    else
      warn "Asset not found in checksums file - skipping verification"
    fi
  else
    warn "No checksums file - skipping verification"
  fi

  # Extract and install
  info "Extracting..."
  tar xzf "$TMP/$ARCHIVE" -C "$TMP"
  mkdir -p "$DIR"
  mv "$TMP/$BINARY" "$DIR/$BINARY"
  chmod +x "$DIR/$BINARY"

  info "Installed ${BINARY} to ${DIR}/${BINARY}"

  # Check PATH
  ensure_in_path "$DIR"

  # Verify
  if command -v "$BINARY" >/dev/null 2>&1; then
    installed_version=$("$BINARY" --version 2>/dev/null || echo "unknown")
    printf "\n  ${GREEN}${BOLD}carryon ${installed_version} installed successfully${RESET}\n"
  else
    printf "\n  ${GREEN}${BOLD}carryon installed successfully${RESET}\n"
    printf "  ${DIM}Restart your shell or run: export PATH=\"%s:\$PATH\"${RESET}\n" "$DIR"
  fi

  printf "\n  Get started:\n"
  printf "    ${DIM}carryon --name dev${RESET}       # create a session\n"
  printf "    ${DIM}carryon list${RESET}             # list sessions\n"
  printf "    ${DIM}carryon --help${RESET}           # full usage\n"
  printf "\n"
}

main "$@"
