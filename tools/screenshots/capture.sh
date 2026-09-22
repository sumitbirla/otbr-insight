#!/usr/bin/env bash
# Regenerate the README screenshots.
#
#   ./tools/screenshots/capture.sh
#
# Needs Google Chrome, Node 22+ (for the global WebSocket the driver uses), and
# a built binary. It starts its own instance on port 8099 and its own headless
# Chrome, and leaves neither running.
#
# The instance needs an OTBR to talk to only so the page renders with a status;
# neither screenshot uses live data. Point OTBR_URL at yours, or expect the
# status pill to read offline.
set -euo pipefail

cd "$(dirname "$0")/../.."

OTBR_URL="${OTBR_URL:-http://127.0.0.1:8081}"
PORT=8099
DEBUG_PORT=9222
CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
PROFILE="$(mktemp -d)"

cleanup() {
  [[ -n "${APP_PID:-}" ]] && kill "$APP_PID" 2>/dev/null || true
  [[ -n "${CHROME_PID:-}" ]] && kill "$CHROME_PID" 2>/dev/null || true
  # Chrome is still flushing its profile the instant after SIGTERM, so a
  # removal here races it and the script exits non-zero on a successful run.
  wait "${CHROME_PID:-}" 2>/dev/null || true
  rm -rf "$PROFILE" 2>/dev/null || true
}
trap cleanup EXIT

make build >/dev/null

dist/otbr-insight --otbr-url "$OTBR_URL" --listen "127.0.0.1:$PORT" --discovery-interval 0 >/dev/null 2>&1 &
APP_PID=$!

"$CHROME" --headless=new --remote-debugging-port="$DEBUG_PORT" --user-data-dir="$PROFILE" \
  --hide-scrollbars --force-color-profile=srgb --disable-gpu about:blank >/dev/null 2>&1 &
CHROME_PID=$!

# Both need to be listening before the driver connects.
for _ in $(seq 1 30); do
  curl -sf "http://127.0.0.1:$PORT/api/v1/health" >/dev/null 2>&1 \
    && curl -sf "http://127.0.0.1:$DEBUG_PORT/json/version" >/dev/null 2>&1 && break
  sleep 0.5
done

cd tools/screenshots
SCENE=scenes/channels.js OUT=../../docs/channel-noise.png HEIGHT=745 \
  node shoot.mjs
SCENE=scenes/report.js FIXTURE=fixtures/diagnostics-export.json \
  OUT=../../docs/device-report.png HEIGHT=1120 node shoot.mjs
