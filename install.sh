#!/bin/sh
set -eu

# OpenCTO installer
#
# Default path:
#   binary:    $HOME/.local/bin/opencto
#   workspace: $HOME/.opencto
#
# The installer prefers pre-built GitHub release assets and falls back to a
# source install with Go when release assets are unavailable.

GITHUB_REPO="${OPENCTO_GITHUB_REPO:-LukaGiorgadze/opencto}"
PREFIX="$HOME"
WORKSPACE_DIR="${OPENCTO_WORKSPACE:-$HOME/.opencto}"
UNINSTALL=false

if [ -t 1 ]; then
  BOLD='\033[1m'
  GREEN='\033[32m'
  YELLOW='\033[33m'
  RED='\033[31m'
  RESET='\033[0m'
else
  BOLD=''
  GREEN=''
  YELLOW=''
  RED=''
  RESET=''
fi

bold() { printf "${BOLD}%s${RESET}" "$*"; }
info() { printf "  ${GREEN}✓${RESET} %s\n" "$*"; }
warn() { printf "  ${YELLOW}⚠${RESET} %s\n" "$*" >&2; }
die() {
  printf "  ${RED}✗${RESET} %s\n" "$*" >&2
  exit 1
}

usage() {
  cat <<EOF
$(bold "OpenCTO installer")

Install:
  curl -fsSL https://raw.githubusercontent.com/LukaGiorgadze/opencto/main/install.sh | sh

Uninstall:
  curl -fsSL https://raw.githubusercontent.com/LukaGiorgadze/opencto/main/install.sh | sh -s -- --uninstall
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
  --uninstall)
    UNINSTALL=true
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    die "Unknown option: $1. Run: $0 --help"
    ;;
  esac
  shift
done

BIN_DIR="$PREFIX/.local/bin"
BIN_PATH="$BIN_DIR/opencto"
ORIGINAL_PATH="${PATH:-}"
PROFILE_PATH=""
RELOAD_HINT=""

detect_asset_target() {
  target_os=$(uname -s)
  target_arch=$(uname -m)

  case "$target_os" in
  Darwin) target_os="darwin" ;;
  Linux) target_os="linux" ;;
  *) return 1 ;;
  esac

  case "$target_arch" in
  x86_64 | amd64) target_arch="amd64" ;;
  arm64 | aarch64) target_arch="arm64" ;;
  *) return 1 ;;
  esac

  printf '%s-%s' "$target_os" "$target_arch"
}

install_file() {
  src="$1"
  dst="$2"
  mkdir -p "$(dirname "$dst")"
  if command -v install >/dev/null 2>&1; then
    install -m 755 "$src" "$dst"
  else
    cp "$src" "$dst"
    chmod 755 "$dst"
  fi
}

sha256_file() {
  file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    return 1
  fi
}

find_extracted_binary() {
  root="$1"
  if [ -f "$root/opencto" ]; then
    printf '%s\n' "$root/opencto"
    return 0
  fi
  found=$(find "$root" -type f -name opencto 2>/dev/null | head -n 1 || true)
  [ -n "$found" ] || return 1
  printf '%s\n' "$found"
}

install_prebuilt() {
  target=$(detect_asset_target) || {
    warn "No pre-built OpenCTO asset target for this platform"
    return 1
  }

  asset_name="opencto-${target}.tar.gz"
  asset_url="https://github.com/${GITHUB_REPO}/releases/latest/download/${asset_name}"
  checksums_url="https://github.com/${GITHUB_REPO}/releases/latest/download/SHA256SUMS"

  echo
  printf "%s\n" "$(bold "Installing OpenCTO pre-built binary")"
  info "Platform: $target"
  info "Source:   $asset_url"

  tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/opencto-install.XXXXXX") || return 1
  trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

  if ! curl -fSL --progress-bar "$asset_url" -o "$tmp_dir/$asset_name"; then
    warn "Download failed"
    rm -rf "$tmp_dir"
    trap - EXIT HUP INT TERM
    return 1
  fi

  if ! curl -fsSL "$checksums_url" -o "$tmp_dir/SHA256SUMS"; then
    warn "Could not download SHA256SUMS"
    rm -rf "$tmp_dir"
    trap - EXIT HUP INT TERM
    return 1
  fi

  expected=$(grep "$asset_name" "$tmp_dir/SHA256SUMS" | awk '{print $1}' | head -n 1 || true)
  [ -n "$expected" ] || {
    warn "$asset_name not found in SHA256SUMS"
    rm -rf "$tmp_dir"
    trap - EXIT HUP INT TERM
    return 1
  }

  actual=$(sha256_file "$tmp_dir/$asset_name") || {
    warn "No sha256sum or shasum command found"
    rm -rf "$tmp_dir"
    trap - EXIT HUP INT TERM
    return 1
  }

  [ "$actual" = "$expected" ] || die "Checksum mismatch for $asset_name"
  info "Checksum verified"

  tar -xzf "$tmp_dir/$asset_name" -C "$tmp_dir"
  extracted=$(find_extracted_binary "$tmp_dir") || {
    warn "Downloaded archive did not contain an opencto binary"
    rm -rf "$tmp_dir"
    trap - EXIT HUP INT TERM
    return 1
  }
  install_file "$extracted" "$BIN_PATH"

  rm -rf "$tmp_dir"
  trap - EXIT HUP INT TERM
  return 0
}

