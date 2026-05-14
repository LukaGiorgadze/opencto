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
	w.RegisterActivityWithOptions(activities.ExecuteMemoryTool, activity.RegisterOptions{Name: "Activities.ExecuteMemoryTool"})
	w.RegisterActivityWithOptions(activities.PersistEvent, activity.RegisterOptions{Name: "Activities.PersistEvent"})
	w.RegisterActivityWithOptions(activities.CompressConversation, activity.RegisterOptions{Name: "Activities.CompressConversation"})
	w.RegisterActivityWithOptions(activities.PersistNextAction, activity.RegisterOptions{Name: "Activities.PersistNextAction"})
	w.RegisterActivityWithOptions(activities.PersistToolResult, activity.RegisterOptions{Name: "Activities.PersistToolResult"})
	w.RegisterActivityWithOptions(activities.ResponseSession, activity.RegisterOptions{Name: "Activities.ResponseSession"})
	w.RegisterActivityWithOptions(activities.ReportResponse, activity.RegisterOptions{Name: "Activities.ReportResponse"})
	w.RegisterActivityWithOptions(activities.EnqueueScheduledEvent, activity.RegisterOptions{Name: "Activities.EnqueueScheduledEvent"})
	w.RegisterActivityWithOptions(activities.PrepareWorkflowRun, activity.RegisterOptions{Name: "Activities.PrepareWorkflowRun"})
	w.RegisterActivityWithOptions(activities.CleanupWorkflowRuns, activity.RegisterOptions{Name: "Activities.CleanupWorkflowRuns"})
	w.RegisterActivityWithOptions(activities.ExecuteWorkflowStep, activity.RegisterOptions{Name: "Activities.ExecuteWorkflowStep"})
	w.RegisterActivityWithOptions(activities.CompleteWorkflowRun, activity.RegisterOptions{Name: "Activities.CompleteWorkflowRun"})
	w.RegisterActivityWithOptions(activities.NotifyWorkflowFailure, activity.RegisterOptions{Name: "Activities.NotifyWorkflowFailure"})
	w.RegisterWorkflowWithOptions(workflows.ProjectWorkflow, workflow.RegisterOptions{Name: workflows.ProjectWorkflowName})
	w.RegisterWorkflowWithOptions(workflows.TaskWorkflow, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})
	w.RegisterWorkflowWithOptions(workflows.ScheduledDispatchWorkflow, workflow.RegisterOptions{Name: workflows.ScheduledDispatchWorkflowName})
	w.RegisterWorkflowWithOptions(workflows.WorkflowRunWorkflow, workflow.RegisterOptions{Name: workflows.WorkflowRunWorkflowName})
	return &Worker{temporal: w}
}

func (w *Worker) Run() error {
	return w.temporal.Run(worker.InterruptCh())
}
