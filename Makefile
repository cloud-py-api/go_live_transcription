# SPDX-FileCopyrightText: 2026 Nextcloud GmbH and Nextcloud contributors
# SPDX-License-Identifier: AGPL-3.0-or-later
.DEFAULT_GOAL := help

APP_ID := live_transcription
APP_NAME := Live Transcription (Go)
APP_VERSION := 0.0.1
APP_SECRET := 12345
APP_PORT := 23000
JSON_INFO := "{\"id\":\"$(APP_ID)\",\"name\":\"$(APP_NAME)\",\"daemon_config_name\":\"manual_daemon\",\"version\":\"$(APP_VERSION)\",\"secret\":\"$(APP_SECRET)\",\"port\":$(APP_PORT)}"

.PHONY: help
help:
	@echo "  Welcome to $(APP_NAME) $(APP_VERSION)!"
	@echo " "
	@echo "  Please use \`make <target>\` where <target> is one of"
	@echo " "
	@echo "  build             builds the Go binary (CGO enabled for Vosk + Opus)"
	@echo "  run               builds and runs the ExApp locally"
	@echo " "
	@echo "  > Docker commands:"
	@echo " "
	@echo "  docker-build      builds the CPU Docker image (MKL)"
	@echo "  docker-build-cuda builds the CUDA GPU Docker image"
	@echo "  build-push        builds and pushes CPU image to ghcr.io"
	@echo "  build-push-cuda   builds and pushes CUDA image to ghcr.io"
	@echo " "
	@echo "  > Commands for manual registration (ExApp should be running first!):"
	@echo " "
	@echo "  register          registers the ExApp into manual_daemon on master"
	@echo "  unregister        unregisters the ExApp from master"
	@echo "  register33        registers the ExApp on stable33"
	@echo "  register32        registers the ExApp on stable32"

.PHONY: build
build:
	CGO_ENABLED=1 /usr/local/go/bin/go build -o go_live_transcription .

.PHONY: run
run: build
	APP_ID=$(APP_ID) \
	APP_SECRET=$(APP_SECRET) \
	APP_VERSION=$(APP_VERSION) \
	APP_PORT=$(APP_PORT) \
	NEXTCLOUD_URL=http://nextcloud.appapi \
	LT_HPB_URL=wss://talk-signaling.appapi \
	LT_INTERNAL_SECRET=4567 \
	SKIP_CERT_VERIFY=true \
	APP_PERSISTENT_STORAGE=persistent_storage \
	LT_LOG_LEVEL=debug \
	./go_live_transcription

.PHONY: docker-build
docker-build:
	DOCKER_BUILDKIT=1 docker build \
		--build-arg RT_IMAGE=ubuntu:22.04 \
		--build-arg HAVE_CUDA=0 \
		--build-arg KALDI_MKL=1 \
		-t $(APP_ID):dev .

.PHONY: docker-build-cuda
docker-build-cuda:
	DOCKER_BUILDKIT=1 docker build \
		--build-arg RT_IMAGE=nvidia/cuda:12.4.1-devel-ubuntu22.04 \
		--build-arg HAVE_CUDA=1 \
		--build-arg KALDI_MKL=0 \
		-t $(APP_ID):dev-cuda .

.PHONY: build-push
build-push:
	DOCKER_BUILDKIT=1 docker buildx build --push --platform linux/amd64 \
		--build-arg RT_IMAGE=ubuntu:22.04 \
		--build-arg HAVE_CUDA=0 \
		--build-arg KALDI_MKL=1 \
		--tag ghcr.io/cloud-py-api/$(APP_ID):latest \
		--tag ghcr.io/cloud-py-api/$(APP_ID):$(APP_VERSION) .

.PHONY: build-push-cuda
build-push-cuda:
	DOCKER_BUILDKIT=1 docker buildx build --push --platform linux/amd64 \
		--build-arg RT_IMAGE=nvidia/cuda:12.4.1-devel-ubuntu22.04 \
		--build-arg HAVE_CUDA=1 \
		--build-arg KALDI_MKL=0 \
		--tag ghcr.io/cloud-py-api/$(APP_ID):$(APP_VERSION)-cuda \
		--tag ghcr.io/cloud-py-api/$(APP_ID):latest-cuda .

.PHONY: register
register:
	docker exec appapi-nextcloud-1 sudo -u www-data php occ app_api:app:unregister $(APP_ID) --silent --force || true
	docker exec appapi-nextcloud-1 sudo -u www-data php occ app_api:app:register $(APP_ID) manual_daemon --json-info $(JSON_INFO) --force-scopes

.PHONY: unregister
unregister:
	docker exec appapi-nextcloud-1 sudo -u www-data php occ app_api:app:unregister $(APP_ID) --silent --force || true

.PHONY: register33
register33:
	docker exec appapi-stable33-1 sudo -u www-data php occ app_api:app:unregister $(APP_ID) --silent --force || true
	docker exec appapi-stable33-1 sudo -u www-data php occ app_api:app:register $(APP_ID) manual_daemon --json-info $(JSON_INFO) --force-scopes

.PHONY: register32
register32:
	docker exec appapi-stable32-1 sudo -u www-data php occ app_api:app:unregister $(APP_ID) --silent --force || true
	docker exec appapi-stable32-1 sudo -u www-data php occ app_api:app:register $(APP_ID) manual_daemon --json-info $(JSON_INFO) --force-scopes
