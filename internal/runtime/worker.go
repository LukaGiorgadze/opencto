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
	w.RegisterActivityWithOptions(activities, activity.RegisterOptions{Name: "Activities."})
	w.RegisterWorkflowWithOptions(workflows.ProjectWorkflow, workflow.RegisterOptions{Name: workflows.ProjectWorkflowName})
	w.RegisterWorkflowWithOptions(workflows.TaskWorkflow, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})
	w.RegisterWorkflowWithOptions(workflows.ApprovalWorkflow, workflow.RegisterOptions{Name: workflows.ApprovalWorkflowName})
	w.RegisterWorkflowWithOptions(workflows.ContradictionWorkflow, workflow.RegisterOptions{Name: workflows.ContradictionWorkflowName})
	return &Worker{temporal: w}
}

func (w *Worker) Run() error {
	return w.temporal.Run(worker.InterruptCh())
}
