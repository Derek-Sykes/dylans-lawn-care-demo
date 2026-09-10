#!/bin/sh
set -eu
base=http://127.0.0.1:8080
i=0
until wget -q -O /dev/null "$base/healthz"; do
  i=$((i + 1)); [ "$i" -lt 30 ] || exit 1; sleep 1
done
wget -S -O /tmp/index "$base/" 2>/tmp/headers
grep -qi 'X-Robots-Tag:.*noindex' /tmp/headers
wget -q -O /tmp/version "$base/version.json"
grep -Fq "\"revision\":\"$DYLAN_REVISION\"" /tmp/version
cd /expected
find . -type f > /tmp/public-files
while IFS= read -r file; do
  wget -q -O /tmp/asset "$base/${file#./}"
  cmp "$file" /tmp/asset
done < /tmp/public-files
for path in .git/config .env .local/runtime.env README.md Dockerfile booking/web/index.html; do
  if wget -S -O /dev/null "$base/$path" 2>/tmp/private-headers; then
    echo "Unexpected public file: $path" >&2; exit 1
  fi
  grep -q 'HTTP/1.1 404' /tmp/private-headers
done
echo "Static package checks passed: $(wc -l < /tmp/public-files) files, revision, headers and private-file exclusions."
