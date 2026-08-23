#!/usr/bin/env bash

set -e

# Default configurations (can be overridden via environment variables or CLI flags)
export PORT="${PORT:-8080}"
export TARGET_URL="${TARGET_URL:-http://localhost:4000}"
export LOG_LEVEL="${LOG_LEVEL:-INFO}"
export WINDOW_DURATION="${WINDOW_DURATION:-60s}"
export WINDOW_MAX_REQUESTS="${WINDOW_MAX_REQUESTS:-0}"
export WINDOW_MAX_TOKENS="${WINDOW_MAX_TOKENS:-0}"
export ENFORCE_LIMITS="${ENFORCE_LIMITS:-false}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "=========================================================="
echo " Starting Circuit Breaker Reverse Proxy Server..."
echo " - Listening Port       : $PORT"
echo " - Target Destination   : $TARGET_URL"
echo " - Log Level            : $LOG_LEVEL"
echo " - Sliding Window       : $WINDOW_DURATION"
echo " - Max Window Requests  : $WINDOW_MAX_REQUESTS"
echo " - Max Window Tokens    : $WINDOW_MAX_TOKENS"
echo " - Enforce Limits       : $ENFORCE_LIMITS"
echo "=========================================================="

# Build binary if not already built or run directly
if [ ! -f "./proxy-server" ]; then
    echo "Building proxy-server binary..."
    go build -o proxy-server main.go
fi

exec ./proxy-server "$@"

