package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nodemanagerexampleorgv1 "github.com/nishant95/k8s-nodepool-controller/api/v1"
)

const (
	nodePoolFinalizer = "nodepool.example.com/finalizer"
	bootstrapLabel    = "nodes.example.com/nodepool"
	controllerName    = "nodepool-controller"
)

// NodePoolReconciler reconciles a NodePool object
type NodePoolReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=nodemanager.example.org.example.org,resources=nodepools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nodemanager.example.org.example.org,resources=nodepools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nodemanager.example.org.example.org,resources=nodepools/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// This is what the Reconcile loop intends to do:
// 1. fetch the nodepool resource
// 2. if deleting, clean up nodes and remove finalizer
// 3. fetch all nodes matching the bootstrap label
// 4. apply declared labels and taints
// 5. handle unready nodes
// 6. calculate and schedule the next requeue if nodes in grace period
// 7. update nodepool status
func (r *NodePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	log := logf.FromContext(ctx)
	log.Info("Reconciling", "nodePool", req.Name)

	// read the NodePool resource (ignore if not there)
	var nodePool nodemanagerexampleorgv1.NodePool
	if err := r.Get(ctx, req.NamespacedName, &nodePool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// handle deletion
	if !nodePool.DeletionTimestamp.IsZero() {
		return r.handleNodePoolDeletion(ctx, &nodePool)
	}

	// add finalizer
	if !controllerutil.ContainsFinalizer(&nodePool, nodePoolFinalizer) {
		controllerutil.AddFinalizer(&nodePool, nodePoolFinalizer)
		if err := r.Update(ctx, &nodePool); err != nil {
			log.Error(err, "Failed to add finalizer to Nodepool")
			return ctrl.Result{}, err
		}
	}

	// get all nodes that match the bootstrap label
	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList, client.MatchingLabels{bootstrapLabel: nodePool.Name}); err != nil {
		log.Error(err, "Unable to list nodes")
		return ctrl.Result{}, err
	}

	// update status at the end
	defer func() {
		if !nodePool.DeletionTimestamp.IsZero() && !controllerutil.ContainsFinalizer(&nodePool, nodePoolFinalizer) {
			return
		}
		// Re-fetch the NodePool to get the latest resource version before updating status.
		// This avoids "the object has been modified" errors caused by the finalizer update
		// or other changes within the same reconciliation cycle.
		if getErr := r.Get(ctx, req.NamespacedName, &nodePool); getErr != nil {
			log.Error(getErr, "Failed to re-fetch NodePool before status update")
			return
		}
		if statusErr := r.updateStatus(ctx, &nodePool, nodeList.Items); statusErr != nil {
			log.Error(statusErr, "Failed to update NodePool status")
		}
	}()

	// apply the declared state to the ready nodes
	if err := r.syncNodes(ctx, &nodePool, nodeList.Items); err != nil {
		log.Error(err, "Failed to sync nodes")
		return ctrl.Result{}, err
	}

	// handle unready nodes
	if err := r.executeUnreadyActions(ctx, &nodePool, nodeList.Items); err != nil {
		return ctrl.Result{}, err
	}

	// requeue if nodes in grace period
	if requeueAfter := calculateNextRequeue(&nodePool, nodeList.Items); requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	return ctrl.Result{}, nil
}

func (r *NodePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nodemanagerexampleorgv1.NodePool{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj client.Object) []reconcile.Request {
				node := obj.(*corev1.Node)

				// nodepool CR name has to exactly match the bootstrap label
				nodePoolName, exists := node.Labels[bootstrapLabel]
				if !exists {
					// skip nodes without the bootstrap label
					return nil
				}
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Name: nodePoolName,
					},
				}}
			},
		)).
		Named(controllerName).
		Complete(r)
}
