// Package sdk is the contract plugins compile against.
//
// It imports only the standard library, and nothing of the dashboard: a plugin built against it
// depends on what the dashboard promises, not on how the dashboard is written. Its packages:
//
//   - capability: what a plugin declares and brings, pipelines and their interpreters first;
//   - parse: the records the dashboard's parsers hand to an interpreter;
//   - facts: what an interpreter returns;
//   - interpret: helpers shared by interpreters.
package sdk

// APIVersion is the contract's version. A plugin records the one it was built against, and the
// dashboard refuses a plugin of another major version.
const APIVersion = "1"
