#!/bin/bash

set -e

docker login -u="$DOCKER_USERNAME" -p="$DOCKER_PASSWORD" ghcr.io
docker buildx build --pull --push -t "ghcr.io/onpier-oss/selenoid:$1" --platform linux/amd64,linux/arm64 .
