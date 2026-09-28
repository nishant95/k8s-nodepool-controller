package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	nodemanagerexampleorgv1 "github.com/nishant95/k8s-nodepool-controller/api/v1"
)

var _ = Describe("NodePool Controller", func() {
	Context("When reconciling a NodePool", func() {
		const (
			nodePoolName = "test-pool"
			nodeName     = "test-node-1"
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name: nodePoolName, // Cluster scoped
		}

		BeforeEach(func() {
			// Create a dummy node representing an underlying VM
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Labels: map[string]string{
						bootstrapLabel: nodePoolName,
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())

			// Create the NodePool
			pool := &nodemanagerexampleorgv1.NodePool{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodePoolName,
				},
				Spec: nodemanagerexampleorgv1.NodePoolSpec{
					Labels: map[string]string{"env": "prod"},
					Taints: []corev1.Taint{
						{Key: "gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule},
					},
					UnreadyPolicy: nodemanagerexampleorgv1.UnreadyPolicy{
						GracePeriod:           metav1.Duration{Duration: 5 * time.Minute},
						Action:                nodemanagerexampleorgv1.ActionCordon,
						MaxConcurrentRemovals: 1,
					},
				},
			}
			Expect(k8sClient.Create(ctx, pool)).To(Succeed())
		})

		AfterEach(func() {
			// Clean up NodePool
			pool := &nodemanagerexampleorgv1.NodePool{}
			_ = k8sClient.Get(ctx, typeNamespacedName, pool)
			_ = k8sClient.Delete(ctx, pool)

			// Clean up Node
			node := &corev1.Node{}
			_ = k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)
			_ = k8sClient.Delete(ctx, node)
		})

		It("should sync labels and taints to matching nodes", func() {
			reconciler := &NodePoolReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(100),
			}

			// Trigger reconcile
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify the Node received the labels and taints
			var node corev1.Node
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, &node)).To(Succeed())

			Expect(node.Labels).To(HaveKeyWithValue("env", "prod"))
			Expect(node.Spec.Taints).To(HaveLen(1))
			Expect(node.Spec.Taints[0].Key).To(Equal("gpu"))

			// Verify NodePool status was updated
			var pool nodemanagerexampleorgv1.NodePool
			Expect(k8sClient.Get(ctx, typeNamespacedName, &pool)).To(Succeed())
			Expect(pool.Status.Nodes).To(HaveLen(1))
		})
	})
})
