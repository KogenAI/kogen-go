package intentapprove

import "kogen-go/internal/process"

func cloneProcessEnvironment(source process.Environment) process.Environment {
	result := make(process.Environment, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
