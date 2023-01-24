#!/bin/bash
shell_dir=$(dirname $0)
cd ${shell_dir}

# check params
if [[ ! $1 ]]; then
  echo "image tag is null"
  exit 1
else
  echo "image tag: $1"
fi

cd ../../

if [[ $2 == "buildx" ]]; then
  docker buildx build -t $1 -f build/docker/Dockerfile --platform=linux/amd64,linux/arm64,linux/ppc64le,linux/s390x,linux/arm/v7 . --push
else
  docker build -t $1 -f build/docker/Dockerfile .
fi
