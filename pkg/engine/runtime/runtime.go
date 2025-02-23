package runtime

import (
	"context"

	"k8s.io/apimachinery/pkg/watch"

	"kusionstack.io/kusion/pkg/models"
	"kusionstack.io/kusion/pkg/projectstack"
	"kusionstack.io/kusion/pkg/status"
)

const (
	Kubernetes models.Type = "Kubernetes"
	Terraform  models.Type = "Terraform"
)

// Runtime represents an actual infrastructure runtime managed by Kusion and every runtime implements this interface can be orchestrated
// by Kusion like normal K8s resources. All methods in this interface are designed for manipulating one Resource at a time and will be
// invoked in operations like Apply, Preview, Destroy, etc.
type Runtime interface {
	// Apply means modify this Resource to the desired state described in the request,
	// and it will turn into creating or updating a Resource in most scenarios.
	// If the infrastructure runtime already provides an Apply method that conform to this method's semantics meaning,
	// like the Kubernetes Runtime, you can directly invoke this method without any conversion.
	// PlanResource and priorState are given in this method for the runtime which would make a
	// three-way-merge (planState,priorState and live state) when implementing this interface
	Apply(ctx context.Context, request *ApplyRequest) *ApplyResponse

	// Read the latest state of this Resource
	Read(ctx context.Context, request *ReadRequest) *ReadResponse

	// Import Resource that already existed in the actual infrastructure
	Import(ctx context.Context, request *ImportRequest) *ImportResponse

	// Delete this Resource in the actual infrastructure and return success if this Resource is not exist
	Delete(ctx context.Context, request *DeleteRequest) *DeleteResponse

	// Watch the latest state or event of this Resource.
	// This is an optional method for the Runtime to implement,
	// but it will be very helpful for us to know what is happening when applying this Resource
	Watch(ctx context.Context, request *WatchRequest) *WatchResponse
}

type ApplyRequest struct {
	// PriorResource is the last applied resource saved in state storage
	PriorResource *models.Resource

	// PlanResource is the resource we want to apply in this request
	PlanResource *models.Resource

	// Stack contains info about where this command is invoked
	Stack *projectstack.Stack

	// DryRun means this a dry-run request and will not make any changes in actual infra
	DryRun bool
}

type ApplyResponse struct {
	// Resource is the result returned by Runtime
	Resource *models.Resource

	// Status contains messages will show to users
	Status status.Status
}

type ReadRequest struct {
	// PriorResource is the last applied resource saved in state storage
	PriorResource *models.Resource

	// PlanResource is the resource we want to apply in this request
	PlanResource *models.Resource

	// Stack contains info about where this command is invoked
	Stack *projectstack.Stack
}

type ReadResponse struct {
	// Resource is the result read from the actual infra
	Resource *models.Resource

	// Status contains messages will show to users
	Status status.Status
}

type ImportRequest struct {
	// PlanResource is the resource we want to apply in this request
	PlanResource *models.Resource

	// Stack contains info about where this command is invoked
	Stack *projectstack.Stack
}

type ImportResponse struct {
	// Resource is the result returned by Runtime
	Resource *models.Resource

	// Status contains messages will show to users
	Status status.Status
}

type DeleteRequest struct {
	// Resource represents the resource we want to delete from the actual infra
	Resource *models.Resource

	// Stack contains info about where this command is invoked
	Stack *projectstack.Stack
}

type DeleteResponse struct {
	// Status contains messages will show to users
	Status status.Status
}

type WatchRequest struct {
	// Resource represents the resource we want to watch from the actual infra
	Resource *models.Resource
}

type WatchResponse struct {
	Watchers *SequentialWatchers

	// Status contains messages will show to users
	Status status.Status
}

type SequentialWatchers struct {
	IDs      []string
	Watchers []<-chan watch.Event
}

func NewWatchers() *SequentialWatchers {
	return &SequentialWatchers{
		IDs:      []string{},
		Watchers: []<-chan watch.Event{},
	}
}

func (w *SequentialWatchers) Insert(id string, watcher <-chan watch.Event) {
	w.IDs = append(w.IDs, id)
	w.Watchers = append(w.Watchers, watcher)
}
