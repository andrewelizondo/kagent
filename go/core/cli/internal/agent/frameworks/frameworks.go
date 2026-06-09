package frameworks

import (
	"fmt"

	adk_python "github.com/kagent-dev/kagent/go/core/cli/internal/agent/frameworks/adk/python"
	"github.com/kagent-dev/kagent/go/core/cli/internal/agent/frameworks/aura"
)

// Generator interface for project generation
type Generator interface {
	Generate(projectDir, agentName, instruction, modelProvider, modelName, description string, verbose bool, kagentVersion string) error
}

// NewGenerator creates a new generator for the specified framework and language
func NewGenerator(framework, language string) (Generator, error) {
	switch framework {
	case "adk":
		switch language {
		case "python":
			return adk_python.NewPythonGenerator(), nil
		default:
			return nil, fmt.Errorf("unsupported language '%s' for adk", language)
		}
	case "aura":
		// AURA agents are declarative; language is not applicable.
		return aura.NewAuraGenerator(), nil
	default:
		return nil, fmt.Errorf("unsupported framework: %s", framework)
	}
}
