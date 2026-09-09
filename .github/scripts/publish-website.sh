#!/usr/bin/env bash
set -euo pipefail

# Use this run's exact release artifacts, not a second fetch of the mutable tag.
cd "${1:-dist}"
bucket=yolomancer-website-183305290766
assets=(yolomancer-darwin-amd64 yolomancer-darwin-arm64 yolomancer-linux-amd64 yolomancer-linux-arm64 yolomancer-windows-amd64.exe yolomancer-windows-arm64.exe)
for asset in "${assets[@]}"; do
  test -s "$asset"
  expected="$(awk -v asset="$asset" '$2 == asset {print $1}' SHA256SUMS)"
  actual="$(shasum -a 256 "$asset" | awk '{print $1}')"
  [[ "$expected" =~ ^[0-9a-f]{64}$ && "$expected" == "$actual" ]] || { echo "Checksum mismatch: $asset" >&2; exit 1; }
done
for platform in darwin linux windows; do
  for arch in amd64 arm64; do
    filename=yolomancer
    if [[ "$platform" == windows ]]; then filename=yolomancer.exe; fi
    asset="yolomancer-$platform-$arch${filename#yolomancer}"
    echo "Publishing $platform/$arch/$filename"
    # A single PutObject requires no bucket listing, reads, ACLs or deletes.
    # Supply SHA256 so S3 verifies the uploaded bytes as well.
    checksum="$(openssl dgst -sha256 -binary "$asset" | openssl base64 -A)"
    aws s3api put-object --bucket "$bucket" \
      --key "downloads/$platform/$arch/$filename" --body "$asset" \
      --expected-bucket-owner 183305290766 \
      --content-type application/octet-stream \
      --content-disposition "attachment; filename=\"$filename\"" \
      --cache-control 'no-store, no-cache, max-age=0, must-revalidate' \
      --checksum-algorithm SHA256 --checksum-sha256 "$checksum" \
      --no-cli-pager > /dev/null
  done
done
