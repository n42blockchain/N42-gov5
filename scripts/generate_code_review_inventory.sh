#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
OUT="${1:-"$ROOT/docs/engineering/CODE_REVIEW_FILE_INVENTORY.md"}"
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT

cd "$ROOT"

group_for() {
	local path="$1"
	case "$path" in
		AGENTS.md|README.md|Makefile|go.mod|go.sum|Dockerfile|Dockerfile.release|LICENSE|SECURITY.md|VERSION|interfaces.go|qodana.yaml|CLAUDE.md|.github/*|.golangci.yml|.goreleaser.yml|.gitignore|.dockerignore)
			printf '00\tRoot, Governance, and CI\n'
			;;
		cmd/*|accounts/*|console/*)
			printf '01\tEntry Points and Operator UX\n'
			;;
		api/*)
			printf '02\tProtocol Schemas and Public Interfaces\n'
			;;
		conf/*|params/*|sdk)
			printf '03\tConfiguration and Network Parameters\n'
			;;
		common/*)
			printf '04\tShared Domain Primitives\n'
			;;
		internal/*)
			printf '05\tNode Runtime and Core Services\n'
			;;
		modules/*)
			printf '06\tShared State, DB, and RPC Modules\n'
			;;
		lib/*)
			printf '07\tEmbedded Libraries and Externalized Subsystems\n'
			;;
		contracts/*)
			printf '08\tSmart Contracts and Generated Bindings\n'
			;;
		tests/*|benchmarks/*|tools/*|turbo/*|tpsbench|tps_benchmark_results.txt)
			printf '09\tTests, Benchmarks, and Research\n'
			;;
		docs/*|deployments/*|scripts/*|pkg/*|utils/*|log/*)
			printf '10\tOperations, Tooling, and Documentation\n'
			;;
		*)
			printf '11\tUnclassified\n'
			;;
	esac
}

module_for() {
	local path="$1"
	case "$path" in
		*/*/*)
			case "$path" in
				internal/*|modules/*|common/*|lib/*|cmd/*|api/*|contracts/*|tests/*|tools/*|accounts/*|docs/*|deployments/*|log/*)
					printf '%s/%s\n' "${path%%/*}" "$(printf '%s' "${path#*/}" | cut -d/ -f1)"
					;;
				*)
					printf '%s\n' "${path%%/*}"
					;;
			esac
			;;
		*/*)
			printf '%s\n' "${path%%/*}"
			;;
		*)
			printf 'root\n'
			;;
	esac
}

sort_score() {
	local path="$1"
	case "$path" in
		*_test.go)
			printf '2'
			;;
		*.go)
			printf '1'
			;;
		*.md|*.txt|*.rst|*.proto)
			printf '3'
			;;
		*)
			printf '4'
			;;
	esac
}

git ls-files -co --exclude-standard |
	rg -v '^(devtest|mainnet|n42data|build/|bin/|coverage$|\.codex-cache/|\.claude/)' |
	while IFS= read -r path; do
		group_info="$(group_for "$path")"
		group_key="$(printf '%s' "$group_info" | cut -f1)"
		group_title="$(printf '%s' "$group_info" | cut -f2)"
		module="$(module_for "$path")"
		score="$(sort_score "$path")"
		printf '%s\t%s\t%s\t%s\t%s\n' "$group_key" "$group_title" "$module" "$score" "$path"
	done |
	sort -t $'\t' -k1,1 -k3,3 -k4,4 -k5,5 >"$TMP"

total_files="$(wc -l <"$TMP" | tr -d ' ')"
go_files="$(awk -F '\t' '$5 ~ /\.go$/ { count++ } END { print count + 0 }' "$TMP")"
go_test_files="$(awk -F '\t' '$5 ~ /_test\.go$/ { count++ } END { print count + 0 }' "$TMP")"

mkdir -p "$(dirname "$OUT")"

{
	printf '# Code Review File Inventory\n\n'
	printf 'Generated on `%s` from `%s`.\n\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$(basename "$0")"
	printf 'Excluded paths: `devtest/`, `mainnet/`, `n42data/`, `build/`, `bin/`, `coverage`, `.codex-cache/`, `.claude/`.\n\n'
	printf '## Summary\n\n'
	printf -- '- Total files: `%s`\n' "$total_files"
	printf -- '- Go source files: `%s`\n' "$go_files"
	printf -- '- Go test files: `%s`\n' "$go_test_files"
	printf '\n## Top-Level Distribution\n\n'
	awk -F '\t' '{ split($5, parts, "/"); counts[parts[1]]++ } END { for (name in counts) printf "%s\t%d\n", name, counts[name] }' "$TMP" |
		sort -k2,2nr -k1,1 |
		while IFS=$'\t' read -r name count; do
			printf -- '- `%s`: %s\n' "$name" "$count"
		done

	current_group=''
	current_module=''

	printf '\n## Functional Modules\n\n'
	while IFS=$'\t' read -r group_key group_title module score path; do
		if [ "$group_key" != "$current_group" ]; then
			group_count="$(awk -F '\t' -v key="$group_key" '$1 == key { count++ } END { print count + 0 }' "$TMP")"
			printf '### %s. %s (%s files)\n\n' "$group_key" "$group_title" "$group_count"
			current_group="$group_key"
			current_module=''
		fi

		if [ "$module" != "$current_module" ]; then
			module_count="$(awk -F '\t' -v key="$group_key" -v module_name="$module" '$1 == key && $3 == module_name { count++ } END { print count + 0 }' "$TMP")"
			printf '#### `%s` (%s files)\n\n' "$module" "$module_count"
			current_module="$module"
		fi

		printf -- '- `%s`\n' "$path"
	done <"$TMP"
} >"$OUT"

printf 'Wrote %s\n' "$OUT"
