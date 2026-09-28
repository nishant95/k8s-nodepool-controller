package controller

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nodemanagerexampleorgv1 "github.com/nishant95/k8s-nodepool-controller/api/v1"
)

// check if node is unready and grace period expired
func isNodeUnreadyAndExpired(node *corev1.Node, gracePeriod time.Duration) bool {
	readyCond := getNodeCondition(node, corev1.NodeReady)
	if readyCond == nil || readyCond.Status == corev1.ConditionTrue {
		return false
	}
	return time.Since(readyCond.LastTransitionTime.Time) >= gracePeriod
}

// get node condition
func getNodeCondition(node *corev1.Node, conditionType corev1.NodeConditionType) *corev1.NodeCondition {
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == conditionType {
			return &node.Status.Conditions[i]
		}
	}
	return nil
}

// build node status
func buildNodeStatus(node *corev1.Node) (nodemanagerexampleorgv1.NodeStatus, bool) {
	readyCond := getNodeCondition(node, corev1.NodeReady)
	phase := nodemanagerexampleorgv1.NodePhaseNotReady
	var notReadySince *metav1.Time
	isReady := false

	if readyCond != nil {
		if readyCond.Status == corev1.ConditionTrue {
			phase = nodemanagerexampleorgv1.NodePhaseReady
			isReady = true
		} else {
			if readyCond.Status == corev1.ConditionUnknown {
				phase = nodemanagerexampleorgv1.NodePhaseUnknown
			}
			ts := readyCond.LastTransitionTime
			notReadySince = &ts
		}
	}

	if !node.DeletionTimestamp.IsZero() {
		phase = nodemanagerexampleorgv1.NodePhaseRemoving
		isReady = false
	} else if node.Spec.Unschedulable {
		phase = nodemanagerexampleorgv1.NodePhaseDraining
		isReady = false
	}

	return nodemanagerexampleorgv1.NodeStatus{
		Name:          node.Name,
		Phase:         phase,
		NotReadySince: notReadySince,
	}, isReady
}

// set nodepool status conditions
func setNodePoolStatusConditions(pool *nodemanagerexampleorgv1.NodePool) {
	if pool.Status.ReadyNodes > 0 {
		meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionTrue,
			Reason:  "NodesReady",
			Message: fmt.Sprintf("%d nodes are ready", pool.Status.ReadyNodes),
		})
	} else {
		meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Reason:  "NoNodesReady",
			Message: "No nodes are ready",
		})
	}

	unreadyCount := len(pool.Status.Nodes) - int(pool.Status.ReadyNodes)
	if unreadyCount > 0 {
		meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
			Type:    "Degraded",
			Status:  metav1.ConditionTrue,
			Reason:  "NodesUnready",
			Message: fmt.Sprintf("%d nodes are currently unready or being removed", unreadyCount),
		})
	} else {
		meta.SetStatusCondition(&pool.Status.Conditions, metav1.Condition{
			Type:    "Degraded",
			Status:  metav1.ConditionFalse,
			Reason:  "AllNodesHealthy",
			Message: "All nodes in the pool are healthy",
		})
	}
}
