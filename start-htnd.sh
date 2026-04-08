#!/usr/bin/env bash
if [[ -z "${BASH_VERSION-}" ]]; then
  echo "ERROR: This script must be run with bash (not sh). Try: bash start-htnd.sh" >&2
  exit 1
fi

set -euo pipefail

# Interactive HTND starter.
#
# Designed to work both when run normally:
#   bash start-htnd.sh
# and when piped from the internet (prompts use /dev/tty):
#   curl -fsSL https://raw.githubusercontent.com/HoosatNetwork/HTND/master/start-htnd.sh | bash
#
# This script can DOWNLOAD+INSTALL HTND (optional), then start it.

TTY_IN="/dev/tty"
if [[ ! -r "$TTY_IN" ]]; then
  TTY_IN="/dev/stdin"
fi

say() { printf '%s\n' "$*"; }
die() { say "ERROR: $*" >&2; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "Missing required command: $1"
}

download_to_file() {
  local url="$1"
  local out="$2"

  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 3 --connect-timeout 10 --max-time 600 -o "$out" "$url"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -q --tries=3 --timeout=10 -O "$out" "$url"
    return
  fi
  die "Need curl or wget to download releases"
}

detect_platform_arch() {
  local os arch
  os="$(uname -s 2>/dev/null || echo unknown)"
  arch="$(uname -m 2>/dev/null || echo unknown)"

  if [[ "${os,,}" != "linux" ]]; then
    die "This script currently supports Linux only (detected: $os)"
  fi

  case "${arch,,}" in
    x86_64|amd64) printf '%s' "amd64" ;;
    aarch64|arm64) printf '%s' "aarch64" ;;
    *) die "Unsupported CPU architecture: $arch" ;;
  esac
}

github_latest_tag() {
  local owner="$1"
  local repo="$2"
  local api_url="https://api.github.com/repos/${owner}/${repo}/releases/latest"
  local json

  if command -v curl >/dev/null 2>&1; then
    json="$(curl -fsSL --retry 3 --connect-timeout 10 --max-time 60 "$api_url")" || return 1
  elif command -v wget >/dev/null 2>&1; then
    json="$(wget -qO- "$api_url")" || return 1
  else
    return 1
  fi

  # Extract tag_name value without requiring jq.
  echo "$json" | sed -n 's/^[[:space:]]*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]\+\)".*/\1/p' | head -n 1
}

install_htnd_release() {
  local owner="$1"
  local repo="$2"
  local tag="$3"
  local install_dir="$4"
  local platform_arch="$5"

  need_cmd tar
  need_cmd mktemp

  mkdir -p "$install_dir" || die "Failed to create install dir: $install_dir"
  [[ -w "$install_dir" ]] || die "Install dir is not writable: $install_dir"

  local version="$tag"
  version="${version#v}"

  local asset="HTND-${version}-linux-${platform_arch}.tar.gz"
  local url="https://github.com/${owner}/${repo}/releases/download/${tag}/${asset}"

  local tmpdir tarball
  tmpdir="$(mktemp -d)"
  tarball="$tmpdir/$asset"

  say "Downloading: $url"
  if ! download_to_file "$url" "$tarball"; then
    # Some release processes might omit the leading 'v' in the tag.
    if [[ "$tag" == v* ]]; then
      local tag_no_v="${tag#v}"
      url="https://github.com/${owner}/${repo}/releases/download/${tag_no_v}/${asset}"
      say "Retrying: $url"
      download_to_file "$url" "$tarball" || die "Failed to download release asset"
    else
      die "Failed to download release asset"
    fi
  fi

  (cd "$tmpdir" && tar -xzf "$tarball") || die "Failed to extract release tarball"

  local extracted_bin=""
  if [[ -x "$tmpdir/HTND" ]]; then
    extracted_bin="$tmpdir/HTND"
  elif [[ -x "$tmpdir/htnd" ]]; then
    extracted_bin="$tmpdir/htnd"
  else
    # Look for a binary in case the archive includes a folder.
    extracted_bin="$(find "$tmpdir" -maxdepth 3 -type f \( -name HTND -o -name htnd \) -perm -u+x 2>/dev/null | head -n 1 || true)"
  fi

  [[ -n "$extracted_bin" ]] || die "Could not find HTND binary in extracted archive"

  local dest="$install_dir/HTND"
  cp -f "$extracted_bin" "$dest" || die "Failed to install HTND to $dest"
  chmod +x "$dest" || true

  say "Installed HTND to: $dest"
  printf '%s' "$dest"
}

trim() {
  local s="$1"
  # shellcheck disable=SC2001
  s="$(echo "$s" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
  printf '%s' "$s"
}

expand_tilde() {
  local p="$1"
  if [[ "$p" == "~" ]]; then
    printf '%s' "$HOME"
    return
  fi
  if [[ "$p" == "~/"* ]]; then
    printf '%s' "$HOME/${p:2}"
    return
  fi
  printf '%s' "$p"
}

