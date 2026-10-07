package recipe

import (
	"fmt"
	"strings"
)

const PlanFormat = "Difficulty: easy|hard\n## Acceptance criteria\n## Technical approach\n## Implementation steps"

type Difficulty string

const (
	Easy Difficulty = "easy"
	Hard Difficulty = "hard"
)

type Plan struct {
	Difficulty Difficulty
	Text       string
	WordCount  int
}

// ParsePlan validates the planner's response format and counts whitespace-
// separated words in both the response and its wrapper text.
func ParsePlan(response, wrapper string, maxWords int) (Plan, error) {
	if maxWords < MinPlanMaxWords || maxWords > MaxPlanMaxWords {
		return Plan{}, fmt.Errorf("plan_max_words must be an integer from %d to %d", MinPlanMaxWords, MaxPlanMaxWords)
	}
	lines := strings.Split(response, "\n")
	if len(lines) == 0 {
		return Plan{}, fmt.Errorf("plan must start with Difficulty: easy or Difficulty: hard")
	}
	first := strings.TrimSuffix(lines[0], "\r")
	var difficulty Difficulty
	switch first {
	case "Difficulty: easy":
		difficulty = Easy
	case "Difficulty: hard":
		difficulty = Hard
	default:
		return Plan{}, fmt.Errorf("plan must start with Difficulty: easy or Difficulty: hard")
	}

	headings := []string{"## Acceptance criteria", "## Technical approach", "## Implementation steps"}
	nextHeading := 0
	for _, line := range lines[1:] {
		line = strings.TrimSuffix(line, "\r")
		if nextHeading < len(headings) && strings.TrimSpace(line) == headings[nextHeading] {
			nextHeading++
			continue
		}
		for index := nextHeading + 1; index < len(headings); index++ {
			if strings.TrimSpace(line) == headings[index] {
				return Plan{}, fmt.Errorf("plan headings must appear in the order %s", strings.Join(headings, ", "))
			}
		}
	}
	if nextHeading != len(headings) {
		return Plan{}, fmt.Errorf("plan must include headings %s", strings.Join(headings, ", "))
	}
	words := len(strings.Fields(wrapper + "\n" + response))
	if words > maxWords {
		return Plan{}, fmt.Errorf("plan has %d words including wrapper text; maximum is %d", words, maxWords)
	}
	return Plan{Difficulty: difficulty, Text: response, WordCount: words}, nil
}
