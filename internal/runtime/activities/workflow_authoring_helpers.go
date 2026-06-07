package activities

import (
	"fmt"

	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
)

func (a *Activities) workflowAuthoringExecutor() (scheduletool.AuthoringExecutor, error) {
	if a.Schedule == nil {
		return nil, fmt.Errorf("workflow schedule executor is not configured")
	}
	executor, ok := a.Schedule.(scheduletool.AuthoringExecutor)
	if !ok {
		return nil, fmt.Errorf("workflow schedule executor does not support agent authoring")
	}
	return executor, nil
}
