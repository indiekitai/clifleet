package probe

// rangeShell compares a Node version to a package.json engines.node range.
// It understands the operators npm packages actually ship (>, >=, <, <=, =)
// joined by spaces (AND) and || (OR). Anything else is "unknown": the caller
// does not block on a range it cannot read.
const rangeShell = `
cf_norm() {
  printf '%s\n' "$1" | sed 's/^v//; s/[^0-9.].*$//'
}

cf_split() {
  v=$(cf_norm "$1")
  case $v in
    *.*.*)
      cf_maj=${v%%.*}
      split_rest=${v#*.}
      cf_min=${split_rest%%.*}
      cf_pat=${split_rest#*.}
      ;;
    *.*)
      cf_maj=${v%%.*}
      cf_min=${v#*.}
      cf_pat=0
      ;;
    *)
      cf_maj=$v
      cf_min=0
      cf_pat=0
      ;;
  esac
  case $cf_maj in ''|*[!0-9]*) return 1 ;; esac
  case $cf_min in ''|*[!0-9]*) return 1 ;; esac
  case $cf_pat in ''|*[!0-9]*) return 1 ;; esac
  return 0
}

cf_cmp() {
  cf_split "$1" || return 1
  a1=$cf_maj
  a2=$cf_min
  a3=$cf_pat
  cf_split "$2" || return 1
  if [ "$a1" -ne "$cf_maj" ]; then
    if [ "$a1" -lt "$cf_maj" ]; then cf_cmp_r=-1; else cf_cmp_r=1; fi
    return 0
  fi
  if [ "$a2" -ne "$cf_min" ]; then
    if [ "$a2" -lt "$cf_min" ]; then cf_cmp_r=-1; else cf_cmp_r=1; fi
    return 0
  fi
  if [ "$a3" -ne "$cf_pat" ]; then
    if [ "$a3" -lt "$cf_pat" ]; then cf_cmp_r=-1; else cf_cmp_r=1; fi
    return 0
  fi
  cf_cmp_r=0
  return 0
}

cf_atom() {
  have=$1
  atom=$2
  op=
  ver=$atom
  case $atom in
    '>='*) op='>='; ver=${atom#>=} ;;
    '<='*) op='<='; ver=${atom#<=} ;;
    '>'*) op='>'; ver=${atom#>} ;;
    '<'*) op='<'; ver=${atom#<} ;;
    '='*) op='='; ver=${atom#=} ;;
    *) return 2 ;;
  esac
  case $ver in
    ''|*[!0-9.]*) return 2 ;;
  esac
  cf_cmp "$have" "$ver" || return 2
  c=$cf_cmp_r
  case $op in
    '>=') [ "$c" -ge 0 ] ;;
    '<=') [ "$c" -le 0 ] ;;
    '>') [ "$c" -gt 0 ] ;;
    '<') [ "$c" -lt 0 ] ;;
    '=') [ "$c" -eq 0 ] ;;
    *) return 2 ;;
  esac
}

cf_range_result() {
  have=$1
  range=$2
  range_rest=$range
  saw_yes=0
  saw_no=0
  saw_unk=0
  while [ -n "$range_rest" ]; do
    case $range_rest in
      *'||'*) range_part=${range_rest%%||*}; range_rest=${range_rest#*||} ;;
      *) range_part=$range_rest; range_rest= ;;
    esac
    range_part=$(printf '%s\n' "$range_part" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
    [ -n "$range_part" ] || continue
    set -- $range_part
    clause=yes
    for atom in "$@"; do
      if cf_atom "$have" "$atom"; then
        :
      else
        rc=$?
        if [ "$rc" -eq 2 ]; then
          clause=unk
        else
          clause=no
        fi
        break
      fi
    done
    case $clause in
      yes) saw_yes=1 ;;
      no) saw_no=1 ;;
      unk) saw_unk=1 ;;
    esac
  done
  if [ "$saw_yes" -eq 1 ]; then
    printf 'yes\n'
  elif [ "$saw_unk" -eq 1 ]; then
    printf 'unknown\n'
  elif [ "$saw_no" -eq 1 ]; then
    printf 'no\n'
  else
    printf 'unknown\n'
  fi
}
`

