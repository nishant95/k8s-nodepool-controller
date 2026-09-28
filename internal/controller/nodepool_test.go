package controller

import (
	"testing"
	"time"

	nodemanagerexampleorgv1 "github.com/nishant95/k8s-nodepool-controller/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCalculateNextRequeue(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name        string
		gracePeriod time.Duration
		nodes       []corev1.Node
		expected    time.Duration
	}{
		{
			name:        "No unready nodes",
			gracePeriod: 5 * time.Minute,
			nodes: []corev1.Node{
				{
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
						},
					},
				},
			},
			expected: 0,
		},
		{
			name:        "One unready node exactly halfway through grace period",
			gracePeriod: 10 * time.Minute,
			nodes: []corev1.Node{
				{
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{
								Type:               corev1.NodeReady,
								Status:             corev1.ConditionFalse,
								LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
							},
						},
					},
				},
			},
			expected: 5 * time.Minute,
		},
		{
			name:        "Multiple unready nodes, returns shortest remaining time",
			gracePeriod: 10 * time.Minute,
			nodes: []corev1.Node{
				{
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{
								Type:               corev1.NodeReady,
								Status:             corev1.ConditionFalse,
								LastTransitionTime: metav1.NewTime(now.Add(-2 * time.Minute)), // 8 mins left
							},
						},
					},
				},
				{
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{
								Type:               corev1.NodeReady,
								Status:             corev1.ConditionFalse,
								LastTransitionTime: metav1.NewTime(now.Add(-9 * time.Minute)), // 1 min left
							},
						},
					},
				},
			},
			expected: 1 * time.Minute,
		},
		{
			name:        "Node already exceeded grace period",
			gracePeriod: 5 * time.Minute,
			nodes: []corev1.Node{
				{
					Status: corev1.NodeStatus{
						Conditions: []corev1.NodeCondition{
							{
								Type:               corev1.NodeReady,
								Status:             corev1.ConditionFalse,
								LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Minute)), // Exceeded by 5 mins
							},
						},
					},
				},
			},
			expected: 0, // Should not requeue, the execution phase will handle it immediately
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := &nodemanagerexampleorgv1.NodePool{
				Spec: nodemanagerexampleorgv1.NodePoolSpec{
					UnreadyPolicy: nodemanagerexampleorgv1.UnreadyPolicy{
						GracePeriod: metav1.Duration{Duration: tt.gracePeriod},
					},
				},
			}

			result := calculateNextRequeue(pool, tt.nodes)

			// Use a small epsilon for time comparison to prevent flaky tests
			diff := result - tt.expected
			if diff < -time.Second || diff > time.Second {
				t.Errorf("calculateNextRequeue() = %v, want %v", result, tt.expected)
			}
		})
	}
}
