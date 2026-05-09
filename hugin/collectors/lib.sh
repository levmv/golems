#!/usr/bin/env bash

json_string() {
	local value=${1-}
	awk 'BEGIN {
		s = ARGV[1]
		ARGV[1] = ""
		gsub(/\\/, "\\\\", s)
		gsub(/"/, "\\\"", s)
		gsub(/\t/, "\\t", s)
		gsub(/\r/, "\\r", s)
		gsub(/\n/, "\\n", s)
		printf "\"%s\"", s
	}' "$value"
}

collector_check_id() {
	local fallback=$1
	printf '%s' "${HUGIN_CHECK_ID:-$fallback}"
}

emit_error() {
	local check=$1
	local code=$2
	local message=$3

	printf '{"check":%s,"status":"error","errors":[{"code":%s,"message":%s}]}\n' \
		"$(json_string "$check")" \
		"$(json_string "$code")" \
		"$(json_string "$message")"
}