install_source() {
  echo
  printf "%s\n" "$(bold "Installing OpenCTO from source")"
  info "Binary: $BIN_PATH"

  command -v go >/dev/null 2>&1 || die "Go is required because no release binary was available."
  mkdir -p "$BIN_DIR"

  tmp_src_dir=""
  if [ -f "go.mod" ] && grep -q '^module github.com/opencto/opencto$' go.mod 2>/dev/null && [ -d "cmd/opencto" ]; then
    src_dir="."
    info "Source:   current checkout"
  else
    command -v git >/dev/null 2>&1 || die "Git is required because no release binary was available."
    tmp_src_dir=$(mktemp -d "${TMPDIR:-/tmp}/opencto-source.XXXXXX") || exit 1
    trap 'rm -rf "$tmp_src_dir"' EXIT HUP INT TERM
    src_dir="$tmp_src_dir/opencto"
    info "Source:   https://github.com/${GITHUB_REPO}.git"
    git clone --depth 1 "https://github.com/${GITHUB_REPO}.git" "$src_dir"
  fi

  (cd "$src_dir" && GOBIN="$BIN_DIR" go install ./cmd/opencto)

  if [ -n "$tmp_src_dir" ]; then
    rm -rf "$tmp_src_dir"
    trap - EXIT HUP INT TERM
  fi
}

ensure_workspace() {
  mkdir -p "$WORKSPACE_DIR"
}

detect_shell_profile() {
  shell_name=$(basename "${SHELL:-/bin/bash}")
  case "$shell_name" in
  zsh) printf '%s\n' "$HOME/.zshrc" ;;
  fish) printf '%s\n' "$HOME/.config/fish/config.fish" ;;
  *) printf '%s\n' "$HOME/.bashrc" ;;
  esac
}

shell_path_line() {
  shell_name=$(basename "${SHELL:-/bin/bash}")
  case "$shell_name" in
  fish) printf 'set -gx PATH "%s" $PATH\n' "$BIN_DIR" ;;
  *) printf 'export PATH="%s:$PATH"\n' "$BIN_DIR" ;;
  esac
}

shell_reload_hint() {
  profile_path="$1"
  shell_name=$(basename "${SHELL:-/bin/bash}")
  case "$shell_name" in
  fish) printf 'source %s\n' "$profile_path" ;;
  *) printf '. %s\n' "$profile_path" ;;
  esac
}

ensure_path_in_profile() {
  profile=$(detect_shell_profile)
  path_line=$(shell_path_line)
  reload_hint=$(shell_reload_hint "$profile")
  PROFILE_PATH="$profile"
  RELOAD_HINT="$reload_hint"
  active_bin=$(PATH="$ORIGINAL_PATH" command -v opencto 2>/dev/null || true)

  if [ -n "$active_bin" ] && [ "$active_bin" != "$BIN_PATH" ]; then
    active_version=$("$active_bin" --help 2>/dev/null | head -n 1 || printf 'unknown')
    echo
    warn "$(bold "WARNING:") opencto in PATH is $active_bin"
    warn "It may shadow the binary installed at $BIN_PATH"
    warn "First line from existing binary: $active_version"
  fi

  if [ -f "$profile" ] && grep -F "$BIN_DIR" "$profile" >/dev/null 2>&1; then
    info "Shell profile already includes $BIN_DIR"
    return 0
  fi

  mkdir -p "$(dirname "$profile")"
  {
    echo
    echo "# OpenCTO"
    printf '%s\n' "$path_line"
  } >>"$profile"

  info "Added $BIN_DIR to $profile"
  warn "Open a new terminal or run: $reload_hint"
}

