#!/usr/bin/env bash
while true; do
  source .env
  gin -i \
    --build . \
    run
  echo "Command failed with exit code $?. Respawning.." >&2
  sleep 1
done
