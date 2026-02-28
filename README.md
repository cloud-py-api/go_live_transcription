# Live Transcription in Talk (Go)

A Go implementation of [nextcloud/live_transcription](https://github.com/nextcloud/live_transcription) — real-time speech-to-text for Nextcloud Talk calls.

This is a ground-up rewrite of the original Python app, built as an AppAPI ExApp using Go, WebRTC, and Vosk for offline speech recognition.

## How it works

1. Connects to the Nextcloud High-Performance Backend (HPB) via WebSocket
2. Joins Talk calls as a subscriber and receives audio over WebRTC
3. Decodes Opus audio and feeds it to a local Vosk model for recognition
4. Sends live transcription results back to call participants via data channels

## Key dependencies

- [pion/webrtc](https://github.com/pion/webrtc) — WebRTC stack
- [alphacep/vosk-api](https://github.com/alphacep/vosk-api) — speech recognition
- [hraban/opus](https://github.com/hraban/opus) — Opus audio decoding

## Requirements

- Nextcloud 33+
- AppAPI with a registered deploy daemon
- A running HPB (standalone signaling server)

## Configuration

Set these environment variables before deployment:

| Variable             | Description                                                                    |
|----------------------|--------------------------------------------------------------------------------|
| `LT_HPB_URL`         | HPB WebSocket URL (e.g. `wss://cloud.example.com/standalone-signaling/spreed`) |
| `LT_INTERNAL_SECRET` | HPB internal secret for authentication                                         |
| `SKIP_CERT_VERIFY`   | Optional: set `true` to skip TLS verification                                  |
