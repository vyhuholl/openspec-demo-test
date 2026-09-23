#!/usr/bin/env bash
# Прогон приёмки по рабочей копии ветки.
#   ./judge.sh <repo> <stage 1|2> [base-ref]
# base-ref — коммит, с которого стартовал change. Если задан, скрипт покажет
# удалённые и изменённые строки в тестах, существовавших до change.
set -euo pipefail

repo=$(cd "$1" && pwd)
stage=${2:-1}
base=${3:-}
here=$(cd "$(dirname "$0")" && pwd)
dest="$repo/acceptance"

if [ -e "$dest" ]; then
  echo "$dest уже существует — убери его, прежде чем судить" >&2
  exit 1
fi
mkdir "$dest"
trap 'rm -rf "$dest"' EXIT
cp "$here"/harness_test.go "$here"/series_test.go "$here"/buffer_test.go "$dest"/

log=$(mktemp)
(cd "$dest" && ACCEPTANCE_STAGE="$stage" go test -count=1 -v . 2>&1) | tee "$log" >/dev/null || true

# Считаем только листовые тесты: родитель подтестов в счёт не идёт.
awk '
  /^ *--- (PASS|FAIL|SKIP):/ { st[$3] = $2; order[++n] = $3 }
  END {
    for (i = 1; i <= n; i++) for (j = 1; j <= n; j++)
      if (index(order[j], order[i] "/") == 1) parent[order[i]] = 1
    for (i = 1; i <= n; i++) {
      name = order[i]
      if (name in parent || done[name]++) continue
      s = st[name]; cnt[s]++
      if (s == "FAIL:") fails[++f] = name
    }
    printf "PASS %d  FAIL %d  SKIP %d\n", cnt["PASS:"], cnt["FAIL:"], cnt["SKIP:"]
    for (i = 1; i <= f; i++) print "  FAIL " fails[i]
  }' "$log"
grep -E "^(race detector|FAIL: race|go build|service not ready)" "$log" || true
echo "полный лог: $log"

if [ -n "$base" ]; then
  echo
  echo "== изменения в тестах, существовавших на $base =="
  git -C "$repo" diff --diff-filter=MD "$base" -- '*_test.go' | grep -E '^(---|\+\+\+|-[^-])' || echo "(нет)"
fi