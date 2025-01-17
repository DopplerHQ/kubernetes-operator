#!/bin/bash
set -euo pipefail

tmpDir=$(mktemp -d)

function cleanup() {
  rm -r "$tmpDir"
  echo "Deleted $tmpDir"
}
trap cleanup EXIT

# TEMP/charts will contain our new chart
mkdir "$tmpDir/charts"
cp "$1" "$tmpDir/charts"
pushd "$tmpDir"

git clone https://github.com/DopplerHQ/helm-charts.git
cd helm-charts

git config user.name "Doppler Bot"
git config user.email "support@doppler.com"

if [[ -f "index.yaml" ]]; then
  echo "Found index, merging changes"
  # Ugly workaround to preserve old file timestamps: https://github.com/helm/helm/issues/7363#issuecomment-572369872
  # Generate the index from TEMP/charts (which only contains our new chart) and merge in the existing index
  helm repo index ../charts --url https://helm.doppler.com --merge index.yaml
  # Then copy the index.yaml and the new chart to TEMP/helm-charts
  mv ../charts/* ./
else
  echo "No index found, generating a new one"
  # Copy new chart TEMP/helm-charts
  mv ../charts/* ./
  # Generate the new index in this dir with the new chart
  helm repo index . --url https://helm.doppler.com
fi

git add .
git commit -m "Publish Helm charts"
git push

popd
