package runtime

import (
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/runtime/activities"
	"github.com/opencto/opencto/internal/runtime/workflows"
)

type Worker struct {
	temporal worker.Worker
}

func NewWorker(client client.Client, taskQueue string, activities *activities.Activities) *Worker {
	w := worker.New(client, taskQueue, worker.Options{})
	w.RegisterActivityWithOptions(activities.NextAction, activity.RegisterOptions{Name: "Activities.NextAction"})
	w.RegisterActivityWithOptions(activities.ExecuteTool, activity.RegisterOptions{Name: "Activities.ExecuteTool"})
	w.RegisterActivityWithOptions(activities.StartShellProcess, activity.RegisterOptions{Name: "Activities.StartShellProcess"})
	w.RegisterActivityWithOptions(activities.CheckShellProcess, activity.RegisterOptions{Name: "Activities.CheckShellProcess"})
	w.RegisterActivityWithOptions(activities.StopShellProcess, activity.RegisterOptions{Name: "Activities.StopShellProcess"})
	w.RegisterActivityWithOptions(activities.ReadShellProcessLogs, activity.RegisterOptions{Name: "Activities.ReadShellProcessLogs"})
	w.RegisterWorkflowWithOptions(workflows.ProjectWorkflow, workflow.RegisterOptions{Name: workflows.ProjectWorkflowName})
	w.RegisterWorkflowWithOptions(workflows.TaskWorkflow, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})
	return &Worker{temporal: w}
}

func (w *Worker) Run() error {
	return w.temporal.Run(worker.InterruptCh())
}
