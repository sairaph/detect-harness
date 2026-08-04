// Package detectharness detects AI harnesses and safely manages stdio MCP
// server registrations across their native configuration formats.
//
// Configuration can be managed in two scopes: the zero-value global scope
// (system/user configuration) and a project scope that targets a
// directory-local configuration for harnesses that support per-project MCP
// overrides. Use Scope and ProjectScopeDir to select a scope.
package detectharness
