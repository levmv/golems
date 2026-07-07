# Local dev tasks for caliban. Run from this directory, or from the repo root
# with `make -C caliban test`. `./...` scopes to the caliban subtree.

CONFIG ?= dev-config.json

.PHONY: help build deploy inspect run test clean

help:
	@printf '%s\n' \
		'Targets:' \
		'  make test                         Run Go tests and web typecheck' \
		'  make build                        Run tests, bundle web assets, build ./caliban' \
		'  make inspect [CONFIG=...]         Print context/compaction diagnostics' \
		'  make run [CONFIG=dev-config.json] Run with a local config' \
		'  make deploy CAL_HOST=alias        Build and deploy over SSH' \
		'  make clean                        Remove local binary and embedded web assets' \
		'' \
		'Deploy variables:' \
		'  CAL_HOST    SSH host alias or user@host (required for deploy)' \
		'  CAL_CONFIG  Optional local config to install as /etc/golems/caliban.json' \
		'  CAL_SUDO    sudo command on remote host; set empty for root SSH' \
		'  CAL_SSH_TTY Force SSH TTY for sudo prompts; set 0 to disable'

test:
	go test ./...
	npm --prefix webapp run typecheck

build: test
	npm --prefix webapp run bundle
	go build -o caliban .

deploy: build
	./deploy/deploy.sh

inspect:
	go run . -config $(CONFIG) inspect-context

# Run the bot against a local (gitignored) config. Override: make run CONFIG=path
run:
	go run . -config $(CONFIG)

clean:
	rm -f caliban
	rm -rf internal/web/static