// safeShell is the npm swap and the GitHub-release staging used by fetch and
// apply. It never removes the live binary before the replacement is in place.
const fetchHead = rangeShell + `
cf_json_string() {
  file=$1
  key=$2
  [ -f "$file" ] || return 0
  awk -v key="$key" '
    {
      re = "\"" key "\"[ \t]*:[ \t]*\"[^\"]*\""
      if (match($0, re)) {
        s = substr($0, RSTART, RLENGTH)
        sub(/^.*:[ \t]*"/, "", s)
        sub(/"$/, "", s)
        print s
        exit
      }
    }
  ' "$file"
}

cf_tarball_json() {
  out=$cf_dir/pkg.json
  if tar -xOf "$1" package/package.json > "$out" 2>/dev/null && [ -s "$out" ]; then
    printf '%s\n' "$out"
  fi
}

cf_tarball_version() {
  json=$(cf_tarball_json "$1") || true
  if [ -n "$json" ]; then
    cf_json_string "$json" version
  fi
}

cf_tarball_engines() {
  json=$(cf_tarball_json "$1") || true
  if [ -n "$json" ]; then
    cf_json_string "$json" node
  fi
}

cf_tarball_name() {
  out=$cf_dir/name.json
  if tar -xOf "$1" package/package.json > "$out" 2>/dev/null && [ -s "$out" ]; then
    cf_json_string "$out" name
  fi
}

cf_codex_npm_platform() {
  os=$(uname -s)
  arch=$(uname -m)
  case "$os:$arch" in
    Darwin:arm64|Darwin:aarch64) printf '%s\n' darwin-arm64 ;;
    Darwin:x86_64)
      if [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
        printf '%s\n' darwin-arm64
      else
        printf '%s\n' darwin-x64
      fi
      ;;
    Linux:aarch64|Linux:arm64) printf '%s\n' linux-arm64 ;;
    Linux:x86_64|Linux:amd64) printf '%s\n' linux-x64 ;;
    *) return 1 ;;
  esac
}

cf_restore_labeled() {
  dest=$1
  label=$2
  parent=$(dirname "$dest")
  prev=$parent/.clifleet-$label-prev
  if [ -d "$prev" ]; then
    failed=$parent/.clifleet-$label-failed
    rm -rf "$failed"
    mv "$dest" "$failed" 2>/dev/null || true
    mv "$prev" "$dest" || true
    rm -rf "$failed"
  fi
}

cf_semver_line() {
  printf '%s\n' "$1" | tr -c '0-9.\n' ' ' | awk '{
    for (i = 1; i <= NF; i++) if ($i ~ /^[0-9]+\.[0-9]+\.[0-9]+$/) { print $i; exit }
  }'
}

cf_node_have() {
  if ! command -v node >/dev/null 2>&1; then
    return 1
  fi
  verfile=$cf_dir/nodever
  if cf_cap 3 "$verfile" node --version; then
    head -n 1 "$verfile" | tr -d '\r'
    return 0
  fi
  return 1
}

cf_engines_status() {
  eng=$1
  case $eng in
    ''|-) printf 'ok\n'; return 0 ;;
  esac
  have=$(cf_node_have) || {
    printf 'blocked: needs Node %s (node not on PATH)\n' "$eng"
    return 0
  }
  have_one=$(printf '%s\n' "$have" | head -n 1)
  case $(cf_range_result "$have_one" "$eng") in
    yes) printf 'ok\n' ;;
    no) printf 'blocked: needs Node %s (host has %s)\n' "$eng" "$have_one" ;;
    *) printf 'ok\n' ;;
  esac
}

cf_pkg_version_near() {
  dir=$(dirname "$1")
  i=0
  while [ "$i" -lt 8 ]; do
    if [ -f "$dir/package.json" ]; then
      cf_json_string "$dir/package.json" version
      return 0
    fi
    parent=$(dirname "$dir")
    if [ "$parent" = "$dir" ]; then
      return 0
    fi
    dir=$parent
    i=$((i + 1))
  done
}

cf_cmd_semver() {
  verfile=$cf_dir/cmdver.$$
  if cf_cap 10 "$verfile" "$@"; then
    line=$(head -n 1 "$verfile" 2>/dev/null || true)
    cf_semver_line "$line"
  fi
  rm -f "$verfile"
}

cf_versions_match() {
  left=$(cf_semver_line "$1")
  right=$(cf_semver_line "$2")
  [ -n "$left" ] && [ "$left" = "$right" ]
}

cf_stamp_field() {
  printf '%s\n' "$1" | awk -F '\t' -v n="$2" '{print $n}'
}

cf_atomic_replace() {
  dest=$1
  src=$2
  dir=$(dirname "$dest")
  base=$(basename "$dest")
  tmp="$dir/.clifleet-${base}-new"
  rm -f "$tmp"
  cp "$src" "$tmp" || return 1
  chmod 755 "$tmp" || { rm -f "$tmp"; return 1; }
  mv -f "$tmp" "$dest" || { rm -f "$tmp"; return 1; }
}

cf_atomic_link() {
  link=$1
  target=$2
  if [ -d "$link" ] && [ ! -L "$link" ]; then
    return 1
  fi
  dir=$(dirname "$link")
  tmp="$dir/.clifleet-link-new"
  rm -f "$tmp"
  ln -s "$target" "$tmp" || return 1
  # mv follows a symlink whose target is a directory and would drop the new
  # link inside that directory. -T is GNU; -h is BSD (macOS). Both replace
  # the symlink itself.
  if mv -Tf "$tmp" "$link" 2>/dev/null; then
    return 0
  fi
  if mv -hf "$tmp" "$link" 2>/dev/null; then
    return 0
  fi
  if [ -L "$link" ]; then
    rm -f "$link"
    mv -f "$tmp" "$link" || return 1
    return 0
  fi
  mv -f "$tmp" "$link" || { rm -f "$tmp"; return 1; }
}

cf_download() {
  out=$1
  url=$2
  case $url in
    https://github.com/openai/codex/releases/latest/download/codex-package-*.tar.gz) ;;
    https://downloads.claude.ai/claude-code-releases/latest) ;;
    https://downloads.claude.ai/claude-code-releases/[0-9]*.[0-9]*.[0-9]*/manifest.json) ;;
    https://downloads.claude.ai/claude-code-releases/[0-9]*.[0-9]*.[0-9]*/manifest.json.sig) ;;
    https://downloads.claude.ai/claude-code-releases/[0-9]*.[0-9]*.[0-9]*/darwin-arm64/claude) ;;
    https://downloads.claude.ai/claude-code-releases/[0-9]*.[0-9]*.[0-9]*/darwin-x64/claude) ;;
    https://downloads.claude.ai/claude-code-releases/[0-9]*.[0-9]*.[0-9]*/linux-arm64/claude) ;;
    https://downloads.claude.ai/claude-code-releases/[0-9]*.[0-9]*.[0-9]*/linux-x64/claude) ;;
    https://downloads.claude.ai/claude-code-releases/[0-9]*.[0-9]*.[0-9]*/linux-arm64-musl/claude) ;;
    https://downloads.claude.ai/claude-code-releases/[0-9]*.[0-9]*.[0-9]*/linux-x64-musl/claude) ;;
    https://cursor.com/install) ;;
    https://downloads.cursor.com/lab/[0-9][0-9][0-9][0-9].[0-9][0-9].[0-9][0-9]-*/darwin/arm64/agent-cli-package.tar.gz) ;;
    https://downloads.cursor.com/lab/[0-9][0-9][0-9][0-9].[0-9][0-9].[0-9][0-9]-*/darwin/x64/agent-cli-package.tar.gz) ;;
    https://downloads.cursor.com/lab/[0-9][0-9][0-9][0-9].[0-9][0-9].[0-9][0-9]-*/linux/arm64/agent-cli-package.tar.gz) ;;
    https://downloads.cursor.com/lab/[0-9][0-9][0-9][0-9].[0-9][0-9].[0-9][0-9]-*/linux/x64/agent-cli-package.tar.gz) ;;
    *)
      printf '%s\n' 'refusing download url' >&2
      return 1
      ;;
  esac
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 2 --connect-timeout 15 -o "$out" "$url" </dev/null
    return $?
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -q -O "$out" "$url" </dev/null
    return $?
  fi
  printf '%s\n' 'curl or wget is required' >&2
  return 127
}

`

