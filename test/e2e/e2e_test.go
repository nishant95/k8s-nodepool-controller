//go:build e2e
// +build e2e

/*
Copyright 2026 Nishant Hooda.

Licensed under the GNU Affero General Public License, Version 3.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.gnu.org/licenses/agpl-3.0.en.html

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/nishant95/k8s-nodepool-controller/test/utils"
)

// namespace where the project is deployed in
const namespace = "k8s-nodepool-controller-system"

// serviceAccountName created for the project
const serviceAccountName = "k8s-nodepool-controller-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "k8s-nodepool-controller-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "k8s-nodepool-controller-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")
		time.Sleep(2 * time.Second)

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks

		It("should sync NodePool labels and handle Unready nodes", func() {
			By("creating a NodePool")
			nodePoolYaml := `
apiVersion: nodemanager.example.org.example.org/v1
kind: NodePool
metadata:
  name: test-e2e-pool
spec:
  labels:
    e2etest: passed
  unreadyPolicy:
    gracePeriod: 5s
    action: Cordon
    maxConcurrentRemovals: 1
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nodePoolYaml)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create NodePool")

			By("creating a lightweight fake node")
			fakeNodeYaml := `
apiVersion: v1
kind: Node
metadata:
  name: fake-node-1
  labels:
    nodes.example.com/nodepool: test-e2e-pool
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(fakeNodeYaml)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create fake node")

			By("verifying the node received the spec labels via SSA")
			verifyLabels := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", "fake-node-1", "-o", "jsonpath={.metadata.labels.e2etest}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("passed"))
			}
			Eventually(verifyLabels, 15*time.Second, time.Second).Should(Succeed())

			By("simulating node failure instantly via status patch")
			cmd = exec.Command("kubectl", "patch", "node", "fake-node-1", "--subresource=status", "--type=merge", "-p", `{"status":{"conditions":[{"type":"Ready","status":"False","lastTransitionTime":"2020-01-01T00:00:00Z"}]}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to patch node status")

			By("waiting for the controller to cordon the node after GracePeriod")
			verifyCordoned := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", "fake-node-1", "-o", "jsonpath={.spec.unschedulable}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("true"))
			}
			Eventually(verifyCordoned, 15*time.Second, time.Second).Should(Succeed())

			By("cleaning up")
			exec.Command("kubectl", "delete", "node", "fake-node-1").Run()
			exec.Command("kubectl", "delete", "nodepool", "test-e2e-pool").Run()
		})

		It("should ignore Unready nodes that do not belong to the NodePool", func() {
			By("creating a NodePool")
			nodePoolYaml := `
apiVersion: nodemanager.example.org.example.org/v1
kind: NodePool
metadata:
  name: ignored-pool
spec:
  unreadyPolicy:
    gracePeriod: 1s
    action: Cordon
    maxConcurrentRemovals: 1
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nodePoolYaml)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("creating an unrelated fake node")
			fakeNodeYaml := `
apiVersion: v1
kind: Node
metadata:
  name: unrelated-node
  labels:
    some-other-label: "true"
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(fakeNodeYaml)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("simulating node failure")
			cmd = exec.Command("kubectl", "patch", "node", "unrelated-node", "--subresource=status", "--type=merge", "-p", `{"status":{"conditions":[{"type":"Ready","status":"False","lastTransitionTime":"2020-01-01T00:00:00Z"}]}}`)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("ensuring the node is NEVER cordoned")
			verifyNotCordoned := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", "unrelated-node", "-o", "jsonpath={.spec.unschedulable}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(Equal("true"))
			}
			Consistently(verifyNotCordoned, 5*time.Second, time.Second).Should(Succeed())

			By("cleaning up")
			exec.Command("kubectl", "delete", "node", "unrelated-node").Run()
			exec.Command("kubectl", "delete", "nodepool", "ignored-pool").Run()
		})

		It("should enforce MaxConcurrentRemovals", func() {
			By("creating a NodePool with maxConcurrentRemovals=1")
			nodePoolYaml := `
apiVersion: nodemanager.example.org.example.org/v1
kind: NodePool
metadata:
  name: rate-limit-pool
spec:
  unreadyPolicy:
    gracePeriod: 1s
    action: Cordon
    maxConcurrentRemovals: 1
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nodePoolYaml)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("creating two fake nodes")
			for i := 1; i <= 2; i++ {
				fakeNodeYaml := fmt.Sprintf(`
apiVersion: v1
kind: Node
metadata:
  name: limit-node-%d
  labels:
    nodes.example.com/nodepool: rate-limit-pool
`, i)
				cmd = exec.Command("kubectl", "apply", "-f", "-")
				cmd.Stdin = strings.NewReader(fakeNodeYaml)
				_, err = utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())
			}

			By("simulating the first node failing")
			cmd = exec.Command("kubectl", "patch", "node", "limit-node-1", "--subresource=status", "--type=merge", "-p", `{"status":{"conditions":[{"type":"Ready","status":"False","lastTransitionTime":"2020-01-01T00:00:00Z"}]}}`)
			utils.Run(cmd)

			By("waiting for the first node to be cordoned")
			verifyFirstCordoned := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", "limit-node-1", "-o", "jsonpath={.spec.unschedulable}")
				out, _ := utils.Run(cmd)
				g.Expect(out).To(Equal("true"))
			}
			Eventually(verifyFirstCordoned, 10*time.Second, time.Second).Should(Succeed())
			time.Sleep(2 * time.Second)

			By("simulating the second node failing")
			cmd = exec.Command("kubectl", "patch", "node", "limit-node-2", "--subresource=status", "--type=merge", "-p", `{"status":{"conditions":[{"type":"Ready","status":"False","lastTransitionTime":"2020-01-01T00:00:00Z"}]}}`)
			utils.Run(cmd)

			By("ensuring the second node is NOT cordoned while the first remains failed")
			verifySecondNotCordoned := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", "limit-node-2", "-o", "jsonpath={.spec.unschedulable}")
				out, _ := utils.Run(cmd)
				g.Expect(out).NotTo(Equal("true"))
			}
			Consistently(verifySecondNotCordoned, 5*time.Second, time.Second).Should(Succeed())

			By("cleaning up")
			exec.Command("kubectl", "delete", "node", "limit-node-1", "limit-node-2").Run()
			exec.Command("kubectl", "delete", "nodepool", "rate-limit-pool").Run()
		})

		It("should accurately sync NodePool status based on actual node states", func() {
			By("creating a NodePool")
			nodePoolYaml := `
apiVersion: nodemanager.example.org.example.org/v1
kind: NodePool
metadata:
  name: status-pool
spec:
  unreadyPolicy:
    gracePeriod: 10m
    action: Cordon
    maxConcurrentRemovals: 1
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nodePoolYaml)
			utils.Run(cmd)

			By("creating three fake nodes (two ready, one unready)")
			for i := 1; i <= 3; i++ {
				nodeYaml := fmt.Sprintf(`
apiVersion: v1
kind: Node
metadata:
  name: status-node-%d
  labels:
    nodes.example.com/nodepool: status-pool
`, i)
				cmd = exec.Command("kubectl", "apply", "-f", "-")
				cmd.Stdin = strings.NewReader(nodeYaml)
				utils.Run(cmd)

				statusPatch := "True"
				if i == 3 {
					statusPatch = "False" // Node 3 is unready
				}
				cmd = exec.Command("kubectl", "patch", "node", fmt.Sprintf("status-node-%d", i), "--subresource=status", "--type=merge", "-p", fmt.Sprintf(`{"status":{"conditions":[{"type":"Ready","status":"%s"}]}}`, statusPatch))
				utils.Run(cmd)
			}

			By("verifying the NodePool status reflects exactly 3 nodes with correct phases")
			verifyStatus := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nodepool", "status-pool", "-o", "jsonpath={.status.nodes[*].name}")
				out, _ := utils.Run(cmd)
				g.Expect(out).To(ContainSubstring("status-node-1"))
				g.Expect(out).To(ContainSubstring("status-node-2"))
				g.Expect(out).To(ContainSubstring("status-node-3"))

				cmd = exec.Command("kubectl", "get", "nodepool", "status-pool", "-o", "jsonpath={.status.conditions[?(@.type=='Degraded')].status}")
				out, _ = utils.Run(cmd)
				g.Expect(out).To(Equal("True"))
			}
			Eventually(verifyStatus, 15*time.Second, time.Second).Should(Succeed())

			By("cleaning up")
			exec.Command("kubectl", "delete", "node", "status-node-1", "status-node-2", "status-node-3").Run()
			exec.Command("kubectl", "delete", "nodepool", "status-pool").Run()
		})

		It("should publish Kubernetes events when taking action on unready nodes", func() {
			By("creating a NodePool")
			nodePoolYaml := `
apiVersion: nodemanager.example.org.example.org/v1
kind: NodePool
metadata:
  name: event-pool
spec:
  labels:
    synced-label: "true"
  unreadyPolicy:
    gracePeriod: 1s
    action: Cordon
    maxConcurrentRemovals: 1
`
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(nodePoolYaml)
			utils.Run(cmd)

			By("creating a fake node and simulating failure")
			fakeNodeYaml := `
apiVersion: v1
kind: Node
metadata:
  name: event-node
  labels:
    nodes.example.com/nodepool: event-pool
`
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(fakeNodeYaml)
			utils.Run(cmd)

			By("simulating node failure")
			cmd = exec.Command("kubectl", "patch", "node", "event-node", "--subresource=status", "--type=merge", "-p", `{"status":{"conditions":[{"type":"Ready","status":"False","lastTransitionTime":"2020-01-01T00:00:00Z"}]}}`)
			utils.Run(cmd)

			By("waiting for the controller to cordon the node")
			verifyCordoned := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "node", "event-node", "-o", "jsonpath={.spec.unschedulable}")
				output, _ := utils.Run(cmd)
				g.Expect(output).To(Equal("true"))
			}
			Eventually(verifyCordoned, 15*time.Second, time.Second).Should(Succeed())

			By("verifying the NodeCordoned event was published to the NodePool")
			verifyCordonEvent := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "events", "--field-selector", "involvedObject.kind=NodePool,involvedObject.name=event-pool,reason=NodeCordoned", "-A", "-o", "jsonpath={.items[*].message}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Cordoned node event-node due to unready policy"))
			}
			Eventually(verifyCordonEvent, 10*time.Second, time.Second).Should(Succeed())

			By("verifying the NodeSynced event was published to the NodePool")
			verifySyncEvent := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "events", "--field-selector", "involvedObject.kind=NodePool,involvedObject.name=event-pool,reason=NodeSynced", "-A", "-o", "jsonpath={.items[*].message}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Successfully synced node event-node to match NodePool state"))
			}
			Eventually(verifySyncEvent, 10*time.Second, time.Second).Should(Succeed())

			By("cleaning up")
			exec.Command("kubectl", "delete", "node", "event-node").Run()
			exec.Command("kubectl", "delete", "nodepool", "event-pool").Run()
		})

	})
})
