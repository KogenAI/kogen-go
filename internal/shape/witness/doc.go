// Package witness owns the opt-in, throwaway Shape witness workflow.
//
// The controller treats witness adjudication as repair advice. It never
// changes gate results or makes failed acceptance items landable. Approval
// binds a green witness commit to its exact base and binary diff, and Build
// re-verification has no provider capability.
package witness
