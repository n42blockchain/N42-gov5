#!/bin/bash
# Version bump script for N42
# Usage:
#   ./scripts/bump_version.sh build   - Increment build number (486 -> 487)
#   ./scripts/bump_version.sh minor   - Increment minor version (5.1 -> 5.2)
#   ./scripts/bump_version.sh major   - Increment major version (5 -> 6)

set -e

VERSION_FILE="params/version.go"
VERSION_TXT="VERSION"

# Read current version from version.go
MAJOR=$(grep "VersionMajor" "$VERSION_FILE" | head -1 | grep -o '[0-9]\+')
MINOR=$(grep "VersionMinor" "$VERSION_FILE" | head -1 | grep -o '[0-9]\+')
BUILD=$(grep "VersionBuild" "$VERSION_FILE" | head -1 | grep -o '[0-9]\+')

echo "Current version: $MAJOR.$MINOR.$BUILD"

case "$1" in
  build)
    NEW_BUILD=$((BUILD + 1))
    sed -i.bak "s/VersionBuild.*=.*$BUILD/VersionBuild       = $NEW_BUILD/" "$VERSION_FILE"
    echo "$MAJOR.$MINOR.$NEW_BUILD" > "$VERSION_TXT"
    echo "New version: $MAJOR.$MINOR.$NEW_BUILD"
    ;;
  minor)
    NEW_MINOR=$((MINOR + 1))
    sed -i.bak "s/VersionMinor.*=.*$MINOR/VersionMinor       = $NEW_MINOR/" "$VERSION_FILE"
    sed -i.bak "s/VersionBuild.*=.*$BUILD/VersionBuild       = 0/" "$VERSION_FILE"
    echo "$MAJOR.$NEW_MINOR.0" > "$VERSION_TXT"
    echo "New version: $MAJOR.$NEW_MINOR.0"
    ;;
  major)
    NEW_MAJOR=$((MAJOR + 1))
    sed -i.bak "s/VersionMajor.*=.*$MAJOR/VersionMajor       = $NEW_MAJOR/" "$VERSION_FILE"
    sed -i.bak "s/VersionMinor.*=.*$MINOR/VersionMinor       = 0/" "$VERSION_FILE"
    sed -i.bak "s/VersionBuild.*=.*$BUILD/VersionBuild       = 0/" "$VERSION_FILE"
    echo "$NEW_MAJOR.0.0" > "$VERSION_TXT"
    echo "New version: $NEW_MAJOR.0.0"
    ;;
  *)
    echo "Usage: $0 {build|minor|major}"
    echo "  build - Increment build number (486 -> 487)"
    echo "  minor - Increment minor version and reset build (5.1.486 -> 5.2.0)"
    echo "  major - Increment major version and reset all (5.1.486 -> 6.0.0)"
    exit 1
    ;;
esac

# Clean up backup files
rm -f "$VERSION_FILE.bak"

echo "Done!"