const applyOnlyShell = `
cf_apply_npm_safe() {
  tool=$1
  pkg=$2
  tarball=$3
  plat_tarball=$4
  staged_ver=$(cf_tarball_version "$tarball")
  eng=$(cf_tarball_engines "$tarball")
  live_path=$(command -v "$tool" 2>/dev/null || true)
  live_ver=
  if [ -n "$live_path" ]; then
    live_real=$(resolve "$live_path")
    live_ver=$(cf_pkg_version_near "$live_real")
    if [ -z "$live_ver" ]; then
      live_ver=$(cf_cmd_semver "$live_path" --version)
    fi
  fi
  if cf_versions_match "$live_ver" "$staged_ver"; then
    cf_report apply "$tool" current "up to date"
    return 0
  fi
  est=$(cf_engines_status "$eng")
  case $est in
    ok) ;;
    *)
      cf_report apply "$tool" blocked "${est#blocked: }"
      return 0
      ;;
  esac
  if ! cf_npm_swap "$tool" "$pkg" "$tarball" "$plat_tarball"; then
    detail=$cf_swap_err
    [ -n "$detail" ] || detail="npm install failed"
    cf_report apply "$tool" 1 "$detail"
    return 0
  fi
  cf_report apply "$tool" 0 "$tarball"
}

cf_clear_codex_junk() {
  parent=$1
  [ -d "$parent" ] || return 0
  # Interrupted npm installs leave @openai/.codex-* next to the live package.
  # Those are junk. The live codex directory is not one of them.
  find "$parent" -mindepth 1 -maxdepth 1 -type d -name '.codex-*' -exec rm -rf {} + 2>/dev/null || true
}

cf_npm_swap() {
  tool=$1
  pkg=$2
  tarball=$3
  plat_tarball=$4
  cf_swap_err=
  prefix=$cf_dir/npm-prefix
  rm -rf "$prefix"
  mkdir -p "$prefix" || { cf_swap_err="could not create temp prefix"; return 1; }
  if [ -n "$plat_tarball" ] && [ "$plat_tarball" != - ] && [ -f "$plat_tarball" ]; then
    if ! npm install -g --prefix "$prefix" --include=optional --no-fund --no-audit "$tarball" "$plat_tarball" </dev/null >&2; then
      cf_swap_err="npm install failed"
      return 1
    fi
  elif ! npm install -g --prefix "$prefix" --include=optional --no-fund --no-audit "$tarball" </dev/null >&2; then
    cf_swap_err="npm install failed"
    return 1
  fi
  src=$prefix/lib/node_modules/$pkg
  if [ ! -d "$src" ]; then
    src=$prefix/node_modules/$pkg
  fi
  if [ ! -d "$src" ]; then
    cf_swap_err="npm install did not produce the package"
    return 1
  fi
  live_root=$(npm root -g </dev/null | tail -n 1 | tr -d '\r')
  case $live_root in
    */lib/node_modules) live_prefix=${live_root%/lib/node_modules} ;;
    */node_modules) live_prefix=${live_root%/node_modules} ;;
    *)
      cf_swap_err="could not read npm root"
      return 1
      ;;
  esac
  live_bin=$live_prefix/bin
  dest=$live_root/$pkg
  parent=$(dirname "$dest")
  mkdir -p "$parent" "$live_bin" || { cf_swap_err="could not create package dir"; return 1; }
  cf_clear_codex_junk "$parent"
  stage=$parent/.clifleet-$tool-new
  prev=$parent/.clifleet-$tool-prev
  if [ ! -e "$dest" ] && [ -d "$prev" ]; then
    mv "$prev" "$dest" || true
  fi
  rm -rf "$stage"
  cp -a "$src" "$stage" || { cf_swap_err="could not stage package on the install filesystem"; return 1; }
  if [ -e "$dest" ] || [ -L "$dest" ]; then
    rm -rf "$prev"
    if ! mv "$dest" "$prev"; then
      rm -rf "$stage"
      cf_swap_err="could not move current package aside"
      return 1
    fi
  fi
  if ! mv "$stage" "$dest"; then
    if [ -d "$prev" ] && [ ! -e "$dest" ]; then
      mv "$prev" "$dest" || true
    fi
    cf_swap_err="could not swap staged package into place"
    return 1
  fi
  plat_dest=
  if [ -n "$plat_tarball" ] && [ "$plat_tarball" != - ] && [ -f "$plat_tarball" ]; then
    plat_name=$(cf_tarball_name "$plat_tarball")
    psrc=$prefix/lib/node_modules/$plat_name
    if [ ! -d "$psrc" ]; then
      psrc=$prefix/node_modules/$plat_name
    fi
    if [ -n "$plat_name" ] && [ -d "$psrc" ]; then
      plat_dest=$live_root/$plat_name
      pparent=$(dirname "$plat_dest")
      mkdir -p "$pparent" || { cf_swap_err="could not create platform dir"; cf_restore_labeled "$dest" "$tool"; return 1; }
      pstage=$pparent/.clifleet-plat-new
      pprev=$pparent/.clifleet-plat-prev
      rm -rf "$pstage"
      if ! cp -a "$psrc" "$pstage"; then
        cf_swap_err="could not stage platform package"
        cf_restore_labeled "$dest" "$tool"
        return 1
      fi
      if [ -e "$plat_dest" ] || [ -L "$plat_dest" ]; then
        rm -rf "$pprev"
        if ! mv "$plat_dest" "$pprev"; then
          rm -rf "$pstage"
          cf_swap_err="could not move platform package aside"
          cf_restore_labeled "$dest" "$tool"
          return 1
        fi
      fi
      if ! mv "$pstage" "$plat_dest"; then
        if [ -d "$pprev" ] && [ ! -e "$plat_dest" ]; then
          mv "$pprev" "$plat_dest" || true
        fi
        cf_swap_err="could not swap platform package"
        cf_restore_labeled "$dest" "$tool"
        return 1
      fi
    fi
  fi
  cf_link_npm_bins "$dest" "$live_bin" || true
  check=$live_bin/$tool
  if [ ! -e "$check" ] && [ ! -L "$check" ]; then
    check=$(command -v "$tool" 2>/dev/null || true)
  fi
  if [ -n "$check" ]; then
    if ! cf_cap 10 "$cf_dir/checkver" "$check" --version; then
      if [ -n "$plat_dest" ]; then
        cf_restore_labeled "$plat_dest" plat
      fi
      cf_restore_labeled "$dest" "$tool"
      cf_swap_err="new package did not run; previous install restored"
      return 1
    fi
  fi
  rm -rf "$prev"
  if [ -n "$plat_dest" ]; then
    rm -rf "$(dirname "$plat_dest")/.clifleet-plat-prev"
  fi
  return 0
}

cf_link_npm_bins() {
  pkgdir=$1
  bindir=$2
  json=$pkgdir/package.json
  [ -f "$json" ] || return 0
  if ! command -v node >/dev/null 2>&1; then
    return 0
  fi
  if ! node -e 'process.exit(0)' </dev/null >/dev/null 2>&1; then
    return 0
  fi
  list=$cf_dir/bins
  CF_JSON=$json node -e 'var fs=require("fs"); var p=JSON.parse(fs.readFileSync(process.env.CF_JSON,"utf8")); var b=p.bin||{}; if(typeof b==="string"){ var n=(p.name||"bin").split("/").pop(); process.stdout.write(n+"\t"+b+"\n"); } else { for(var k in b){ if(Object.prototype.hasOwnProperty.call(b,k)) process.stdout.write(k+"\t"+b[k]+"\n"); } }' </dev/null > "$list" 2>/dev/null || return 0
  while IFS="$(printf '\t')" read -r name rel || [ -n "$name" ]; do
    [ -n "$name" ] || continue
    case $name in
      *[!A-Za-z0-9._+-]*|.*) continue ;;
    esac
    case $rel in
      ''|*..*|/*) continue ;;
    esac
    target=$pkgdir/$rel
    [ -e "$target" ] || continue
    tmp=$bindir/.$name.clifleet-new
    ln -s "$target" "$tmp" || continue
    mv -f "$tmp" "$bindir/$name" || rm -f "$tmp"
  done < "$list"
}

`