prompt() {
  local label="$1"
  local default_value="${2-}"
  local input

  if [[ -n "${default_value}" ]]; then
    read -r -p "$label [$default_value]: " input <"$TTY_IN" || true
    input="$(trim "$input")"
    if [[ -z "$input" ]]; then
      input="$default_value"
    fi
  else
    read -r -p "$label: " input <"$TTY_IN" || true
    input="$(trim "$input")"
  fi

  printf '%s' "$input"
}

prompt_yes_no() {
  local label="$1"
  local default_yes_no="${2:-yes}"
  local default_hint

  if [[ "$default_yes_no" == "yes" ]]; then
    default_hint="Y/n"
  else
    default_hint="y/N"
  fi

  while true; do
    local input
    read -r -p "$label [$default_hint]: " input <"$TTY_IN" || true
    input="$(trim "$input")"
    if [[ -z "$input" ]]; then
      input="$default_yes_no"
    fi
    case "${input,,}" in
      y|yes) return 0 ;;
      n|no) return 1 ;;
      *) say "Please answer yes or no." ;;
    esac
  done
}

resolve_htnd_bin() {
  local candidate

  # 1) explicit env var
  if [[ -n "${HTND_BIN-}" ]]; then
    candidate="$HTND_BIN"
    candidate="$(expand_tilde "$candidate")"
    if [[ -x "$candidate" ]]; then
      printf '%s' "$candidate"
      return
    fi
    die "HTND_BIN is set but not executable: $candidate"
  fi

  # 2) common install locations / PATH
  local preferred_install_dir="${HTND_INSTALL_DIR-}"
  if [[ -z "$preferred_install_dir" ]]; then
    preferred_install_dir="$HOME/bin"
  fi
  preferred_install_dir="$(expand_tilde "$preferred_install_dir")"

  for candidate in "$preferred_install_dir/HTND" "$HOME/bin/HTND" "$(command -v HTND 2>/dev/null || true)" "$(command -v htnd 2>/dev/null || true)"; do
    if [[ -n "$candidate" && -x "$candidate" ]]; then
      printf '%s' "$candidate"
      return
    fi
  done

  printf '%s' ""
}

print_usage() {
  cat <<'USAGE'
Usage:
  ./start-htnd.sh

Optional environment variables:
  HTND_BIN       Path to HTND binary (e.g. /home/htnd/bin/HTND)
  HTND_INSTALL_DIR  Install directory for HTND if downloading (default: ~/bin)
  HTND_GH_OWNER  GitHub owner/org for releases (default: HoosatNetwork)
  HTND_GH_REPO   GitHub repo name for releases (default: HTND)
  HTND_VERSION   Version to download (e.g. 1.7.0) OR 'latest'
  HTND_APPDIR    App directory for HTND data (e.g. ~/.htnd)
  HTND_NETWORK   mainnet|testnet|testnet-b5|testnet-b10|simnet|devnet
  HTND_ADDPEER   Add a peer on startup (default on mainnet: mainnet-node-1.hoosat.fi; set to 'none' to disable)
  HTND_DETACH    yes|no (default: no)
  HTND_LOGLEVEL  trace|debug|info|warn|error|critical
  HTND_UTXOINDEX  yes|no (default: yes)

One-liner (runs interactively):
  curl -fsSL https://raw.githubusercontent.com/HoosatNetwork/HTND/master/start-htnd.sh | bash
USAGE
}

if [[ "${1-}" =~ ^(-h|--help)$ ]]; then
  print_usage
  exit 0
fi

say "HTND Startup Script"
say "-----------------------"

gh_owner="${HTND_GH_OWNER-}"
if [[ -z "$gh_owner" ]]; then
  gh_owner="HoosatNetwork"
fi
gh_repo="${HTND_GH_REPO-}"
if [[ -z "$gh_repo" ]]; then
  gh_repo="HTND"
fi

install_dir="${HTND_INSTALL_DIR-}"
if [[ -z "$install_dir" ]]; then
  install_dir="$HOME/bin"
fi
install_dir="$(expand_tilde "$install_dir")"

platform_arch="$(detect_platform_arch)"

htnd_bin="$(resolve_htnd_bin)"
if [[ -z "$htnd_bin" ]]; then
  say "Couldn't find HTND automatically."
  if prompt_yes_no "Download and install HTND release now?" "yes"; then
    version_input="${HTND_VERSION-}"
    if [[ -z "$version_input" ]]; then
      version_input="latest"
    fi
    version_input="$(prompt 'HTND version to download (e.g. 1.7.0) or latest' "$version_input")"
    version_input="$(trim "$version_input")"

    tag=""
    if [[ "${version_input,,}" == "latest" ]]; then
      tag="$(github_latest_tag "$gh_owner" "$gh_repo" || true)"
      [[ -n "$tag" ]] || die "Could not determine latest release tag from GitHub"
    else
      tag="$version_input"
      if [[ "$tag" != v* ]]; then
        tag="v$tag"
      fi
    fi

    htnd_bin="$(install_htnd_release "$gh_owner" "$gh_repo" "$tag" "$install_dir" "$platform_arch")"
    say ""
  else
    htnd_bin="$(prompt 'Path to HTND binary' "$install_dir/HTND")"
    htnd_bin="$(expand_tilde "$htnd_bin")"
    [[ -x "$htnd_bin" ]] || die "Not executable: $htnd_bin"
  fi
