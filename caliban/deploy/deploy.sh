#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ -z "${CAL_HOST:-}" ]]; then
	echo "CAL_HOST is required, for example: make deploy CAL_HOST=caliban-prod" >&2
	exit 2
fi

ssh_bin="${CAL_SSH:-ssh}"
scp_bin="${CAL_SCP:-scp}"
sudo_bin="${CAL_SUDO:-sudo}"

service="caliban"
user="caliban"
group="caliban"
state_dir="/var/lib/caliban"
remote_bin="/usr/local/bin/caliban"
remote_unit="/etc/systemd/system/caliban.service"
remote_config="/etc/golems/caliban.json"

local_bin="${CAL_BIN:-$root/caliban}"
local_unit="$root/deploy/caliban.service"

if [[ ! -x "$local_bin" ]]; then
	echo "local binary is missing or not executable: $local_bin" >&2
	echo "run make -C caliban build first" >&2
	exit 1
fi
if [[ ! -f "$local_unit" ]]; then
	echo "systemd unit is missing: $local_unit" >&2
	exit 1
fi
if [[ -n "${CAL_CONFIG:-}" && ! -f "$CAL_CONFIG" ]]; then
	echo "CAL_CONFIG does not exist: $CAL_CONFIG" >&2
	exit 1
fi

local_tmp="$(mktemp -d)"
tmp="$("$ssh_bin" "$CAL_HOST" "mktemp -d /tmp/caliban-deploy.XXXXXX")"
cleanup() {
	rm -rf "$local_tmp"
	"$ssh_bin" "$CAL_HOST" "rm -rf '$tmp'" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cat >"$local_tmp/remote-deploy.sh" <<'REMOTE'
#!/usr/bin/env bash
set -euo pipefail

tmp="$1"
sudo_bin="$2"
service="$3"
user="$4"
group="$5"
state_dir="$6"
remote_bin="$7"
remote_unit="$8"
remote_config="$9"

run_priv() {
	if [[ -n "$sudo_bin" ]]; then
		"$sudo_bin" "$@"
	else
		"$@"
	fi
}

if ! getent group "$group" >/dev/null; then
	run_priv groupadd --system "$group"
fi

if ! id -u "$user" >/dev/null 2>&1; then
	run_priv useradd --system --gid "$group" --home-dir "$state_dir" --no-create-home --shell /usr/sbin/nologin "$user"
fi

run_priv install -d -o "$user" -g "$group" -m 0750 "$state_dir"
run_priv install -m 0755 "$tmp/caliban" "$remote_bin"
run_priv install -m 0644 "$tmp/caliban.service" "$remote_unit"

if [[ -f "$tmp/config.json" ]]; then
	run_priv install -d -o root -g root -m 0750 "$(dirname "$remote_config")"
	run_priv install -m 0600 -o root -g root "$tmp/config.json" "$remote_config"
elif ! run_priv test -f "$remote_config"; then
	echo "remote config is missing: $remote_config" >&2
	echo "set CAL_CONFIG for this deploy or create it on the server" >&2
	exit 1
fi

run_priv systemctl daemon-reload
run_priv systemctl enable "$service" >/dev/null
run_priv systemctl restart "$service"
run_priv systemctl is-active --quiet "$service"
run_priv systemctl --no-pager --full status "$service" -n 20
REMOTE

"$scp_bin" "$local_bin" "$local_unit" "$CAL_HOST:$tmp/"
"$scp_bin" "$local_tmp/remote-deploy.sh" "$CAL_HOST:$tmp/"
if [[ -n "${CAL_CONFIG:-}" ]]; then
	"$scp_bin" "$CAL_CONFIG" "$CAL_HOST:$tmp/config.json"
fi

remote_cmd="bash $(printf "%q" "$tmp/remote-deploy.sh") $(printf "%q" "$tmp") $(printf "%q" "$sudo_bin") $(printf "%q" "$service") $(printf "%q" "$user") $(printf "%q" "$group") $(printf "%q" "$state_dir") $(printf "%q" "$remote_bin") $(printf "%q" "$remote_unit") $(printf "%q" "$remote_config")"
if [[ "${CAL_SSH_TTY:-1}" == "0" || "${CAL_SSH_TTY:-1}" == "false" ]]; then
	"$ssh_bin" "$CAL_HOST" "$remote_cmd"
else
	"$ssh_bin" -tt "$CAL_HOST" "$remote_cmd"
fi