const releaseRest = `
cf_codex_target() {
  os=$(uname -s)
  arch=$(uname -m)
  case "$os:$arch" in
    Darwin:arm64|Darwin:aarch64) printf '%s\n' 'aarch64-apple-darwin' ;;
    Darwin:x86_64)
      if [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
        printf '%s\n' 'aarch64-apple-darwin'
      else
        printf '%s\n' 'x86_64-apple-darwin'
      fi
      ;;
    Linux:aarch64|Linux:arm64) printf '%s\n' 'aarch64-unknown-linux-musl' ;;
    Linux:x86_64|Linux:amd64) printf '%s\n' 'x86_64-unknown-linux-musl' ;;
    *) return 1 ;;
  esac
}

cf_claude_platform() {
  os=$(uname -s)
  arch=$(uname -m)
  libc=gnu
  if command -v ldd >/dev/null 2>&1; then
    if ldd --version 2>&1 | grep -qi musl; then
      libc=musl
    fi
  fi
  case "$os:$arch:$libc" in
    Darwin:arm64:*|Darwin:aarch64:*) printf '%s\n' darwin-arm64 ;;
    Darwin:x86_64:*)
      if [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
        printf '%s\n' darwin-arm64
      else
        printf '%s\n' darwin-x64
      fi
      ;;
    Linux:aarch64:musl|Linux:arm64:musl) printf '%s\n' linux-arm64-musl ;;
    Linux:aarch64:*|Linux:arm64:*) printf '%s\n' linux-arm64 ;;
    Linux:x86_64:musl|Linux:amd64:musl) printf '%s\n' linux-x64-musl ;;
    Linux:x86_64:*|Linux:amd64:*) printf '%s\n' linux-x64 ;;
    *) return 1 ;;
  esac
}

cf_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
    return
  fi
  if command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | awk '{print $NF}'
    return
  fi
  return 1
}

cf_manifest_checksum() {
  file=$1
  plat=$2
  awk -v plat="$plat" '
    $0 ~ "\"" plat "\"[[:space:]]*:" { inplat = 1 }
    inplat && $0 ~ /"checksum"/ {
      if (match($0, /[0-9a-fA-F]{64}/)) {
        print substr($0, RSTART, RLENGTH)
        exit
      }
    }
  ' "$file"
}

cf_cursor_platform() {
  os=$(uname -s)
  arch=$(uname -m)
  case "$os" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) return 1 ;;
  esac
  case "$arch" in
    x86_64|amd64) arch=x64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) return 1 ;;
  esac
  if [ "$os" = darwin ] && [ "$arch" = x64 ]; then
    if [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
      arch=arm64
    fi
  fi
  printf '%s/%s\n' "$os" "$arch"
}

cf_release_fetch() {
  tool=$1
  id=$2
  asset=$3
  base=$4
  os=$(uname -s)
  arch=$(uname -m)
  if [ -z "$asset" ]; then
    root=$(cache_root) || { cf_report fetch "$tool" 1 "no home for cache"; return 0; }
    mkdir -p "$root/staged" || { cf_report fetch "$tool" 1 "mkdir failed"; return 0; }
    reason="no $id release asset for $os/$arch"
    printf 'release\t%s\tblocked\t%s\n' "$id" "$reason" > "$root/staged/$tool.tmp" || true
    mv -f "$root/staged/$tool.tmp" "$root/staged/$tool" 2>/dev/null || true
    cf_report fetch "$tool" blocked "$reason"
    return 0
  fi
  url="$base/$asset"
  root=$(cache_root) || { cf_report fetch "$tool" 1 "no home for cache"; return 0; }
  dest="$root/release/$tool"
  rm -rf "$dest"
  mkdir -p "$dest" "$root/staged" || { cf_report fetch "$tool" 1 "mkdir failed"; return 0; }
  if ! cf_download "$dest/$asset" "$url"; then
    cf_report fetch "$tool" 1 "download failed"
    return 0
  fi
  if ! tar -tzf "$dest/$asset" >/dev/null 2>&1; then
    cf_report fetch "$tool" 1 "downloaded archive is not a tarball"
    return 0
  fi
  printf 'release\t%s\t%s\t%s\n' "$id" "$asset" "$dest/$asset" > "$root/staged/$tool.tmp" || {
    cf_report fetch "$tool" 1 "could not write stamp"
    return 0
  }
  mv -f "$root/staged/$tool.tmp" "$root/staged/$tool" || { cf_report fetch "$tool" 1 "could not write stamp"; return 0; }
  cf_report fetch "$tool" 0 "$dest/$asset"
}

stage_release_codex() {
  tool=$1
  asset=
  target=$(cf_codex_target) || target=
  if [ -n "$target" ]; then
    asset="codex-package-$target.tar.gz"
  fi
  cf_release_fetch "$tool" codex "$asset" "https://github.com/openai/codex/releases/latest/download"
}

stage_release_claude() {
  tool=$1
  root=$(cache_root) || { cf_report fetch "$tool" 1 "no home for cache"; return 0; }
  dest="$root/release/$tool"
  rm -rf "$dest"
  mkdir -p "$dest" "$root/staged" || { cf_report fetch "$tool" 1 "mkdir failed"; return 0; }
  plat=$(cf_claude_platform) || {
    cf_report fetch "$tool" blocked "no Claude release for $(uname -s)/$(uname -m)"
    return 0
  }
  if ! cf_download "$dest/latest" "https://downloads.claude.ai/claude-code-releases/latest"; then
    cf_report fetch "$tool" 1 "could not read Claude release version"
    return 0
  fi
  ver=$(tr -cd '0-9.' < "$dest/latest")
  case $ver in
    [0-9]*.[0-9]*.[0-9]*) ;;
    *) cf_report fetch "$tool" 1 "Claude latest version is not a release number"; return 0 ;;
  esac
  base="https://downloads.claude.ai/claude-code-releases/$ver"
  if ! cf_download "$dest/manifest.json" "$base/manifest.json"; then
    cf_report fetch "$tool" 1 "could not download Claude manifest"
    return 0
  fi
  cf_download "$dest/manifest.json.sig" "$base/manifest.json.sig" >/dev/null 2>&1 || rm -f "$dest/manifest.json.sig"
  sum=$(cf_manifest_checksum "$dest/manifest.json" "$plat")
  case $sum in
    ????????????????????????????????????????????????????????????????) ;;
    *) cf_report fetch "$tool" 1 "manifest has no checksum for $plat"; return 0 ;;
  esac
  case $sum in
    *[!0-9a-fA-F]*) cf_report fetch "$tool" 1 "manifest checksum for $plat is not sha256"; return 0 ;;
  esac
  if ! cf_download "$dest/claude" "$base/$plat/claude"; then
    cf_report fetch "$tool" 1 "could not download Claude binary"
    return 0
  fi
  got=$(cf_sha256 "$dest/claude") || { cf_report fetch "$tool" 1 "sha256 tool missing"; return 0; }
  got=$(printf '%s' "$got" | tr 'A-F' 'a-f')
  sum=$(printf '%s' "$sum" | tr 'A-F' 'a-f')
  if [ "$got" != "$sum" ]; then
    cf_report fetch "$tool" 1 "Claude binary checksum did not match the manifest"
    return 0
  fi
  chmod 755 "$dest/claude" 2>/dev/null || true
  printf 'release\tclaude\t%s\t%s\n' "$ver" "$dest/claude" > "$root/staged/$tool.tmp" || {
    cf_report fetch "$tool" 1 "could not write stamp"
    return 0
  }
  mv -f "$root/staged/$tool.tmp" "$root/staged/$tool" || { cf_report fetch "$tool" 1 "could not write stamp"; return 0; }
  cf_report fetch "$tool" 0 "$dest/claude"
}

stage_release_agent() {
  cf_stage_cursor_agent "$1"
}

stage_release_cursor_agent() {
  cf_stage_cursor_agent "$1"
}

cf_stage_cursor_agent() {
  tool=$1
  root=$(cache_root) || { cf_report fetch "$tool" 1 "no home for cache"; return 0; }
  dest="$root/release/$tool"
  rm -rf "$dest"
  mkdir -p "$dest" "$root/staged" || { cf_report fetch "$tool" 1 "mkdir failed"; return 0; }
  pair=$(cf_cursor_platform) || {
    cf_report fetch "$tool" blocked "no Cursor Agent build for $(uname -s)/$(uname -m)"
    return 0
  }
  if ! cf_download "$dest/install.sh" "https://cursor.com/install"; then
    cf_report fetch "$tool" 1 "could not read the Cursor Agent installer"
    return 0
  fi
  ver=$(sed -n 's|.*downloads\.cursor\.com/lab/\([0-9][0-9][0-9][0-9]\.[0-9][0-9]\.[0-9][0-9]-[A-Za-z0-9][A-Za-z0-9]*\)/.*|\1|p' "$dest/install.sh" | head -n 1)
  case $ver in
    [0-9][0-9][0-9][0-9].[0-9][0-9].[0-9][0-9]-*) ;;
    *) cf_report fetch "$tool" blocked "Cursor installer did not publish a versioned download"; return 0 ;;
  esac
  url="https://downloads.cursor.com/lab/$ver/$pair/agent-cli-package.tar.gz"
  if ! cf_download "$dest/agent-cli-package.tar.gz" "$url"; then
    cf_report fetch "$tool" 1 "could not download Cursor Agent"
    return 0
  fi
  if ! tar -tzf "$dest/agent-cli-package.tar.gz" >/dev/null 2>&1; then
    cf_report fetch "$tool" 1 "Cursor Agent download is not a tarball"
    return 0
  fi
  printf 'release\tcursor-agent\t%s\t%s\n' "$ver" "$dest/agent-cli-package.tar.gz" > "$root/staged/$tool.tmp" || {
    cf_report fetch "$tool" 1 "could not write stamp"
    return 0
  }
  mv -f "$root/staged/$tool.tmp" "$root/staged/$tool" || { cf_report fetch "$tool" 1 "could not write stamp"; return 0; }
  cf_report fetch "$tool" 0 "$dest/agent-cli-package.tar.gz"
}

cf_read_release_stamp() {
  tool=$1
  root=$(cache_root) || { cf_report apply "$tool" 1 "no home for cache"; return 1; }
  stamp="$root/staged/$tool"
  if [ ! -f "$stamp" ]; then
    cf_report apply "$tool" skip "nothing staged"
    return 1
  fi
  line=$(head -n 1 "$stamp" | tr -d '\r')
  cf_rel_kind=$(cf_stamp_field "$line" 1)
  cf_rel_id=$(cf_stamp_field "$line" 2)
  cf_rel_a=$(cf_stamp_field "$line" 3)
  cf_rel_b=$(cf_stamp_field "$line" 4)
  if [ "$cf_rel_kind" != release ]; then
    cf_report apply "$tool" 1 "staged artifact is not a release archive"
    return 1
  fi
  if [ "$cf_rel_a" = blocked ]; then
    cf_report apply "$tool" blocked "$cf_rel_b"
    return 1
  fi
  return 0
}

cf_extract_archive() {
  archive=$1
  extract=$cf_dir/extract
  rm -rf "$extract"
  mkdir -p "$extract" || return 1
  tar -xzf "$archive" -C "$extract" || return 1
  printf '%s\n' "$extract"
}

cf_standalone_root() {
  p=$1
  while [ -n "$p" ] && [ "$p" != / ]; do
    if [ "$(basename "$p")" = standalone ]; then
      printf '%s\n' "$p"
      return 0
    fi
    p=$(dirname "$p")
  done
  return 1
}

apply_release_codex() {
  tool=$1
  cf_read_release_stamp "$tool" || return 0
  archive=$cf_rel_b
  case $archive in
    *..*) cf_report apply "$tool" 1 "refusing archive path"; return 0 ;;
  esac
  if [ ! -f "$archive" ] || [ -L "$archive" ]; then
    cf_report apply "$tool" 1 "staged archive missing"
    return 0
  fi
  extract=$(cf_extract_archive "$archive") || { cf_report apply "$tool" 1 "could not extract Codex package"; return 0; }
  if [ ! -f "$extract/bin/codex" ]; then
    sub=$(find "$extract" -mindepth 2 -maxdepth 3 -type f -name codex -path '*/bin/codex' 2>/dev/null | head -n 1)
    if [ -n "$sub" ]; then
      extract=$(dirname "$(dirname "$sub")")
    fi
  fi
  if [ ! -f "$extract/bin/codex" ]; then
    cf_report apply "$tool" 1 "staged Codex package has no bin/codex"
    return 0
  fi
  chmod 755 "$extract/bin/codex" 2>/dev/null || true
  staged_ver=$(cf_json_string "$extract/codex-package.json" version)
  if [ -z "$staged_ver" ]; then
    staged_ver=$(cf_cmd_semver "$extract/bin/codex" --version)
  fi
  staged_ver=$(cf_semver_line "$staged_ver")
  live=$(command -v codex 2>/dev/null || true)
  live_ver=
  if [ -n "$live" ]; then
    live_ver=$(cf_cmd_semver "$live" --version)
  fi
  if [ -n "$staged_ver" ] && [ "$staged_ver" = "$live_ver" ]; then
    cf_report apply "$tool" current "up to date"
    return 0
  fi
  if [ -z "$staged_ver" ]; then
    cf_report apply "$tool" 1 "staged Codex did not report a version"
    return 0
  fi
  case $staged_ver in
    *[!0-9A-Za-z._-]*) cf_report apply "$tool" 1 "refusing Codex version"; return 0 ;;
  esac
  real=
  if [ -n "$live" ]; then
    real=$(resolve "$live")
  fi
  root=$(cf_standalone_root "$real" || true)
  if [ -n "$root" ]; then
    target=$(cf_codex_target) || { cf_report apply "$tool" 1 "no Codex target for this host"; return 0; }
    rel="$root/releases"
    mkdir -p "$rel" || { cf_report apply "$tool" 1 "could not create Codex releases dir"; return 0; }
    name="$staged_ver-$target"
    dest="$rel/$name"
    stage="$rel/.staging.$$"
    rm -rf "$stage"
    mkdir -p "$stage" || { cf_report apply "$tool" 1 "could not stage Codex release"; return 0; }
    cp -a "$extract"/. "$stage"/ || { rm -rf "$stage"; cf_report apply "$tool" 1 "could not copy Codex release"; return 0; }
    if [ ! -e "$stage/codex" ]; then
      ln -sf bin/codex "$stage/codex"
    fi
    cur=$(resolve "$root/current" 2>/dev/null || true)
    if [ "$cur" = "$dest" ]; then
      rm -rf "$stage"
      cf_report apply "$tool" current "up to date"
      return 0
    fi
    if [ -e "$dest" ] || [ -L "$dest" ]; then
      rm -rf "$dest"
    fi
    if ! mv "$stage" "$dest"; then
      rm -rf "$stage"
      cf_report apply "$tool" 1 "could not publish Codex release"
      return 0
    fi
    if ! cf_atomic_link "$root/current" "$dest"; then
      cf_report apply "$tool" 1 "could not switch Codex current link"
      return 0
    fi
    if [ -n "$live" ] && [ -L "$live" ]; then
      cf_atomic_link "$live" "$root/current/bin/codex" || true
    fi
    cf_report apply "$tool" 0 "$archive"
    return 0
  fi
  if [ -z "$real" ]; then
    cf_report apply "$tool" 1 "codex is not on PATH"
    return 0
  fi
  if ! cf_atomic_replace "$real" "$extract/bin/codex"; then
    cf_report apply "$tool" 1 "could not replace Codex binary"
    return 0
  fi
  cf_report apply "$tool" 0 "$archive"
}

apply_release_claude() {
  tool=$1
  cf_read_release_stamp "$tool" || return 0
  ver=$cf_rel_a
  archive=$cf_rel_b
  case $ver in
    [0-9]*.[0-9]*.[0-9]*) ;;
    *) cf_report apply "$tool" 1 "staged Claude version is not a release number"; return 0 ;;
  esac
  case $ver in
    *[!0-9.]*) cf_report apply "$tool" 1 "refusing Claude version"; return 0 ;;
  esac
  case $archive in
    *..*) cf_report apply "$tool" 1 "refusing archive path"; return 0 ;;
  esac
  if [ ! -f "$archive" ] || [ -L "$archive" ]; then
    cf_report apply "$tool" 1 "staged Claude binary missing"
    return 0
  fi
  live=$(command -v claude 2>/dev/null || true)
  if [ -z "$live" ]; then
    cf_report apply "$tool" 1 "claude is not on PATH"
    return 0
  fi
  live_ver=$(cf_cmd_semver "$live" --version)
  if [ -n "$live_ver" ] && [ "$live_ver" = "$ver" ]; then
    cf_report apply "$tool" current "up to date"
    return 0
  fi
  versions="${HOME}/.local/share/claude/versions"
  mkdir -p "$versions" || { cf_report apply "$tool" 1 "could not create Claude versions dir"; return 0; }
  dest="$versions/$ver"
  tmp="$versions/.clifleet-$ver"
  rm -f "$tmp"
  cp "$archive" "$tmp" || { cf_report apply "$tool" 1 "could not copy Claude binary"; return 0; }
  chmod 755 "$tmp" || { rm -f "$tmp"; cf_report apply "$tool" 1 "could not mark Claude executable"; return 0; }
  mv -f "$tmp" "$dest" || { rm -f "$tmp"; cf_report apply "$tool" 1 "could not publish Claude version"; return 0; }
  prev=
  if [ -L "$live" ]; then
    prev=$(readlink "$live" || true)
    if ! cf_atomic_link "$live" "$dest"; then
      cf_report apply "$tool" 1 "could not retarget claude symlink"
      return 0
    fi
  else
    real=$(resolve "$live")
    if ! cf_atomic_replace "$real" "$dest"; then
      cf_report apply "$tool" 1 "could not replace Claude binary"
      return 0
    fi
  fi
  got=$(cf_cmd_semver "$live" --version)
  if [ "$got" != "$ver" ]; then
    if [ -n "$prev" ]; then
      cf_atomic_link "$live" "$prev" || true
    fi
    cf_report apply "$tool" 1 "staged Claude did not report $ver; previous binary restored"
    return 0
  fi
  cf_report apply "$tool" 0 "$dest"
}

apply_release_agent() {
  cf_apply_cursor_agent "$1"
}

apply_release_cursor_agent() {
  cf_apply_cursor_agent "$1"
}

cf_apply_cursor_agent() {
  tool=$1
  cf_read_release_stamp "$tool" || return 0
  ver=$cf_rel_a
  archive=$cf_rel_b
  case $ver in
    [0-9][0-9][0-9][0-9].[0-9][0-9].[0-9][0-9]-[A-Za-z0-9]*) ;;
    *) cf_report apply "$tool" 1 "staged Cursor Agent version is not a release id"; return 0 ;;
  esac
  case $archive in
    *..*) cf_report apply "$tool" 1 "refusing archive path"; return 0 ;;
  esac
  if [ ! -f "$archive" ] || [ -L "$archive" ]; then
    cf_report apply "$tool" 1 "staged Cursor Agent archive missing"
    return 0
  fi
  live=$(command -v "$tool" 2>/dev/null || true)
  if [ -z "$live" ]; then
    cf_report apply "$tool" 1 "$tool is not on PATH"
    return 0
  fi
  live_line=$(cf_cap 10 "$cf_dir/cursor-live" "$live" --version && head -n 1 "$cf_dir/cursor-live" || true)
  live_tok=$(printf '%s\n' "$live_line" | sed -n 's/.*\([0-9][0-9][0-9][0-9]\.[0-9][0-9]\.[0-9][0-9]-[A-Za-z0-9][A-Za-z0-9]*\).*/\1/p' | head -n 1)
  if [ -n "$live_tok" ] && [ "$live_tok" = "$ver" ]; then
    cf_report apply "$tool" current "up to date"
    return 0
  fi
  extract=$(cf_extract_archive "$archive") || { cf_report apply "$tool" 1 "could not extract Cursor Agent"; return 0; }
  bin=$(find "$extract" -type f -name cursor-agent 2>/dev/null | head -n 1)
  if [ -z "$bin" ]; then
    cf_report apply "$tool" 1 "staged Cursor Agent archive has no cursor-agent"
    return 0
  fi
  versions="${HOME}/.local/share/cursor-agent/versions"
  mkdir -p "$versions" || { cf_report apply "$tool" 1 "could not create Cursor Agent versions dir"; return 0; }
  stage="$versions/.tmp-$ver"
  final="$versions/$ver"
  rm -rf "$stage"
  mkdir -p "$stage" || { cf_report apply "$tool" 1 "could not stage Cursor Agent"; return 0; }
  bindir=$(dirname "$bin")
  cp -a "$bindir"/. "$stage"/ || { rm -rf "$stage"; cf_report apply "$tool" 1 "could not copy Cursor Agent"; return 0; }
  chmod 755 "$stage/cursor-agent" 2>/dev/null || true
  if [ -e "$final" ] || [ -L "$final" ]; then
    rm -rf "$final"
  fi
  if ! mv "$stage" "$final"; then
    rm -rf "$stage"
    cf_report apply "$tool" 1 "could not publish Cursor Agent version"
    return 0
  fi
  prev_agent=
  prev_cursor=
  prev_live=
  if [ -L "${HOME}/.local/bin/agent" ]; then
    prev_agent=$(readlink "${HOME}/.local/bin/agent" || true)
    cf_atomic_link "${HOME}/.local/bin/agent" "$final/cursor-agent" || true
  fi
  if [ -L "${HOME}/.local/bin/cursor-agent" ]; then
    prev_cursor=$(readlink "${HOME}/.local/bin/cursor-agent" || true)
    cf_atomic_link "${HOME}/.local/bin/cursor-agent" "$final/cursor-agent" || true
  fi
  if [ -L "$live" ]; then
    prev_live=$(readlink "$live" || true)
    cf_atomic_link "$live" "$final/cursor-agent" || {
      cf_report apply "$tool" 1 "could not retarget $tool symlink"
      return 0
    }
  elif [ ! -L "${HOME}/.local/bin/$tool" ]; then
    real=$(resolve "$live")
    if ! cf_atomic_replace "$real" "$final/cursor-agent"; then
      cf_report apply "$tool" 1 "could not replace $tool"
      return 0
    fi
  fi
  got_line=$(cf_cap 10 "$cf_dir/cursor-new" "$live" --version && head -n 1 "$cf_dir/cursor-new" || true)
  got_tok=$(printf '%s\n' "$got_line" | sed -n 's/.*\([0-9][0-9][0-9][0-9]\.[0-9][0-9]\.[0-9][0-9]-[A-Za-z0-9][A-Za-z0-9]*\).*/\1/p' | head -n 1)
  if [ "$got_tok" != "$ver" ]; then
    if [ -n "$prev_live" ]; then cf_atomic_link "$live" "$prev_live" || true; fi
    if [ -n "$prev_agent" ]; then cf_atomic_link "${HOME}/.local/bin/agent" "$prev_agent" || true; fi
    if [ -n "$prev_cursor" ]; then cf_atomic_link "${HOME}/.local/bin/cursor-agent" "$prev_cursor" || true; fi
    cf_report apply "$tool" 1 "staged Cursor Agent did not report $ver; previous symlink restored"
    return 0
  fi
  cf_report apply "$tool" 0 "$final/cursor-agent"
}
`

const fetchSafeShell = fetchHead + releaseRest

const applySafeShell = fetchSafeShell + applyOnlyShell

// codexPackShell is fetch-only. Apply must not contain `npm pack`.
const codexPackShell = `
cf_pack_codex_platform() {
  dest=$1
  ver=$2
  plat=$(cf_codex_npm_platform) || return 1
  case $ver in
    *[!0-9A-Za-z._+-]*) return 1 ;;
  esac
  case $plat in
    darwin-arm64|darwin-x64|linux-arm64|linux-x64) ;;
    *) return 1 ;;
  esac
  spec="@openai/codex@${ver}-${plat}"
  file=$(npm pack --loglevel=error --pack-destination "$dest" "$spec" </dev/null) || return 1
  file=$(printf '%s\n' "$file" | sed '/^[[:space:]]*$/d' | tail -n 1)
  case $file in
    /*|./*|../*|*/*) tarball=$file ;;
    *) tarball=$dest/$file ;;
  esac
  case $tarball in
    *..*) return 1 ;;
  esac
  if [ ! -f "$tarball" ] || [ -L "$tarball" ]; then
    return 1
  fi
  printf '%s\n' "$tarball"
}
`