run_interactive_configure() {
  if [ -f "$WORKSPACE_DIR/config.json" ] && [ -f "$WORKSPACE_DIR/.env" ]; then
    info "Existing workspace preserved: $WORKSPACE_DIR"
    return 0
  fi

  if [ ! -r /dev/tty ]; then
    warn "No interactive terminal found; run opencto configure after install"
    return 0
  fi

  if OPENCTO_WORKSPACE="$WORKSPACE_DIR" "$BIN_PATH" configure </dev/tty; then
    info "Configured OpenCTO workspace"
  else
    warn "Interactive configuration skipped; run opencto configure"
  fi
}

print_success_screen() {
  echo
  printf "%s\n" "┌────────────────────────────────────────────────────────────┐"
  printf "│ ${BOLD}${GREEN}OpenCTO installed successfully${RESET}                         │\n"
  printf "%s\n" "├────────────────────────────────────────────────────────────┤"
  printf "│ Binary:      %-45s │\n" "$BIN_PATH"
  printf "│ Workspace:   %-45s │\n" "$WORKSPACE_DIR"
  printf "│ Credentials: %-45s │\n" "$WORKSPACE_DIR/.env"
  printf "│ Config:      %-45s │\n" "$WORKSPACE_DIR/config.json"
  printf "%s\n" "├────────────────────────────────────────────────────────────┤"
  printf "│ Next steps:                                                │\n"
  printf "│   1. opencto configure                                    │\n"
  printf "│   2. ${BOLD}opencto start${RESET}                                        │\n"
  printf "│   3. opencto doctor                                       │\n"
  printf "%s\n" "├────────────────────────────────────────────────────────────┤"
  if [ -n "$PROFILE_PATH" ] && [ -n "$RELOAD_HINT" ]; then
    printf "│ If opencto is not found, open a new terminal or run:       │\n"
    printf "│   %-55s │\n" "$RELOAD_HINT"
  else
    printf "│ If opencto is not found, open a new terminal and try again.│\n"
  fi
  printf "%s\n" "└────────────────────────────────────────────────────────────┘"
  echo
}

stop_managed_services() {
  compose_file="$WORKSPACE_DIR/services/compose.yaml"
  if [ ! -f "$compose_file" ]; then
    return 0
  fi
  if ! command -v docker >/dev/null 2>&1; then
    warn "Docker not found; managed services may still be running"
    return 0
  fi

  if docker compose -f "$compose_file" down; then
    info "Stopped managed services"
  else
    warn "Could not stop managed services; workspace preserved at $WORKSPACE_DIR"
  fi
}

confirm_remove_workspace() {
  if [ ! -d "$WORKSPACE_DIR" ]; then
    return 1
  fi

  echo
  warn "OpenCTO workspace: $WORKSPACE_DIR"
  warn "Contains config.json, .env credentials, local database, workflow data, and generated service files."

  if [ ! -r /dev/tty ]; then
    warn "No interactive terminal found; preserving workspace"
    return 1
  fi

  printf "Remove the OpenCTO workspace completely? [y/N] " >/dev/tty
  IFS= read -r answer </dev/tty || answer=
  case "$answer" in
  y | Y | yes | YES)
    return 0
    ;;
  *)
    return 1
    ;;
  esac
}

remove_workspace() {
  case "$WORKSPACE_DIR" in
  "" | "/" | "$HOME" | "$HOME/")
    die "Refusing to remove unsafe workspace path: $WORKSPACE_DIR"
    ;;
  esac

  rm -rf "$WORKSPACE_DIR"
  info "Removed workspace $WORKSPACE_DIR"
}

do_uninstall() {
  echo
  printf "%s\n" "$(bold "Uninstalling OpenCTO")"

  stop_managed_services

  if [ -f "$BIN_PATH" ]; then
    rm -f "$BIN_PATH"
    info "Removed $BIN_PATH"
  else
    warn "Binary not found at $BIN_PATH"
  fi

  if [ -d "$WORKSPACE_DIR" ]; then
    if confirm_remove_workspace; then
      remove_workspace
    else
      info "Workspace preserved at $WORKSPACE_DIR"
    fi
  fi

  exit 0
}

[ "$UNINSTALL" = true ] && do_uninstall

if ! install_prebuilt; then
  warn "Pre-built install failed; falling back to source install"
  install_source
fi

ensure_workspace

if [ -f "$BIN_PATH" ]; then
  size=$(du -h "$BIN_PATH" | awk '{print $1}')
  info "Installed: $BIN_PATH ($size)"
fi

ensure_path_in_profile
run_interactive_configure
print_success_screen
