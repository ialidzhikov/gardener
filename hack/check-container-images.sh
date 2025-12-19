#!/usr/bin/env bash
#
# SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0

set -o errexit
set -o nounset
set -o pipefail

FILE="${1:-TODO}"

echo "> Check Container Images"

readarray images < <(yq -o=j -I=0 '.images[]' $FILE)

for image in "${images[@]}"; do
  name=$(echo "$image" | yq '.name' -)
  repository=$(echo "$image" | yq '.repository' -)
  tag=$(echo "$image" | yq '.tag' -)

  if [[ "$tag" == "null" ]]; then
    continue
  fi

  echo "name: $name"
  echo "tag: $tag"
done
