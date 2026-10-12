#!/bin/sh
set -eu

report=${1:?usage: evaluate-trivy-report.sh REPORT REFERENCE}
reference=${2:?usage: evaluate-trivy-report.sh REPORT REFERENCE}

if [ ! -s "$report" ]; then
  echo "Trivy did not produce a report for ${reference}. The scan is not clean." >&2
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
      echo "## Trivy scan failed"
      echo
      echo "No report was produced for \`${reference}\`. Treat this as a scanner or advisory-database failure, not as a clean result."
    } >> "$GITHUB_STEP_SUMMARY"
  fi
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required to evaluate the Trivy report for ${reference}. The scan is not clean." >&2
  exit 1
fi

high=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "HIGH")] | length' "$report")
critical=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "CRITICAL")] | length' "$report")
secrets=$(jq '[.Results[]?.Secrets[]?] | length' "$report")

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "## Trivy scan: \`${reference}\`"
    echo
    echo "- Critical vulnerabilities: **${critical}**"
    echo "- High vulnerabilities: **${high}**"
    echo "- Secret findings: **${secrets}**"
    echo
    if [ "$critical" -gt 0 ] || [ "$high" -gt 0 ]; then
      echo "| Target | Component | Advisory | Severity | Installed | Fixed version |"
      echo "| --- | --- | --- | --- | --- | --- |"
      jq -r '.Results[]? as $result | $result.Vulnerabilities[]? | select(.Severity == "HIGH" or .Severity == "CRITICAL") | "| `\($result.Target)` | `\(.PkgName)` | `\(.VulnerabilityID)` | \(.Severity) | `\(.InstalledVersion)` | `\(.FixedVersion // "unknown")` |"' "$report"
      echo
    fi
  } >> "$GITHUB_STEP_SUMMARY"
fi

if [ "$critical" -gt 0 ] || [ "$high" -gt 0 ] || [ "$secrets" -gt 0 ]; then
  echo "Trivy found ${critical} critical, ${high} high, and ${secrets} secret finding(s) in ${reference}." >&2
  exit 1
fi

echo "Trivy found no high/critical vulnerabilities or secrets in ${reference}."
