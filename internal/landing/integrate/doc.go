// Package integrate refreshes Build bases and repairs verified candidates
// after the target branch moves. Git effects stay behind the supervised Git
// port; publication to the target branch remains a compare-and-swap owned by
// landing/publish.
package integrate
