#!/bin/bash
# SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
# SPDX-License-Identifier: AGPL-3.0-or-later

set -e
cd "$(dirname "$0")"

# Build if requested or binary doesn't exist
if [[ "$1" == "--build" ]] || [[ ! -f ./go_live_transcription ]]; then
    echo "Building (CGO enabled for Vosk + Opus)..."
    CGO_ENABLED=1 /usr/local/go/bin/go build -o go_live_transcription .
    echo "Build complete."
fi

# Talk hardcodes ExApp ID 'live_transcription' in LiveTranscriptionService.php
export APP_ID=live_transcription
# Fixed secret matching the Makefile registration (see `make register`)
export APP_SECRET="${APP_SECRET:-12345}"
export APP_VERSION=0.0.1
export APP_PORT=23000
export NEXTCLOUD_URL=http://nextcloud.appapi
export LT_HPB_URL=wss://talk-signaling.appapi
export LT_INTERNAL_SECRET=4567
export SKIP_CERT_VERIFY=true
export APP_PERSISTENT_STORAGE=/tmp/go_lt_data

# Ensure models directory exists (copy from Docker volume if available)
mkdir -p "$APP_PERSISTENT_STORAGE"
if [ ! -d "$APP_PERSISTENT_STORAGE/vosk-model-en-us-0.22" ]; then
    DOCKER_VOL="/var/lib/docker/volumes/nc_app_live_transcription_data/_data"
    if [ -d "$DOCKER_VOL/vosk-model-en-us-0.22" ]; then
        echo "Copying Vosk models from Docker volume..."
        sudo cp -r "$DOCKER_VOL"/vosk-model-* "$APP_PERSISTENT_STORAGE/" 2>/dev/null || true
        sudo chown -R "$(id -u):$(id -g)" "$APP_PERSISTENT_STORAGE"
        echo "Models copied."
    else
        echo "WARNING: No Vosk models found. Transcription will not work."
    fi
fi

echo "Starting go_live_transcription on :${APP_PORT}"
echo "  APP_ID=$APP_ID"
echo "  NEXTCLOUD_URL=$NEXTCLOUD_URL"
echo "  LT_HPB_URL=$LT_HPB_URL"
echo "  MODELS=$APP_PERSISTENT_STORAGE"
exec ./go_live_transcription
