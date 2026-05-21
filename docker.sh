#!/usr/bin/env bash
set -euo pipefail

IMAGE_NAME="${YOLOMANCER_DOCKER_IMAGE:-yolomancer:local}"
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AWS_DIR="${AWS_DIR:-$HOME/.aws}"
YOLOMANCER_DIR="${YOLOMANCER_DIR:-$HOME/.yolomancer}"

usage() {
  cat <<'EOF'
Usage:
  ./docker.sh build
  ./docker.sh run [yolomancer args...]
  ./docker.sh [yolomancer args...]

Examples:
  ./docker.sh
  ./docker.sh login --profile vibecoding
  ./docker.sh --login --profile vibecoding
  ./docker.sh run --debug
  ./docker.sh run resume

Environment:
  YOLOMANCER_DOCKER_IMAGE   Image tag to build/run. Default: yolomancer:local
  AWS_DIR                   Host AWS config directory. Default: ~/.aws
  YOLOMANCER_DIR            Host yolomancer config directory. Default: ~/.yolomancer
EOF
}

build_image() {
  docker build -t "$IMAGE_NAME" "$PROJECT_ROOT"
}

run_cli() {
  mkdir -p "$AWS_DIR" "$YOLOMANCER_DIR"

  local tty_args=(-i)
  if [[ -t 1 ]]; then
    tty_args=(-it)
  fi

  docker run --rm "${tty_args[@]}" \
    --user "$(id -u):$(id -g)" \
    --workdir /workspace \
    --env HOME=/home/yolomancer \
    --env AWS_CONFIG_FILE=/home/yolomancer/.aws/config \
    --env AWS_SHARED_CREDENTIALS_FILE=/home/yolomancer/.aws/credentials \
    --env YOLOMANCER_WRITABLE_ROOTS=/workspace \
    --mount "type=bind,src=$PROJECT_ROOT,dst=/workspace" \
    --mount "type=bind,src=$AWS_DIR,dst=/home/yolomancer/.aws" \
    --mount "type=bind,src=$YOLOMANCER_DIR,dst=/home/yolomancer/.yolomancer" \
    "$IMAGE_NAME" "$@"
}

command="${1:-run}"
case "$command" in
  build)
    shift
    if [[ $# -ne 0 ]]; then
      usage >&2
      exit 2
    fi
    build_image
    ;;
  run)
    shift
    if ! docker image inspect "$IMAGE_NAME" >/dev/null 2>&1; then
      build_image
    fi
    run_cli "$@"
    ;;
  help|--help|-h)
    usage
    ;;
  --login)
    shift
    if ! docker image inspect "$IMAGE_NAME" >/dev/null 2>&1; then
      build_image
    fi
    run_cli login "$@"
    ;;
  *)
    if ! docker image inspect "$IMAGE_NAME" >/dev/null 2>&1; then
      build_image
    fi
    run_cli "$@"
    ;;
esac
