// Package importguard hosts CI-enforced import-graph invariants for
// the module. It is test-only — there is no runtime code here. The
// invariants are documented in CLAUDE.md §5 / §11; see the _test.go
// file for the live checks.
package importguard
