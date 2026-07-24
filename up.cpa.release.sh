#!/usr/bin/env bash
# 从 GitHub releases 升级 /root/cpa 下的 CPA。
# grok-manager 已内置于 CPA 二进制，通过 config.yaml 启用，无需安装 .so。
#
# 用法：
#   bash up.cpa.release.sh --check-new --restart
#   bash up.cpa.release.sh --force --restart
#   bash up.cpa.release.sh --version v7.2.95-18 --force --restart
#   bash up.cpa.release.sh --package musl|glibc|no-plugin|auto
#
# cron：
#   */30 * * * * bash /root/cpa/up.cpa.release.sh --check-new --restart >>/root/cpa/up.cpa.release.log 2>&1

set -euo pipefail

REPO="${CPA_RELEASE_REPO:-https://github.com/io55555/CPALITE}"
CPA_DIR="${CPA_DIR:-/root/cpa}"
VERSION_FILE="${CPA_VERSION_FILE:-$CPA_DIR/cpa.version}"
SERVICE="${CPA_SERVICE:-}"
RESTART=0
CHECK_NEW=0
FORCE=0
PACKAGE_PREF="${CPA_PACKAGE_PREF:-auto}" # auto|glibc|musl|no-plugin
REQUEST_VERSION="${CPA_REQUEST_VERSION:-}"

die() { echo "错误：$*" >&2; exit 1; }
log() { echo "[$(date '+%F %T')] $*"; }
need() { command -v "$1" >/dev/null 2>&1 || die "缺少依赖：$1"; }

repo_api_latest() {
  local r="${1%/}"
  case "$r" in
    https://github.com/*/*)
      printf 'https://api.github.com/repos/%s/%s/releases/latest\n' \
        "$(printf '%s' "$r" | awk -F/ '{print $(NF-1)}')" \
        "$(printf '%s' "$r" | awk -F/ '{print $NF}')"
      ;;
    *) printf '%s/releases/latest\n' "$r" ;;
  esac
}

repo_api_tag() {
  local r="${1%/}" tag="$2"
  case "$r" in
    https://github.com/*/*)
      printf 'https://api.github.com/repos/%s/%s/releases/tags/%s\n' \
        "$(printf '%s' "$r" | awk -F/ '{print $(NF-1)}')" \
        "$(printf '%s' "$r" | awk -F/ '{print $NF}')" \
        "$tag"
      ;;
    *) printf '%s/releases/tags/%s\n' "$r" "$tag" ;;
  esac
}

usage() {
  cat <<EOF
用法：bash $0 [参数]
  --check-new          仅当目标版本与 $VERSION_FILE 不同时才升级
  --force              即使版本相同也强制升级
  --version TAG        升级到指定 release（如 v7.2.95-18）；默认 latest
  --tag TAG            同 --version
  --restart            替换后重启 CPA
  --no-restart         只替换不重启（默认）
  --service NAME       systemctl restart NAME
  --dir PATH           安装目录，默认 /root/cpa
  --repo URL           GitHub 仓库
  --package MODE       auto|glibc|musl|no-plugin（默认 auto）
  -h, --help           帮助

包选型（auto）：
  musl/Alpine : _musl -> _no-plugin -> 普通包
  glibc       : 普通包 -> _no-plugin

grok-manager：
  已内置进 CPA，无需下载 .so。在 config.yaml 中启用：
    plugins:
      enabled: true
      configs:
        grok-manager:
          enabled: true
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --check-new) CHECK_NEW=1 ;;
    --force) FORCE=1 ;;
    --version|--tag)
      REQUEST_VERSION="${2:-}"
      [ -n "$REQUEST_VERSION" ] || die "$1 需要版本号，例如 v7.2.95-18"
      shift
      ;;
    --restart) RESTART=1 ;;
    --no-restart) RESTART=0 ;;
    --service) SERVICE="${2:-}"; shift ;;
    --dir) CPA_DIR="${2:-}"; VERSION_FILE="$CPA_DIR/cpa.version"; shift ;;
    --repo) REPO="${2:-}"; shift ;;
    --package) PACKAGE_PREF="${2:-auto}"; shift ;;
    --update-plugin|--skip-plugin)
      log "提示：$1 已忽略（grok-manager 内置于二进制，不再发布/安装 .so）"
      ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
  shift
done

need curl; need tar; need sha256sum; need awk; need sed; need grep; need install; need mktemp; need uname; need find
[ -d "$CPA_DIR" ] || die "目录不存在：$CPA_DIR"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "不支持的架构：$(uname -m)" ;;
esac
[ "$OS" = "linux" ] || die "此脚本仅用于 Linux 服务器"

