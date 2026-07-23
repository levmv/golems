package ui

import toolruntime "github.com/levmv/golems/cy/internal/tools"

type fileChangeMeta = toolruntime.FileChangeMeta

func fileChangeMetaFrom(value any) (fileChangeMeta, bool) {
	return toolruntime.FileChangeMetaFrom(value)
}
