package edge

import "time"

// RecipeVersion names the fixed edge-test generation and execution recipe.
// Changing any recipe field requires a version change so both ordinary and
// parallel checks keep using the same bounded policy.
const RecipeVersion = "edge-v1"

// Recipe is the immutable resource policy shared by generation, direct test
// runs, and parallel cross-checks.
type Recipe struct {
	Version               string
	MaxRequestBytes       int
	MaxSuiteBytes         int
	MaxTests              int
	GenerationTimeout     time.Duration
	TestTimeout           time.Duration
	MaxOutputTailBytes    int
	MaxConcurrentChecks   int
	MaxParallelCandidates int
}

// SharedRecipe returns the one supported edge recipe. Callers cannot tune
// individual rungs to make an edge check easier to satisfy.
func SharedRecipe() Recipe {
	return Recipe{
		Version:               RecipeVersion,
		MaxRequestBytes:       1 << 20,
		MaxSuiteBytes:         256 << 10,
		MaxTests:              256,
		GenerationTimeout:     2 * time.Minute,
		TestTimeout:           5 * time.Minute,
		MaxOutputTailBytes:    16 << 10,
		MaxConcurrentChecks:   2,
		MaxParallelCandidates: 4,
	}
}
