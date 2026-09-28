package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	nodemanagerexampleorgv1 "github.com/nishant95/k8s-nodepool-controller/api/v1"
)

// updateStatus calculates and commits the NodePoolStatus
func (r *NodePoolReconciler) updateStatus(ctx context.Context, pool *nodemanagerexampleorgv1.NodePool, nodes []corev1.Node) error {
	var readyNodes int32
	nodeStatuses := make([]nodemanagerexampleorgv1.NodeStatus, 0, len(nodes))

	for _, node := range nodes {
		status, isReady := buildNodeStatus(&node)
		if isReady {
			readyNodes++
		}
		nodeStatuses = append(nodeStatuses, status)
	}

	pool.Status.ReadyNodes = readyNodes
	pool.Status.Nodes = nodeStatuses
	pool.Status.ObservedGeneration = pool.Generation

	setNodePoolStatusConditions(pool)

	return r.Status().Update(ctx, pool)
}

// calculateNextRequeue returns the shortest time until any unready node exceeds its grace period
func calculateNextRequeue(pool *nodemanagerexampleorgv1.NodePool, nodes []corev1.Node) time.Duration {
	gracePeriod := pool.Spec.UnreadyPolicy.GracePeriod.Duration
	var minRequeue time.Duration

	for _, node := range nodes {
		readyCond := getNodeCondition(&node, corev1.NodeReady)
		if readyCond == nil || readyCond.Status == corev1.ConditionTrue {
			// healthy node, skip
			continue
		}

		timeUnready := time.Since(readyCond.LastTransitionTime.Time)
		if timeUnready < gracePeriod {
			remaining := gracePeriod - timeUnready
			if minRequeue == 0 || remaining < minRequeue {
				minRequeue = remaining
			}
		}
	}

	return minRequeue
}

// clean up labels and taints before allowing the NodePool to be deleted
func (r *NodePoolReconciler) handleNodePoolDeletion(ctx context.Context, pool *nodemanagerexampleorgv1.NodePool) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if controllerutil.ContainsFinalizer(pool, nodePoolFinalizer) {
		var nodeList corev1.NodeList
		if err := r.List(ctx, &nodeList, client.MatchingLabels{bootstrapLabel: pool.Name}); err != nil {
			return ctrl.Result{}, err
		}

		for _, node := range nodeList.Items {
			patchNode := r.createNodepatch(node, pool)

			if err := r.Apply(ctx, patchNode, client.ForceOwnership, client.FieldOwner(controllerName)); err != nil {
				log.Error(err, "Failed to clean up labels/tains from node", "node", node.Name)
				return ctrl.Result{}, err
			}
		}

		controllerutil.RemoveFinalizer(pool, nodePoolFinalizer)
		if err := r.Update(ctx, pool); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}
