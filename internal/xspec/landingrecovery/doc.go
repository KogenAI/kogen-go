// Package landingrecovery holds the production-effect fixtures for the rebase
// and recovery xspec slices. The fixtures exercise landing.Publish,
// integrate.Land, and recovery.Controller against temporary repositories; they
// do not contain a second landing or recovery policy model.
//
// The private xspec command registry is owned by the I7 integration package.
// This package remains separately testable while that registry and the shared
// migrated Quint cohort are unavailable.
package landingrecovery
