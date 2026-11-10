#!/bin/sh
# Build on a preparation machine with Internet access; the output runs offline.
set -eu
release=${1:?Usage: build-offline.sh RELEASE OUTPUT_DIR [PLATFORM]}
output=${2:?Usage: build-offline.sh RELEASE OUTPUT_DIR [PLATFORM]}
platform=${3:-}
case "$release" in
    [a-zA-Z0-9_]*) ;;
    *) echo "Invalid release tag: $release" >&2; exit 1 ;;
esac
case "$release" in
    *[!a-zA-Z0-9_.-]*) echo "Invalid release tag: $release" >&2; exit 1 ;;
esac
[ "${#release}" -le 128 ] || { echo 'Release tag is too long' >&2; exit 1; }
case "$platform" in
    ""|linux/amd64|linux/arm64) ;;
    *) echo "Unsupported platform: $platform" >&2; exit 1 ;;
esac
[ ! -e "$output" ] || { echo "Output path already exists: $output" >&2; exit 1; }
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
[ -s "$repo/protocol/proto/metrics.proto" ] || {
    echo 'Initialize the protocol submodule before building' >&2; exit 1;
}
native_platform=$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}')
platform=${platform:-$native_platform}
case "$platform" in
    linux/amd64|linux/arm64) ;;
    *) echo "Unsupported platform: $platform" >&2; exit 1 ;;
esac
printf 'Building for %s (Docker server: %s)\n' "$platform" "$native_platform"
mkdir -p "$(dirname -- "$output")"
destination="$(CDPATH= cd -- "$(dirname -- "$output")" && pwd)/$(basename -- "$output")"
staging=$(mktemp -d "$(dirname -- "$destination")/.timesync-bundle.XXXXXX")
trap 'rm -rf "$staging"' 0
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
output=$staging
cd "$repo"

db_image=${TIMESCALE_IMAGE:-timescale/timescaledb:latest-pg16}
renderer_image=${RENDERER_IMAGE:-grafana/grafana-image-renderer:latest}
grafana_image=${GRAFANA_BASE_IMAGE:-grafana/grafana:latest}
docker buildx build --load --pull --platform "$platform" -t "timesync-analyzer:$release" .
docker buildx build --load --pull --platform "$platform" -f deploy/Dockerfile.grafana \
    --build-arg "GRAFANA_BASE_IMAGE=$grafana_image" \
    --build-arg "FORM_PANEL_VERSION=${FORM_PANEL_VERSION:-}" \
    --build-arg "INFINITY_VERSION=${INFINITY_VERSION:-}" \
    -t "timesync-grafana:$release" .
docker pull --platform "$platform" "$db_image"
docker tag "$db_image" "timesync-db:$release"
docker pull --platform "$platform" "$renderer_image"
docker tag "$renderer_image" "timesync-renderer:$release"

set -- "timesync-analyzer:$release" "timesync-grafana:$release" \
    "timesync-db:$release" "timesync-renderer:$release"
docker image inspect "$@" > "$output/images.json"
docker save -o "$output/images.tar" "$@"
docker run --rm --platform "$platform" --network none --entrypoint /bin/sh "timesync-grafana:$release" \
    -c 'cat /opt/grafana-plugins/volkovlabs-form-panel/plugin.json' > "$output/form-panel.json"
docker run --rm --platform "$platform" --network none --entrypoint /bin/sh "timesync-grafana:$release" \
    -c 'cat /opt/grafana-plugins/yesoreyeram-infinity-datasource/plugin.json' > "$output/infinity.json"
cp deploy/compose.yaml "$output/compose.yaml"
cp config/config.yaml "$output/config.yaml"
sed "s/^RELEASE=.*/RELEASE=$release/" deploy/.env.example > "$output/.env.example"
cp docs/OFFLINE_DEPLOYMENT.md "$output/OFFLINE_DEPLOYMENT.md"
printf 'release=%s\nplatform=%s\ntimescale_source=%s\nrenderer_source=%s\ngrafana_source=%s\n' \
    "$release" "$platform" "$db_image" "$renderer_image" "$grafana_image" > "$output/build-info.txt"
git rev-parse HEAD >> "$output/build-info.txt"
git status --short >> "$output/build-info.txt"
git -C protocol rev-parse HEAD >> "$output/build-info.txt"
cd "$output"
if command -v sha256sum >/dev/null 2>&1; then
    sha256sum images.tar images.json compose.yaml config.yaml .env.example \
        OFFLINE_DEPLOYMENT.md build-info.txt form-panel.json infinity.json > SHA256SUMS
else
    shasum -a 256 images.tar images.json compose.yaml config.yaml .env.example \
        OFFLINE_DEPLOYMENT.md build-info.txt form-panel.json infinity.json > SHA256SUMS
fi
cd "$repo"
[ ! -e "$destination" ] || { echo "Output path already exists: $destination" >&2; exit 1; }
mv "$staging" "$destination"
trap - 0 HUP INT TERM
printf 'Offline bundle: %s\n' "$destination"
