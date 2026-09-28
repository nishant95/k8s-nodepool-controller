package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1apply "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	nodemanagerexampleorgv1 "github.com/nishant95/k8s-nodepool-controller/api/v1"
)

// create patch and apply to all nodes
func isNodeSynced(node *corev1.Node, pool *nodemanagerexampleorgv1.NodePool) bool {
	for k, v := range pool.Spec.Labels {
		if node.Labels == nil || node.Labels[k] != v {
			return false
		}
	}
	for _, pt := range pool.Spec.Taints {
		found := false
		for _, nt := range node.Spec.Taints {
			if nt.Key == pt.Key && nt.Value == pt.Value && nt.Effect == pt.Effect {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (r *NodePoolReconciler) syncNodes(ctx context.Context, pool *nodemanagerexampleorgv1.NodePool, nodes []corev1.Node) error {
	log := logf.FromContext(ctx)
	for i := range nodes {
		node := &nodes[i]

		if !isNodeSynced(node, pool) {
			// Construct and apply patch
			patchNode := r.createNodepatch(*node, pool)

			if err := r.Apply(ctx, patchNode, client.ForceOwnership, client.FieldOwner(controllerName)); err != nil {
				log.Error(err, "Failed to apply labels/taints via SSA", "node", node.Name)
				return err
			}

			log.Info("Successfully synced node state", "node", node.Name)
			r.Recorder.Eventf(pool, corev1.EventTypeNormal, "NodeSynced", "Successfully synced node %s to match NodePool state", node.Name)
		}
	}
	return nil
}

// create node patch
func (r *NodePoolReconciler) createNodepatch(node corev1.Node, pool *nodemanagerexampleorgv1.NodePool) *corev1apply.NodeApplyConfiguration {
	patchNode := corev1apply.Node(node.Name).WithLabels(pool.Spec.Labels)

	if len(pool.Spec.Taints) > 0 {
		taints := make([]*corev1apply.TaintApplyConfiguration, 0, len(pool.Spec.Taints))
		for _, t := range pool.Spec.Taints {
			taintApply := corev1apply.Taint().
				WithKey(t.Key).
				WithEffect(t.Effect)
			if t.Value != "" {
				taintApply.WithValue(t.Value)
			}
			taints = append(taints, taintApply)
		}
		patchNode.WithSpec(corev1apply.NodeSpec().WithTaints(taints...))
	}

	return patchNode
}

// executeUnreadyActions applies the desired actions (Remove/Cordon) to nodes that have exceeded their grace period
func (r *NodePoolReconciler) executeUnreadyActions(ctx context.Context, pool *nodemanagerexampleorgv1.NodePool, nodes []corev1.Node) error {
	log := logf.FromContext(ctx)
	gracePeriod := pool.Spec.UnreadyPolicy.GracePeriod.Duration
	var currentRemovals int32 = 0

	// First pass: count already affected nodes
	for i := range nodes {
		node := &nodes[i]
		if isNodeUnreadyAndExpired(node, gracePeriod) {
			if pool.Spec.UnreadyPolicy.Action == nodemanagerexampleorgv1.ActionRemove && !node.DeletionTimestamp.IsZero() {
				currentRemovals++
			} else if pool.Spec.UnreadyPolicy.Action == nodemanagerexampleorgv1.ActionCordon && node.Spec.Unschedulable {
				currentRemovals++
			}
		}
	}

	// Second pass: apply actions to remaining nodes up to limit
	for i := range nodes {
		node := &nodes[i]
		if isNodeUnreadyAndExpired(node, gracePeriod) {

			switch pool.Spec.UnreadyPolicy.Action {
			case nodemanagerexampleorgv1.ActionRemove:
				if node.DeletionTimestamp.IsZero() {
					if currentRemovals >= pool.Spec.UnreadyPolicy.MaxConcurrentRemovals {
						continue
					}
					if err := r.removeUnreadyNode(ctx, pool, node); err != nil {
						return err
					}
					currentRemovals++
				}
			case nodemanagerexampleorgv1.ActionCordon:
				if !node.Spec.Unschedulable {
					if currentRemovals >= pool.Spec.UnreadyPolicy.MaxConcurrentRemovals {
						continue
					}
					if err := r.cordonUnreadyNode(ctx, pool, node); err != nil {
						return err
					}
					currentRemovals++
				}
			case nodemanagerexampleorgv1.ActionIgnore:
				log.Info("Ignoring unready node due to policy", "node", node.Name)
			}
		}
	}
	return nil
}

// removeUnreadyNode attempts to delete the node if the max concurrent removals limit hasn't been reached
func (r *NodePoolReconciler) removeUnreadyNode(ctx context.Context, pool *nodemanagerexampleorgv1.NodePool, node *corev1.Node) error {
	log := logf.FromContext(ctx)
	// STONITH Fencing Guardrail
	isFenced, err := r.ensureNodeIsFenced(ctx, node)
	if err != nil {
		return err
	}
	if !isFenced {
		log.Info("Node is not yet physically fenced at the infrastructure level, deferring removal", "node", node.Name)
		return nil
	}

	log.Info("Removing node due to unready policy", "node", node.Name)
	r.Recorder.Eventf(pool, corev1.EventTypeWarning, "NodeRemoved", "Removing node %s due to unready policy", node.Name)

	if err := r.Delete(ctx, node); err != nil {
		log.Error(err, "Failed to remove node", "node", node.Name)
		return err
	}
	// update the node so that status reflects the deletion instantly
	now := metav1.Now()
	node.DeletionTimestamp = &now
	return nil
}

// Here we can ensure that the node is actually deleted before deleting the Node K8s resource.
func (r *NodePoolReconciler) ensureNodeIsFenced(ctx context.Context, node *corev1.Node) (bool, error) {
	return true, nil
}

// cordon node
func (r *NodePoolReconciler) cordonUnreadyNode(ctx context.Context, pool *nodemanagerexampleorgv1.NodePool, node *corev1.Node) error {
	log := logf.FromContext(ctx)
	log.Info("Cordoning node due to unready policy", "node", node.Name)
	patch := client.MergeFrom(node.DeepCopy())
	node.Spec.Unschedulable = true
	if err := r.Patch(ctx, node, patch); err != nil {
		log.Error(err, "Failed to cordon node", "node", node.Name)
		return err
	}
	r.Recorder.Eventf(pool, corev1.EventTypeWarning, "NodeCordoned", "Cordoned node %s due to unready policy", node.Name)
	return nil
}
