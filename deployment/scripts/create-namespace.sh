#!/bin/sh
set -eu

namespace="default"
retention="24h"

if temporal operator namespace describe --namespace "${namespace}" >/dev/null 2>&1; then
  exit 0
fi

temporal operator namespace create \
  --namespace "${namespace}" \
  --retention "${retention}"