detect_libc_kind() {
  if [ -f /etc/alpine-release ]; then echo musl; return; fi
  if command -v ldd >/dev/null 2>&1; then
    if ldd --version 2>&1 | grep -qi musl; then echo musl; return; fi
    if ldd --version 2>&1 | grep -qi 'gnu\|glibc'; then echo glibc; return; fi
  fi
  if ls /lib/ld-musl-*.so.1 >/dev/null 2>&1; then echo musl; return; fi
  if [ -e /lib64/ld-linux-x86-64.so.2 ] || [ -e /lib/ld-linux-aarch64.so.1 ]; then echo glibc; return; fi
  echo unknown
}

LIBC_KIND="$(detect_libc_kind)"
case "$PACKAGE_PREF" in
  auto|glibc|musl|no-plugin) ;;
  *) die "--package 仅支持 auto|glibc|musl|no-plugin" ;;
esac

resolve_package_order() {
  case "$PACKAGE_PREF" in
    glibc) printf '%s\n' '' '_no-plugin' '_musl' ;;
    musl) printf '%s\n' '_musl' '_no-plugin' '' ;;
    no-plugin) printf '%s\n' '_no-plugin' '' ;;
    auto)
      case "$LIBC_KIND" in
        musl) printf '%s\n' '_musl' '_no-plugin' '' ;;
        *) printf '%s\n' '' '_no-plugin' '_musl' ;;
      esac
      ;;
  esac
}

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ -n "$REQUEST_VERSION" ]; then
  case "$REQUEST_VERSION" in
    v*) TAG="$REQUEST_VERSION" ;;
    *) TAG="v${REQUEST_VERSION}" ;;
  esac
  API="$(repo_api_tag "$REPO" "$TAG")"
  release_json="$TMP/release.json"
  if ! curl -fsSL --retry 3 --retry-delay 2 "$API" -o "$release_json"; then
    die "找不到指定 release：$TAG（API $API）"
  fi
  GOT_TAG="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$release_json" | head -n1)"
  [ -n "$GOT_TAG" ] || die "无法读取 release tag：$TAG"
  TAG="$GOT_TAG"
  log "目标版本（指定）：$TAG"
else
  API="$(repo_api_latest "$REPO")"
  release_json="$TMP/release.json"
  curl -fsSL --retry 3 --retry-delay 2 "$API" -o "$release_json"
  TAG="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$release_json" | head -n1)"
  [ -n "$TAG" ] || die "无法读取 latest release tag"
  log "目标版本（latest）：$TAG"
fi
VER="${TAG#v}"

old_ver=""
[ -f "$VERSION_FILE" ] && old_ver="$(tr -d '[:space:]' < "$VERSION_FILE" || true)"
if [ "$FORCE" -eq 0 ] && [ "$CHECK_NEW" -eq 1 ] && [ "$old_ver" = "$TAG" ]; then
  log "已是目标版本：$TAG"
  exit 0
fi

download_asset() {
  local out="$1"; shift
  local name url
  for name in "$@"; do
    [ -n "$name" ] || continue
    url="${REPO%/}/releases/download/${TAG}/$name"
    if curl -fsSL --retry 2 --retry-delay 1 "$url" -o "$out" 2>/dev/null; then
      echo "$name"
      return 0
    fi
  done
  return 1
}

package_candidates() {
  local suffix="$1"
  printf '%s\n' \
    "CPA_${VER}_${OS}_${ARCH}${suffix}.tar.gz" \
    "CPA_${TAG}_${OS}_${ARCH}${suffix}.tar.gz" \
    "CPA_v${VER}_${OS}_${ARCH}${suffix}.tar.gz"
}

PKG_NAME=""
PKG_SUFFIX_USED=""
TRIED_PKGS=""
while IFS= read -r suffix; do
  set -- $(package_candidates "$suffix")
  if name="$(download_asset "$TMP/pkg.download" "$@")"; then
    PKG_NAME="$name"
    PKG_SUFFIX_USED="$suffix"
    break
  fi
  for c in "$@"; do TRIED_PKGS="${TRIED_PKGS} ${c}"; done
done < <(resolve_package_order)

[ -n "$PKG_NAME" ] || die "找不到当前平台包：${OS}_${ARCH}（libc=$LIBC_KIND package=$PACKAGE_PREF tag=$TAG）。已尝试：${TRIED_PKGS}"
log "选用包：$PKG_NAME（libc=$LIBC_KIND package=$PACKAGE_PREF）"

PKG="$TMP/$PKG_NAME"
mv -f "$TMP/pkg.download" "$PKG"

MGMT="$TMP/management.html"
download_asset "$MGMT" "management.html" >/dev/null || die "下载 management.html 失败（tag=$TAG）"

