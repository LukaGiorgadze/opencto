package runtime

import (
	"time"

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
	w := worker.New(client, taskQueue, worker.Options{
		MaxHeartbeatThrottleInterval: 5 * time.Second,
	})
	w.RegisterActivityWithOptions(activities.NextAction, activity.RegisterOptions{Name: "Activities.NextAction"})
	w.RegisterActivityWithOptions(activities.ExecuteTool, activity.RegisterOptions{Name: "Activities.ExecuteTool"})
	w.RegisterActivityWithOptions(activities.ResponseSession, activity.RegisterOptions{Name: "Activities.ResponseSession"})
	w.RegisterActivityWithOptions(activities.ReportResponse, activity.RegisterOptions{Name: "Activities.ReportResponse"})
	w.RegisterWorkflowWithOptions(workflows.ProjectWorkflow, workflow.RegisterOptions{Name: workflows.ProjectWorkflowName})
	w.RegisterWorkflowWithOptions(workflows.TaskWorkflow, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})
	return &Worker{temporal: w}
}

func (w *Worker) Run() error {
	return w.temporal.Run(worker.InterruptCh())
}
