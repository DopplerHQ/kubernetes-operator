#!/bin/bash
set -euo pipefail

NOSEC_RULES="G601"

function is_gnu_sed(){
  sed --version >/dev/null 2>&1
}

while IFS= read -r -d '' file
do
  sed_command="1s;^;/* #nosec $NOSEC_RULES */\n;"
  if is_gnu_sed; then
    sed -i "$sed_command" "$file"
  else
    sed -i "" "$sed_command" "$file"
  fi
done < <(find . -iname '*zz_generated*' -print0)
