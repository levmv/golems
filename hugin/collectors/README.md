# Hugin Bundled Collectors

These collectors are intentionally small Linux shell scripts. They gather facts,
print one strict JSON object to stdout, and leave judgment to Hugin's LLM
analysis.

Recommended remote install path:

```bash
/opt/hugin/collectors
```

Copy this whole directory to the monitored host and run scripts from there so
the shared `lib.sh` helper is available.

`hugin deploy <target>` installs this bundled directory for an SSH target. The
install also includes `hugin-collector-wrapper`, a small forced-command helper
for restricting the SSH key to Hugin collectors only. A minimal
`authorized_keys` entry looks like this:

```text
restrict,command="/opt/hugin/collectors/hugin-collector-wrapper" ssh-ed25519 AAAA... hugin
```

The wrapper accepts plain commands of the form:

```bash
HUGIN_CHECK_ID=disk_web1 HUGIN_DISK_PATH=/ /opt/hugin/collectors/disk
```

It only allows `HUGIN_*` environment assignments and bundled collector names,
and expects absolute collector paths in check commands. If you install
collectors somewhere else, set `HUGIN_COLLECTOR_DIR` before the wrapper in the
forced command:

```text
restrict,command="HUGIN_COLLECTOR_DIR=/home/hugin/collectors /home/hugin/collectors/hugin-collector-wrapper" ssh-ed25519 AAAA... hugin
```

## Contract

- stdout must be a single JSON object.
- `check` must match the configured Hugin check ID.
- `status` is the collection status: `ok`, `partial`, or `error`.
- `metrics` values must be scalar JSON values only: numbers, strings, or bools.
- collector failures should use `status: "error"` with an `errors` array.
- stderr is for diagnostics only; Hugin reads JSON from stdout.

The bundled scripts use `HUGIN_CHECK_ID` so one script can back many checks:

```yaml
checks:
  - id: root_disk_web1
    target: web1
    command: HUGIN_CHECK_ID=root_disk_web1 HUGIN_DISK_PATH=/ /opt/hugin/collectors/disk
    schedule: "*/15 * * * *"
    timeout: 10s
```

## Scripts

`disk`

Reports usage for one filesystem path. Set `HUGIN_DISK_PATH`; default is `/`.

```bash
HUGIN_CHECK_ID=root_disk HUGIN_DISK_PATH=/ /opt/hugin/collectors/disk
```

`memory`

Reports memory and swap facts from `/proc/meminfo`.

```bash
HUGIN_CHECK_ID=memory /opt/hugin/collectors/memory
```

`load`

Reports Linux load average, process counts, and CPU count.

```bash
HUGIN_CHECK_ID=load /opt/hugin/collectors/load
```

`systemd-service`

Reports systemd unit state. Set `HUGIN_SYSTEMD_UNIT` or pass the unit as the
first argument.

```bash
HUGIN_CHECK_ID=nginx_service HUGIN_SYSTEMD_UNIT=nginx.service /opt/hugin/collectors/systemd-service
```

`network`

Checks whether a TCP host and port are reachable using Bash `/dev/tcp`. Set
`HUGIN_HOST`, `HUGIN_PORT`, and optionally `HUGIN_TIMEOUT_SEC`.

```bash
HUGIN_CHECK_ID=site_https HUGIN_HOST=example.com HUGIN_PORT=443 /opt/hugin/collectors/network
```
