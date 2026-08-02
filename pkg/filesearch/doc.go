// Package filesearch provides rooted file enumeration and text search.
//
// Paths accepted and returned by the package are relative to the search root.
// Recursive traversal does not follow symbolic links and always excludes Git
// metadata directories. Repository ignore rules are local to the search root;
// parent and user-global ignore files are never read.
package filesearch
