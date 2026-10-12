#!/usr/bin/env bash

set -Eeuo pipefail

tag="${1:-}"
if [[ ! "${tag}" =~ ^v([0-9]+\.[0-9]+\.[0-9]+-alpha\.[0-9]+(\.security\.[0-9]+)?)$ ]]; then
  echo "Release tag must match vMAJOR.MINOR.PATCH-alpha.NUMBER or its .security.NUMBER refresh form" >&2
  exit 1
fi
version="${BASH_REMATCH[1]}"

node - "${version}" <<'NODE'
const fs = require('node:fs')

const version = process.argv[2]
const packageJson = JSON.parse(fs.readFileSync('frontend/package.json', 'utf8'))
const packageLock = JSON.parse(fs.readFileSync('frontend/package-lock.json', 'utf8'))
const dockerfile = fs.readFileSync('Dockerfile', 'utf8')
const changelog = fs.readFileSync('CHANGELOG.md', 'utf8')
const releaseGuide = fs.readFileSync('ALPHA-RELEASE.md', 'utf8')

const failures = []
if (packageJson.name !== 'varianotes-frontend' || packageLock.name !== 'varianotes-frontend') {
  failures.push('frontend package name is not varianotes-frontend')
}
if (packageJson.version !== version) {
  failures.push(`frontend/package.json is ${packageJson.version}`)
}
if (packageLock.version !== version || packageLock.packages?.['']?.version !== version) {
  failures.push('frontend/package-lock.json does not match')
}
const dockerArgs = [...dockerfile.matchAll(/^ARG VERSION=(.+)$/gm)].map((match) => match[1])
if (dockerArgs.length !== 2 || dockerArgs.some((value) => value !== version)) {
  failures.push('Dockerfile VERSION defaults do not match')
}
if (!dockerfile.includes('org.opencontainers.image.version="${VERSION}"')) {
  failures.push('Dockerfile OCI version label is not build-argument based')
}
if (!dockerfile.includes('/usr/local/bin/varianotes')) {
  failures.push('Dockerfile does not install the varianotes CLI')
}
if (!changelog.includes(`## [${version}] - `)) {
  failures.push('CHANGELOG.md has no matching release section')
}
const unreleased = changelog.match(/## \[Unreleased\][^\n]*\n([\s\S]*?)(?=\n## \[)/)?.[1]?.trim()
if (unreleased) {
  failures.push('CHANGELOG.md Unreleased section is not empty')
}
if (!releaseGuide.startsWith('# VariaNotes alpha release and recovery guide\n')) {
  failures.push('ALPHA-RELEASE.md does not identify the VariaNotes release guide')
}

if (failures.length > 0) {
  console.error(`Release metadata does not match ${version}:\n- ${failures.join('\n- ')}`)
  process.exit(1)
}
console.log(`Release metadata matches ${version}.`)
NODE
