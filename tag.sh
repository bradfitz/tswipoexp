#!/usr/bin/env bash
# tag.sh creates the SSH-signed tag for a new release but does not
# push it. See RELEASING.md for the overall release process.
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <version>   (e.g. v0.1.0)" >&2
    exit 1
fi

# Accept both "0.1.0" and "v0.1.0" and canonicalize to "v0.1.0".
v="${1#v}"
if [[ ! "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
    echo "tag.sh: invalid version \"$1\"; want something like v0.1.0" >&2
    exit 1
fi
tag="v$v"

if ! git config --get user.signingkey >/dev/null; then
    echo "tag.sh: git user.signingkey is not set; set it to your SSH public key with:" >&2
    echo "  git config --global user.signingkey ~/.ssh/id_ed25519.pub" >&2
    exit 1
fi

# The remote is the source of truth for whether a release exists, so
# always ask it rather than trusting possibly-stale local refs. A tag
# that exists only locally was never pushed and is fine to replace.
echo "Checking whether $tag already exists on origin..."
if [[ -n "$(git ls-remote --tags origin "refs/tags/$tag")" ]]; then
    echo "tag.sh: tag $tag already exists on origin" >&2
    exit 1
fi

if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
    echo "Replacing existing unpushed local tag $tag."
fi

git -c gpg.format=ssh tag -s -f -m "tswipoexp $tag" "$tag"

echo "Created signed tag $tag. To push it and start the release:"
echo "  git push origin $tag"
