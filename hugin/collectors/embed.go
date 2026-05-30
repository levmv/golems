package collectors

import "embed"

// File describes one bundled collector asset and the mode it should have after deployment.
type File struct {
	Name string
	Mode int64
}

// Files is the manifest of bundled Linux collector scripts shipped with Hugin.
var Files = []File{
	{Name: "README.md", Mode: 0644},
	{Name: "lib.sh", Mode: 0644},
	{Name: "disk", Mode: 0755},
	{Name: "load", Mode: 0755},
	{Name: "memory", Mode: 0755},
	{Name: "network", Mode: 0755},
	{Name: "systemd-service", Mode: 0755},
	{Name: "hugin-collector-wrapper", Mode: 0755},
}

// FS contains the bundled Linux collector scripts shipped with Hugin.
//
//go:embed README.md disk hugin-collector-wrapper lib.sh load memory network systemd-service
var FS embed.FS