else
  if prompt_yes_no "HTND found at '$htnd_bin'. Download/update to a release anyway?" "no"; then
    version_input="${HTND_VERSION-}"
    if [[ -z "$version_input" ]]; then
      version_input="latest"
    fi
    version_input="$(prompt 'HTND version to download (e.g. 1.7.0) or latest' "$version_input")"
    version_input="$(trim "$version_input")"

    tag=""
    if [[ "${version_input,,}" == "latest" ]]; then
      tag="$(github_latest_tag "$gh_owner" "$gh_repo" || true)"
      [[ -n "$tag" ]] || die "Could not determine latest release tag from GitHub"
    else
      tag="$version_input"
      if [[ "$tag" != v* ]]; then
        tag="v$tag"
      fi
    fi

    htnd_bin="$(install_htnd_release "$gh_owner" "$gh_repo" "$tag" "$install_dir" "$platform_arch")"
    say ""
  fi
fi

appdir_default="${HTND_APPDIR-}"
if [[ -z "$appdir_default" ]]; then
  appdir_default="$HOME/.htnd"
fi
appdir_default="$(expand_tilde "$appdir_default")"

network_default="${HTND_NETWORK-}"
if [[ -z "$network_default" ]]; then
  network_default="mainnet"
fi

loglevel_default="${HTND_LOGLEVEL-}"
loglevel_default="$(trim "$loglevel_default")"

detach_default="${HTND_DETACH-}"
if [[ -z "$detach_default" ]]; then
  detach_default="no"
fi

utxoindex_default="${HTND_UTXOINDEX-}"
if [[ -z "$utxoindex_default" ]]; then
  utxoindex_default="yes"
fi

appdir="$(prompt 'App directory (--appdir)' "$appdir_default")"
appdir="$(expand_tilde "$appdir")"

network="$(prompt 'Network (mainnet/testnet/testnet-b5/testnet-b10/simnet/devnet)' "$network_default")"
network="$(trim "$network")"

addpeer="${HTND_ADDPEER-}"
addpeer="$(trim "$addpeer")"
if [[ -z "$addpeer" && "${network,,}" == "mainnet" ]]; then
  addpeer="mainnet-node-1.hoosat.fi"
fi
if [[ "${addpeer,,}" == "none" ]]; then
  addpeer=""
fi

loglevel="$loglevel_default"

say
say "RPC settings"
saferpc=true

if prompt_yes_no "Enable --saferpc (disables state-changing RPC calls)?" "yes"; then
  saferpc=true
else
  saferpc=false
fi

say
say "Run mode"
if prompt_yes_no "Run HTND in background (detach)?" "$detach_default"; then
  detach=true
else
  detach=false
fi

mkdir -p "$appdir" || die "Failed to create appdir: $appdir"
[[ -w "$appdir" ]] || die "Appdir is not writable: $appdir"

args=()
args+=(--appdir "$appdir")

case "${network,,}" in
  mainnet) ;;
  testnet) args+=(--testnet) ;;
  testnet-b5) args+=(--testnet-b5) ;;
  testnet-b10) args+=(--testnet-b10) ;;
  simnet) args+=(--simnet) ;;
  devnet) args+=(--devnet) ;;
  *) die "Unknown network: $network" ;;
 esac

if [[ -n "$addpeer" ]]; then
  args+=(-a "$addpeer")
fi

if [[ -n "$loglevel" ]]; then
  args+=(--loglevel "$loglevel")
fi

if [[ "$saferpc" == true ]]; then
  args+=(--saferpc)
fi

# Enable UTXO index by default.
case "${utxoindex_default,,}" in
  n|no|false|0) ;;
  *) args+=(--utxoindex) ;;
esac


say
say "Command to run:"
# Print in a way users can copy/paste.
printf '  %q' "$htnd_bin" "${args[@]}"
printf '\n'

if ! prompt_yes_no "Start HTND now?" "yes"; then
  say "Cancelled."
  exit 0
fi

if [[ "$detach" == true ]]; then
  stdout_log="$appdir/htnd.stdout.log"
  pid_file="$appdir/htnd.pid"

  say "Starting in background…"
  nohup "$htnd_bin" "${args[@]}" >>"$stdout_log" 2>&1 &
  echo $! >"$pid_file"

  say "Started."
  say "- PID: $(cat "$pid_file")"
  say "- Stdout/stderr log: $stdout_log"
  say "- Stop: kill \"$(cat "$pid_file")\""
else
  say "Starting in foreground… (Ctrl+C to stop)"
  exec "$htnd_bin" "${args[@]}"
fi
