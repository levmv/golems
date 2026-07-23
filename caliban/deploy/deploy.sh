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
if [[ -n "${CAL_CONFIG:-}" ]]; then
	"$local_bin" check-config -config "$CAL_CONFIG"
fi
echo "Preparing Caliban for $CAL_HOST"

local_tmp="$(mktemp -d)"
tmp="$("$ssh_bin" "$CAL_HOST" "mktemp -d /tmp/caliban-deploy.XXXXXX")"
cleanup() {
	rm -rf "$local_tmp"
	"$ssh_bin" "$CAL_HOST" "rm -rf '$tmp'" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cat >"$local_tmp/remote-deploy.sh" <<'REMOTE'
#!/usr/bin/env bash
set -Eeuo pipefail

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

install_atomic() {
	local source="$1"
	local destination="$2"
	local mode="$3"
	local owner="$4"
	local file_group="$5"
	local staged="${destination}.caliban-new.$$"

	run_priv install -m "$mode" -o "$owner" -g "$file_group" "$source" "$staged"
	run_priv mv -f "$staged" "$destination"
}

if ! getent group "$group" >/dev/null; then
	run_priv groupadd --system "$group"
fi

if ! id -u "$user" >/dev/null 2>&1; then
	run_priv useradd --system --gid "$group" --home-dir "$state_dir" --no-create-home --shell /usr/sbin/nologin "$user"
fi

run_priv install -d -o "$user" -g "$group" -m 0750 "$state_dir"

config_to_check="$remote_config"
if [[ -f "$tmp/config.json" ]]; then
	config_to_check="$tmp/config.json"
elif ! run_priv test -f "$remote_config"; then
	echo "remote config is missing: $remote_config" >&2
	echo "set CAL_CONFIG for this deploy or create it on the server" >&2
	exit 1
fi

run_priv "$tmp/caliban" check-config -config "$config_to_check"

backup_dir="$tmp/backup"
mkdir -p "$backup_dir"
binary_existed=0
unit_existed=0
config_existed=0
service_was_active=0
service_was_enabled=0

if run_priv test -f "$remote_bin"; then
	binary_existed=1
	run_priv cp -p "$remote_bin" "$backup_dir/caliban"
fi
if run_priv test -f "$remote_unit"; then
	unit_existed=1
	run_priv cp -p "$remote_unit" "$backup_dir/caliban.service"
fi
if run_priv test -f "$remote_config"; then
	config_existed=1
	run_priv cp -p "$remote_config" "$backup_dir/config.json"
fi
if run_priv systemctl is-active --quiet "$service"; then
	service_was_active=1
fi
if run_priv systemctl is-enabled --quiet "$service"; then
	service_was_enabled=1
fi

rollback_needed=1
rollback() {
	local status="$1"
	trap - ERR
	set +e
	if [[ "$rollback_needed" == "1" ]]; then
		echo "Deploy failed; restoring the previous Caliban installation" >&2
		if [[ "$binary_existed" == "1" ]]; then
			run_priv cp -p "$backup_dir/caliban" "$remote_bin"
		else
			run_priv rm -f "$remote_bin"
		fi
		if [[ "$unit_existed" == "1" ]]; then
			run_priv cp -p "$backup_dir/caliban.service" "$remote_unit"
		else
			run_priv rm -f "$remote_unit"
		fi
		if [[ "$config_existed" == "1" ]]; then
			run_priv cp -p "$backup_dir/config.json" "$remote_config"
		else
			run_priv rm -f "$remote_config"
		fi
		run_priv systemctl daemon-reload
		if [[ "$service_was_enabled" == "1" ]]; then
			run_priv systemctl enable "$service" >/dev/null
		else
			run_priv systemctl disable "$service" >/dev/null 2>&1
		fi
		if [[ "$service_was_active" == "1" ]]; then
			run_priv systemctl restart "$service"
		else
			run_priv systemctl stop "$service"
		fi
	fi
	run_priv rm -rf "$backup_dir"
	exit "$status"
}
trap 'rollback "$?"' ERR

run_priv install -d -o root -g root -m 0750 "$(dirname "$remote_config")"
install_atomic "$tmp/caliban" "$remote_bin" 0755 root root
install_atomic "$tmp/caliban.service" "$remote_unit" 0644 root root
if [[ -f "$tmp/config.json" ]]; then
	install_atomic "$tmp/config.json" "$remote_config" 0600 root root
fi

run_priv systemctl daemon-reload
run_priv systemctl enable "$service" >/dev/null
run_priv systemctl restart "$service"
sleep 3
run_priv systemctl is-active --quiet "$service"
run_priv systemctl --no-pager --full status "$service" -n 20

rollback_needed=0
trap - ERR
run_priv rm -rf "$backup_dir"
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
