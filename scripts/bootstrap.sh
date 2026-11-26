#!/usr/bin/env bash
set -euo pipefail

# Install the pinned build tools under a caller-selected directory.
tool_prefix="${POML_TOOL_PREFIX:-$PWD/.tools}"
go_version="1.27.4"
protoc_version="36.2"
protobuf_go_version="v1.36.10"
go_sha256="7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840"
protoc_sha256="121f6c7afe1d4d0e3ea6aab9432038599250134cbf4474cb1167d2c7decd4278"

mkdir -p "$tool_prefix/cache" "$tool_prefix/go" "$tool_prefix/protoc" "$tool_prefix/bin"
go_archive="$tool_prefix/cache/go${go_version}.linux-amd64.tar.gz"
protoc_archive="$tool_prefix/cache/protoc-${protoc_version}-linux-x86_64.zip"

if [ ! -x "$tool_prefix/go/bin/go" ]; then
  curl -fL --retry 3 -o "$go_archive" "https://go.dev/dl/go${go_version}.linux-amd64.tar.gz"
  printf '%s  %s\n' "$go_sha256" "$go_archive" | sha256sum -c -
  tar -C "$tool_prefix/go" --strip-components=1 -xzf "$go_archive"
fi

if [ ! -x "$tool_prefix/protoc/bin/protoc" ]; then
  curl -fL --retry 3 -o "$protoc_archive" "https://github.com/protocolbuffers/protobuf/releases/download/v${protoc_version}/protoc-${protoc_version}-linux-x86_64.zip"
  printf '%s  %s\n' "$protoc_sha256" "$protoc_archive" | sha256sum -c -
  unzip -q -o "$protoc_archive" -d "$tool_prefix/protoc"
fi

PATH="$tool_prefix/go/bin:$tool_prefix/bin:$PATH" GOBIN="$tool_prefix/bin" \
  "$tool_prefix/go/bin/go" install "google.golang.org/protobuf/cmd/protoc-gen-go@${protobuf_go_version}"

printf 'Go: '
"$tool_prefix/go/bin/go" version
printf 'protoc: '
"$tool_prefix/protoc/bin/protoc" --version
printf 'protoc-gen-go: '
"$tool_prefix/bin/protoc-gen-go" --version
