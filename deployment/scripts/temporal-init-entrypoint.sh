#!/bin/sh
set -eu

attributes="opencto_project_id"
old_ifs="$IFS"
IFS=','
set -- ${attributes}
IFS="$old_ifs"

for attribute in "$@"; do
  while true; do
    output="$(temporal operator search-attribute create --name "${attribute}" --type Keyword 2>&1)" && break
    echo "${output}" | grep -qi "already exists" && break
    sleep 1
  done
done