SUMS="$TMP/checksums.txt"
if download_asset "$SUMS" "checksums.txt" >/dev/null; then
  if grep -Fq "$PKG_NAME" "$SUMS"; then
    (cd "$TMP" && grep -F "$PKG_NAME" checksums.txt | sha256sum -c -) || die "程序包校验失败：$PKG_NAME"
  else
    log "警告：checksums.txt 中无 $PKG_NAME，跳过校验"
  fi
else
  log "警告：未找到 checksums.txt，跳过校验（tag=$TAG）"
fi

grep -qi '<!doctype html\|<html' "$MGMT" || die "management.html 内容异常"

UNPACK="$TMP/unpack"
mkdir -p "$UNPACK"
tar -xzf "$PKG" -C "$UNPACK"
BIN="$(find "$UNPACK" -maxdepth 2 -type f \( -name CLIProxyAPI -o -name CPA \) | head -n1)"
CFG="$(find "$UNPACK" -maxdepth 2 -type f -name config.example.yaml | head -n1)"
[ -n "$BIN" ] && [ -s "$BIN" ] || die "解压后未找到 CLIProxyAPI 或 CPA"
[ -n "$CFG" ] && [ -s "$CFG" ] || die "解压后未找到 config.example.yaml"
chmod +x "$BIN"

BACKUP="$CPA_DIR/.upgrade-backup-$TAG-$(date +%Y%m%d%H%M%S)"
mkdir -p "$BACKUP" "$CPA_DIR/static"
[ -e "$CPA_DIR/CLIProxyAPI" ] && cp -a "$CPA_DIR/CLIProxyAPI" "$BACKUP/" || true
[ -e "$CPA_DIR/config.example.yaml" ] && cp -a "$CPA_DIR/config.example.yaml" "$BACKUP/" || true
[ -e "$CPA_DIR/static/management.html" ] && cp -a "$CPA_DIR/static/management.html" "$BACKUP/" || true

# 清理历史动态插件文件，避免旧 .so 干扰（功能已内置）
for p in \
  "$CPA_DIR/plugins/linux/amd64/grok-manager.so" \
  "$CPA_DIR/plugins/linux/arm64/grok-manager.so" \
  "$CPA_DIR/plugins/linux/grok-manager.so" \
  "$CPA_DIR/plugins/grok-manager.so"; do
  if [ -e "$p" ]; then
    mv -f "$p" "$BACKUP/$(basename "$p").legacy-so" 2>/dev/null || true
    log "已移走旧插件文件到备份：$p"
  fi
done

install -m 0755 "$BIN" "$CPA_DIR/CLIProxyAPI.new"
install -m 0644 "$CFG" "$CPA_DIR/config.example.yaml.new"
install -m 0644 "$MGMT" "$CPA_DIR/static/management.html.new"
mv -f "$CPA_DIR/CLIProxyAPI.new" "$CPA_DIR/CLIProxyAPI"
mv -f "$CPA_DIR/config.example.yaml.new" "$CPA_DIR/config.example.yaml"
mv -f "$CPA_DIR/static/management.html.new" "$CPA_DIR/static/management.html"
printf '%s\n' "$TAG" > "$VERSION_FILE.new"
mv -f "$VERSION_FILE.new" "$VERSION_FILE"

[ -x "$CPA_DIR/CLIProxyAPI" ] || die "目标程序不可执行"
[ -s "$CPA_DIR/config.example.yaml" ] || die "目标 config.example.yaml 为空"
grep -qi '<!doctype html\|<html' "$CPA_DIR/static/management.html" || die "目标 management.html 异常"

log "grok-manager 已内置：请在 config.yaml 用 plugins.configs.grok-manager.enabled 开关"

restart_cpa() {
  if [ -n "$SERVICE" ]; then
    systemctl restart "$SERVICE"
    return
  fi
  for svc in cpa CPA CLIProxyAPI cliproxyapi; do
    if command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files "$svc.service" --no-legend 2>/dev/null | grep -q .; then
      systemctl restart "$svc.service"
      return
    fi
  done
  pkill -f "$CPA_DIR/CLIProxyAPI" 2>/dev/null || pkill -f "$CPA_DIR/CPA" 2>/dev/null || true
  screen -S cpa -X quit 2>/dev/null || true
  screen -dmS "cpa" bash -c 'cd /root/cpa/ && ./CLIProxyAPI; exec bash'
}

if [ "$RESTART" -eq 1 ]; then
  restart_cpa
  log "升级完成并已重启：${old_ver:-none} -> $TAG，包=$PKG_NAME，备份=$BACKUP"
else
  log "升级完成未重启：${old_ver:-none} -> $TAG，包=$PKG_NAME，备份=$BACKUP"
fi
