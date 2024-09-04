package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/pkg/util/slice"
	imageutils "k8s.io/kubernetes/test/utils/image"
	"k8s.io/utils/diff"
	utilpointer "k8s.io/utils/pointer"

	appspub "github.com/openkruise/kruise/apis/apps/pub"
	appsv1alpha1 "github.com/openkruise/kruise/apis/apps/v1alpha1"
	appsv1beta1 "github.com/openkruise/kruise/apis/apps/v1beta1"
	kruiseclientset "github.com/openkruise/kruise/pkg/client/clientset/versioned"
	"github.com/openkruise/kruise/pkg/util"
	"github.com/openkruise/kruise/test/e2e/framework"
)

var _ = SIGDescribe("InplaceVPA", func() {
	f := framework.NewDefaultFramework("inplace-vpa")
	var ns string
	var c clientset.Interface
	var kc kruiseclientset.Interface
	var tester *framework.CloneSetTester
	var randStr string
	IsKubernetesVersionLessThan127 := func() bool {
		if v, err := c.Discovery().ServerVersion(); err != nil {
			framework.Logf("Failed to discovery server version: %v", err)
		} else if minor, err := strconv.Atoi(v.Minor); err != nil || minor < 27 {
			return true
		}
		return false
	}

	ginkgo.BeforeEach(func() {
		c = f.ClientSet
		kc = f.KruiseClientSet
		ns = f.Namespace.Name
		tester = framework.NewCloneSetTester(c, kc, ns)
		randStr = rand.String(10)

		if IsKubernetesVersionLessThan127() {
			ginkgo.Skip("kip this e2e case, it can only run on K8s >= 1.27")
		}
	})

	// TODO(Abner-1)update only inplace resources may fail in kind e2e.
	// I will resolve it in another PR
	framework.KruiseDescribe("CloneSet Updating with only inplace resource", func() {
		var err error
		testUpdateResource := func(fn func(spec *v1.PodSpec), resizePolicy []v1.ContainerResizePolicy) {
			cs := tester.NewCloneSet("clone-"+randStr, 1, appsv1alpha1.CloneSetUpdateStrategy{Type: appsv1alpha1.InPlaceIfPossibleCloneSetUpdateStrategyType})
			cs.Spec.Template.Spec.Containers[0].ResizePolicy = resizePolicy
			imageConfig := imageutils.GetConfig(imageutils.Nginx)
			imageConfig.SetRegistry("docker.io/library")
			imageConfig.SetVersion("alpine")
			cs.Spec.Template.Spec.Containers[0].Image = imageConfig.GetE2EImage()
			cs, err = tester.CreateCloneSet(cs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(cs.Spec.UpdateStrategy.Type).To(gomega.Equal(appsv1alpha1.InPlaceIfPossibleCloneSetUpdateStrategyType))

			ginkgo.By("Wait for replicas satisfied")
			gomega.Eventually(func() int32 {
				cs, err = tester.GetCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				return cs.Status.Replicas
			}, 3*time.Second, time.Second).Should(gomega.Equal(int32(1)))

			ginkgo.By("Wait for all pods ready")
			gomega.Eventually(func() int32 {
				cs, err = tester.GetCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				return cs.Status.ReadyReplicas
			}, 60*time.Second, 3*time.Second).Should(gomega.Equal(int32(1)))

			pods, err := tester.ListPodsForCloneSet(cs.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(len(pods)).Should(gomega.Equal(1))
			oldPodResource := getPodResource(pods[0])

			err = tester.UpdateCloneSet(cs.Name, func(cs *appsv1alpha1.CloneSet) {
				if cs.Annotations == nil {
					cs.Annotations = map[string]string{}
				}
				fn(&cs.Spec.Template.Spec)
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			lastGeneration := cs.Generation
			ginkgo.By("Wait for CloneSet generation consistent")
			gomega.Eventually(func() bool {
				cs, err = tester.GetCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				return cs.Generation == cs.Status.ObservedGeneration
			}, 10*time.Second, 3*time.Second).Should(gomega.Equal(true))

			framework.Logf("CloneSet last %v, generation %v, observedGeneration %v", lastGeneration, cs.Generation, cs.Status.ObservedGeneration)
			start := time.Now()
			ginkgo.By("Wait for all pods updated and ready")
			gomega.Eventually(func() int32 {
				cs, err = tester.GetCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				return cs.Status.UpdatedAvailableReplicas
			}, 600*time.Second, 3*time.Second).Should(gomega.Equal(int32(1)))
			duration := time.Since(start)
			framework.Logf("cloneset with replica resize resource consume %vs", duration.Seconds())

			ginkgo.By("Verify the resource changed and status=spec")
			pods, err = tester.ListPodsForCloneSet(cs.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(checkPodResource(pods, oldPodResource, []string{"redis"})).Should(gomega.Equal(true))
		}
		testWithResizePolicy := func(resizePolicy []v1.ContainerResizePolicy) {
			// This can't be Conformance yet.
			ginkgo.PIt("in-place update resources scale down 1", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale down cpu and memory request")
					spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("100m")
					spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("100Mi")
				}
				testUpdateResource(fn, resizePolicy)
			})
			ginkgo.PIt("in-place update resources scale down 2", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale down cpu and memory limit")
					spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("800m")
					spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("800Mi")
				}
				testUpdateResource(fn, resizePolicy)
			})
			ginkgo.PIt("in-place update resources scale down 3", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale down cpu and memory request&limit")
					spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("100m")
					spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("100Mi")
					spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("800m")
					spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("800Mi")
				}
				testUpdateResource(fn, resizePolicy)
			})

			// This can't be Conformance yet.
			ginkgo.PIt("in-place update resources scale up 1", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale up cpu and memory request")
					spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("300m")
					spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("300Mi")
				}
				testUpdateResource(fn, resizePolicy)
			})
			ginkgo.PIt("in-place update resources scale up 2", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale up cpu and memory limit")
					spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("2")
					spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("2Gi")
				}
				testUpdateResource(fn, resizePolicy)
			})
			ginkgo.PIt("in-place update resources scale up 3", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale up cpu and memory request&limit")
					spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("300m")
					spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("300Mi")
					spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("2")
					spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("2Gi")
				}
				testUpdateResource(fn, resizePolicy)
			})

			// This can't be Conformance yet.
			ginkgo.PIt("in-place update resources scale up only cpu 1", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale up cpu request")
					spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("300m")
				}
				testUpdateResource(fn, resizePolicy)
			})
			ginkgo.PIt("in-place update resources scale up only cpu limit", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale up cpu limit")
					spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("2")
				}
				testUpdateResource(fn, resizePolicy)
			})
			ginkgo.PIt("in-place update resources scale up only cpu 3", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale up cpu request&limit")
					spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("300m")
					spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("2")
				}
				testUpdateResource(fn, resizePolicy)
			})

			// This can't be Conformance yet.
			ginkgo.PIt("in-place update resources scale up only mem 1", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale up memory request")
					spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("300Mi")
				}
				testUpdateResource(fn, resizePolicy)
			})
			ginkgo.PIt("in-place update resources scale up only mem limit", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale up memory limit")
					spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("2Gi")
				}
				testUpdateResource(fn, resizePolicy)
			})
			ginkgo.PIt("in-place update resources scale up only mem 3", func() {
				fn := func(spec *v1.PodSpec) {
					ginkgo.By("scale up memory request&limit")
					spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("300Mi")
					spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("2Gi")
				}
				testUpdateResource(fn, resizePolicy)
			})
		}

		ginkgo.By("inplace update resources with RestartContainer policy")
		testWithResizePolicy([]v1.ContainerResizePolicy{
			{
				ResourceName: v1.ResourceCPU, RestartPolicy: v1.RestartContainer,
			},
			{
				ResourceName: v1.ResourceMemory, RestartPolicy: v1.RestartContainer,
			},
		})
	})

	framework.KruiseDescribe("CloneSet Updating with inplace resource", func() {
		var err error
		testWithResizePolicy := func(resizePolicy []v1.ContainerResizePolicy) {
			testUpdateResource := func(fn func(pod *v1.PodTemplateSpec), resizePolicy []v1.ContainerResizePolicy) {
				cs := tester.NewCloneSet("clone-"+randStr, 1, appsv1alpha1.CloneSetUpdateStrategy{Type: appsv1alpha1.InPlaceIfPossibleCloneSetUpdateStrategyType})
				cs.Spec.Template.Spec.Containers[0].ResizePolicy = resizePolicy
				cs.Spec.Template.Spec.Containers[0].Image = NginxImage
				cs.Spec.Template.ObjectMeta.Labels["test-env"] = "foo"
				cs.Spec.Template.Spec.Containers[0].Env = append(cs.Spec.Template.Spec.Containers[0].Env, v1.EnvVar{
					Name:      "TEST_ENV",
					ValueFrom: &v1.EnvVarSource{FieldRef: &v1.ObjectFieldSelector{FieldPath: "metadata.labels['test-env']"}},
				})
				cs, err = tester.CreateCloneSet(cs)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(cs.Spec.UpdateStrategy.Type).To(gomega.Equal(appsv1alpha1.InPlaceIfPossibleCloneSetUpdateStrategyType))

				ginkgo.By("Wait for replicas satisfied")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.Replicas
				}, 3*time.Second, time.Second).Should(gomega.Equal(int32(1)))

				ginkgo.By("Wait for all pods ready")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.ReadyReplicas
				}, 60*time.Second, 3*time.Second).Should(gomega.Equal(int32(1)))

				pods, err := tester.ListPodsForCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(pods)).Should(gomega.Equal(1))
				oldPodUID := pods[0].UID
				oldContainerStatus := pods[0].Status.ContainerStatuses[0]
				oldPodResource := getPodResource(pods[0])

				ginkgo.By("Update test-env label")
				err = tester.UpdateCloneSet(cs.Name, func(cs *appsv1alpha1.CloneSet) {
					if cs.Annotations == nil {
						cs.Annotations = map[string]string{}
					}
					fn(&cs.Spec.Template)
				})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				ginkgo.By("Wait for CloneSet generation consistent")
				gomega.Eventually(func() bool {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Generation == cs.Status.ObservedGeneration
				}, 10*time.Second, 3*time.Second).Should(gomega.Equal(true))

				ginkgo.By("Wait for all pods updated and ready")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.UpdatedAvailableReplicas
				}, 600*time.Second, 3*time.Second).Should(gomega.Equal(int32(1)))

				ginkgo.By("Verify the containerID changed and restartCount should be 1")
				pods, err = tester.ListPodsForCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(pods)).Should(gomega.Equal(1))
				newPodUID := pods[0].UID
				newContainerStatus := pods[0].Status.ContainerStatuses[0]

				gomega.Expect(oldPodUID).Should(gomega.Equal(newPodUID))
				gomega.Expect(newContainerStatus.ContainerID).NotTo(gomega.Equal(oldContainerStatus.ContainerID))
				gomega.Expect(newContainerStatus.RestartCount).Should(gomega.Equal(int32(1)))

				pods, err = tester.ListPodsForCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(checkPodResource(pods, oldPodResource, []string{"redis"})).Should(gomega.Equal(true))
			}
			// This can't be Conformance yet.
			ginkgo.It("in-place update image and resource", func() {
				fn := func(pod *v1.PodTemplateSpec) {
					spec := &pod.Spec
					ginkgo.By("in-place update image and resource")
					spec.Containers[0].Image = NewNginxImage
					spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("300m")
					spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("300Mi")
					spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("2")
					spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("2Gi")
				}
				testUpdateResource(fn, resizePolicy)
			})

			// This can't be Conformance yet.
			ginkgo.FIt("in-place update resource and env from label", func() {
				fn := func(pod *v1.PodTemplateSpec) {
					spec := &pod.Spec
					ginkgo.By("in-place update resource and env from label")
					pod.Labels["test-env"] = "bar"
					spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("300m")
					spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("300Mi")
					spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("2")
					spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("2Gi")
				}
				testUpdateResource(fn, resizePolicy)
			})

			// This can't be Conformance yet.
			ginkgo.It("in-place update image, resource and env from label", func() {
				fn := func(pod *v1.PodTemplateSpec) {
					spec := &pod.Spec
					ginkgo.By("in-place update image, resource and env from label")
					spec.Containers[0].Image = NewNginxImage
					pod.Labels["test-env"] = "bar"
					spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("300m")
					spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("300Mi")
					spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("2")
					spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("2Gi")
				}
				testUpdateResource(fn, resizePolicy)
			})

			framework.ConformanceIt("in-place update two container image, resource with priorities successfully", func() {
				cs := tester.NewCloneSet("clone-"+randStr, 1, appsv1alpha1.CloneSetUpdateStrategy{Type: appsv1alpha1.InPlaceIfPossibleCloneSetUpdateStrategyType})
				cs.Spec.Template.Spec.Containers[0].ResizePolicy = resizePolicy
				cs.Spec.Template.Spec.Containers = append(cs.Spec.Template.Spec.Containers, v1.Container{
					Name:      "redis",
					Image:     RedisImage,
					Command:   []string{"sleep", "999"},
					Env:       []v1.EnvVar{{Name: appspub.ContainerLaunchPriorityEnvName, Value: "10"}},
					Lifecycle: &v1.Lifecycle{PostStart: &v1.LifecycleHandler{Exec: &v1.ExecAction{Command: []string{"sleep", "10"}}}},
				})
				cs.Spec.Template.Spec.TerminationGracePeriodSeconds = utilpointer.Int64(3)
				cs, err = tester.CreateCloneSet(cs)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(cs.Spec.UpdateStrategy.Type).To(gomega.Equal(appsv1alpha1.InPlaceIfPossibleCloneSetUpdateStrategyType))

				ginkgo.By("Wait for replicas satisfied")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.Replicas
				}, 3*time.Second, time.Second).Should(gomega.Equal(int32(1)))

				ginkgo.By("Wait for all pods ready")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.ReadyReplicas
				}, 60*time.Second, 3*time.Second).Should(gomega.Equal(int32(1)))

				pods, err := tester.ListPodsForCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(pods)).Should(gomega.Equal(1))
				oldPodResource := getPodResource(pods[0])

				ginkgo.By("Update images of nginx and redis")
				err = tester.UpdateCloneSet(cs.Name, func(cs *appsv1alpha1.CloneSet) {
					cs.Spec.Template.Spec.Containers[0].Image = NewNginxImage
					cs.Spec.Template.Spec.Containers[1].Image = imageutils.GetE2EImage(imageutils.BusyBox)
					cs.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("300m")
					cs.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("300Mi")
					cs.Spec.Template.Spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("2")
					cs.Spec.Template.Spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("2Gi")
				})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				ginkgo.By("Wait for CloneSet generation consistent")
				gomega.Eventually(func() bool {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Generation == cs.Status.ObservedGeneration
				}, 10*time.Second, 3*time.Second).Should(gomega.Equal(true))

				ginkgo.By("Wait for all pods updated and ready")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.UpdatedAvailableReplicas
				}, 600*time.Second, 3*time.Second).Should(gomega.Equal(int32(1)))

				ginkgo.By("Verify two containers have all updated in-place")
				pods, err = tester.ListPodsForCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(pods)).Should(gomega.Equal(1))

				pod := pods[0]
				nginxContainerStatus := util.GetContainerStatus("nginx", pod)
				redisContainerStatus := util.GetContainerStatus("redis", pod)
				gomega.Expect(nginxContainerStatus.RestartCount).Should(gomega.Equal(int32(1)))
				gomega.Expect(redisContainerStatus.RestartCount).Should(gomega.Equal(int32(1)))

				ginkgo.By("Verify nginx should be stopped after new redis has started 10s")
				gomega.Expect(nginxContainerStatus.LastTerminationState.Terminated.FinishedAt.After(redisContainerStatus.State.Running.StartedAt.Time.Add(time.Second*10))).
					Should(gomega.Equal(true), fmt.Sprintf("nginx finish at %v is not after redis start %v + 10s",
						nginxContainerStatus.LastTerminationState.Terminated.FinishedAt,
						redisContainerStatus.State.Running.StartedAt))

				ginkgo.By("Verify in-place update state in two batches")
				inPlaceUpdateState := appspub.InPlaceUpdateState{}
				gomega.Expect(pod.Annotations[appspub.InPlaceUpdateStateKey]).ShouldNot(gomega.BeEmpty())
				err = json.Unmarshal([]byte(pod.Annotations[appspub.InPlaceUpdateStateKey]), &inPlaceUpdateState)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(inPlaceUpdateState.ContainerBatchesRecord)).Should(gomega.Equal(2))
				gomega.Expect(inPlaceUpdateState.ContainerBatchesRecord[0].Containers).Should(gomega.Equal([]string{"redis"}))
				gomega.Expect(inPlaceUpdateState.ContainerBatchesRecord[1].Containers).Should(gomega.Equal([]string{"nginx"}))
				gomega.Expect(checkPodResource(pods, oldPodResource, []string{"redis"})).Should(gomega.Equal(true))
			})

			framework.ConformanceIt("in-place update two container image, resource with priorities, should not update the next when the previous one failed", func() {
				cs := tester.NewCloneSet("clone-"+randStr, 1, appsv1alpha1.CloneSetUpdateStrategy{Type: appsv1alpha1.InPlaceIfPossibleCloneSetUpdateStrategyType})
				cs.Spec.Template.Spec.Containers = append(cs.Spec.Template.Spec.Containers, v1.Container{
					Name:      "redis",
					Image:     RedisImage,
					Env:       []v1.EnvVar{{Name: appspub.ContainerLaunchPriorityEnvName, Value: "10"}},
					Lifecycle: &v1.Lifecycle{PostStart: &v1.LifecycleHandler{Exec: &v1.ExecAction{Command: []string{"sleep", "10"}}}},
				})
				cs.Spec.Template.Spec.TerminationGracePeriodSeconds = utilpointer.Int64(3)
				cs, err = tester.CreateCloneSet(cs)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(cs.Spec.UpdateStrategy.Type).To(gomega.Equal(appsv1alpha1.InPlaceIfPossibleCloneSetUpdateStrategyType))

				ginkgo.By("Wait for replicas satisfied")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.Replicas
				}, 3*time.Second, time.Second).Should(gomega.Equal(int32(1)))

				ginkgo.By("Wait for all pods ready")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.ReadyReplicas
				}, 60*time.Second, 3*time.Second).Should(gomega.Equal(int32(1)))

				pods, err := tester.ListPodsForCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(pods)).Should(gomega.Equal(1))
				oldPodResource := getPodResource(pods[0])

				ginkgo.By("Update images of nginx and redis")
				err = tester.UpdateCloneSet(cs.Name, func(cs *appsv1alpha1.CloneSet) {
					cs.Spec.Template.Spec.Containers[0].Image = NewNginxImage
					cs.Spec.Template.Spec.Containers[1].Image = imageutils.GetE2EImage(imageutils.BusyBox)
					cs.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("300m")
					cs.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("300Mi")
					cs.Spec.Template.Spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("2")
					cs.Spec.Template.Spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("2Gi")
				})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				ginkgo.By("Wait for CloneSet generation consistent")
				gomega.Eventually(func() bool {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Generation == cs.Status.ObservedGeneration
				}, 10*time.Second, 3*time.Second).Should(gomega.Equal(true))

				ginkgo.By("Wait for redis failed to start")
				var pod *v1.Pod
				gomega.Eventually(func() *v1.ContainerStateTerminated {
					pods, err = tester.ListPodsForCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(len(pods)).Should(gomega.Equal(1))
					pod = pods[0]
					redisContainerStatus := util.GetContainerStatus("redis", pod)
					return redisContainerStatus.LastTerminationState.Terminated
				}, 60*time.Second, time.Second).ShouldNot(gomega.BeNil())

				gomega.Eventually(func() *v1.ContainerStateWaiting {
					pods, err = tester.ListPodsForCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					gomega.Expect(len(pods)).Should(gomega.Equal(1))
					pod = pods[0]
					redisContainerStatus := util.GetContainerStatus("redis", pod)
					return redisContainerStatus.State.Waiting
				}, 60*time.Second, time.Second).ShouldNot(gomega.BeNil())

				nginxContainerStatus := util.GetContainerStatus("nginx", pod)
				gomega.Expect(nginxContainerStatus.RestartCount).Should(gomega.Equal(int32(0)))

				ginkgo.By("Verify in-place update state only one batch and remain next")
				inPlaceUpdateState := appspub.InPlaceUpdateState{}
				gomega.Expect(pod.Annotations[appspub.InPlaceUpdateStateKey]).ShouldNot(gomega.BeEmpty())
				err = json.Unmarshal([]byte(pod.Annotations[appspub.InPlaceUpdateStateKey]), &inPlaceUpdateState)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(inPlaceUpdateState.ContainerBatchesRecord)).Should(gomega.Equal(1))
				gomega.Expect(inPlaceUpdateState.ContainerBatchesRecord[0].Containers).Should(gomega.Equal([]string{"redis"}))
				gomega.Expect(inPlaceUpdateState.NextContainerImages).Should(gomega.Equal(map[string]string{"nginx": NewNginxImage}))
				gomega.Expect(checkPodResource(pods, oldPodResource, []string{"redis"})).Should(gomega.Equal(false))
			})

			//This can't be Conformance yet.
			ginkgo.It("in-place update two container image, resource and env from metadata with priorities", func() {
				cs := tester.NewCloneSet("clone-"+randStr, 1, appsv1alpha1.CloneSetUpdateStrategy{Type: appsv1alpha1.InPlaceIfPossibleCloneSetUpdateStrategyType})
				cs.Spec.Template.Spec.Containers[0].ResizePolicy = resizePolicy
				cs.Spec.Template.Annotations = map[string]string{"config": "foo"}
				cs.Spec.Template.Spec.Containers = append(cs.Spec.Template.Spec.Containers, v1.Container{
					Name:  "redis",
					Image: RedisImage,
					Env: []v1.EnvVar{
						{Name: appspub.ContainerLaunchPriorityEnvName, Value: "10"},
						{Name: "CONFIG", ValueFrom: &v1.EnvVarSource{FieldRef: &v1.ObjectFieldSelector{FieldPath: "metadata.annotations['config']"}}},
					},
					Lifecycle: &v1.Lifecycle{PostStart: &v1.LifecycleHandler{Exec: &v1.ExecAction{Command: []string{"sleep", "10"}}}},
				})
				cs, err = tester.CreateCloneSet(cs)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(cs.Spec.UpdateStrategy.Type).To(gomega.Equal(appsv1alpha1.InPlaceIfPossibleCloneSetUpdateStrategyType))

				ginkgo.By("Wait for replicas satisfied")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.Replicas
				}, 3*time.Second, time.Second).Should(gomega.Equal(int32(1)))

				ginkgo.By("Wait for all pods ready")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.ReadyReplicas
				}, 60*time.Second, 3*time.Second).Should(gomega.Equal(int32(1)))

				pods, err := tester.ListPodsForCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(pods)).Should(gomega.Equal(1))
				oldPodResource := getPodResource(pods[0])

				ginkgo.By("Update nginx image and config annotation")
				err = tester.UpdateCloneSet(cs.Name, func(cs *appsv1alpha1.CloneSet) {
					cs.Spec.Template.Spec.Containers[0].Image = NewNginxImage
					cs.Spec.Template.Annotations["config"] = "bar"
					cs.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU] = resource.MustParse("300m")
					cs.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceMemory] = resource.MustParse("300Mi")
					cs.Spec.Template.Spec.Containers[0].Resources.Limits[v1.ResourceCPU] = resource.MustParse("2")
					cs.Spec.Template.Spec.Containers[0].Resources.Limits[v1.ResourceMemory] = resource.MustParse("2Gi")
				})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				ginkgo.By("Wait for CloneSet generation consistent")
				gomega.Eventually(func() bool {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Generation == cs.Status.ObservedGeneration
				}, 10*time.Second, 3*time.Second).Should(gomega.Equal(true))

				ginkgo.By("Wait for all pods updated and ready")
				gomega.Eventually(func() int32 {
					cs, err = tester.GetCloneSet(cs.Name)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					return cs.Status.UpdatedAvailableReplicas
				}, 600*time.Second, 3*time.Second).Should(gomega.Equal(int32(1)))

				ginkgo.By("Verify two containers have all updated in-place")
				pods, err = tester.ListPodsForCloneSet(cs.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(pods)).Should(gomega.Equal(1))

				pod := pods[0]
				nginxContainerStatus := util.GetContainerStatus("nginx", pod)
				redisContainerStatus := util.GetContainerStatus("redis", pod)
				gomega.Expect(nginxContainerStatus.RestartCount).Should(gomega.Equal(int32(1)))
				gomega.Expect(redisContainerStatus.RestartCount).Should(gomega.Equal(int32(1)))

				ginkgo.By("Verify nginx should be stopped after new redis has started")
				gomega.Expect(nginxContainerStatus.LastTerminationState.Terminated.FinishedAt.After(redisContainerStatus.State.Running.StartedAt.Time.Add(time.Second*10))).
					Should(gomega.Equal(true), fmt.Sprintf("nginx finish at %v is not after redis start %v + 10s",
						nginxContainerStatus.LastTerminationState.Terminated.FinishedAt,
						redisContainerStatus.State.Running.StartedAt))

				ginkgo.By("Verify in-place update state in two batches")
				inPlaceUpdateState := appspub.InPlaceUpdateState{}
				gomega.Expect(pod.Annotations[appspub.InPlaceUpdateStateKey]).ShouldNot(gomega.BeEmpty())
				err = json.Unmarshal([]byte(pod.Annotations[appspub.InPlaceUpdateStateKey]), &inPlaceUpdateState)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(inPlaceUpdateState.ContainerBatchesRecord)).Should(gomega.Equal(2))
				gomega.Expect(inPlaceUpdateState.ContainerBatchesRecord[0].Containers).Should(gomega.Equal([]string{"redis"}))
				gomega.Expect(inPlaceUpdateState.ContainerBatchesRecord[1].Containers).Should(gomega.Equal([]string{"nginx"}))
				gomega.Expect(checkPodResource(pods, oldPodResource, []string{"redis"})).Should(gomega.Equal(true))
			})
		}

		ginkgo.By("inplace update resources with RestartContainer policy")
		testWithResizePolicy([]v1.ContainerResizePolicy{
			{
				ResourceName: v1.ResourceCPU, RestartPolicy: v1.RestartContainer,
			},
			{
				ResourceName: v1.ResourceMemory, RestartPolicy: v1.RestartContainer,
			},
		})
		ginkgo.By("inplace update resources with memory RestartContainer policy")
		testWithResizePolicy([]v1.ContainerResizePolicy{
			{
				ResourceName: v1.ResourceMemory, RestartPolicy: v1.RestartContainer,
			},
		})
		ginkgo.By("inplace update resources with cpu RestartContainer policy")
		testWithResizePolicy([]v1.ContainerResizePolicy{
			{
				ResourceName: v1.ResourceCPU, RestartPolicy: v1.RestartContainer,
			},
		})
		ginkgo.By("inplace update resources with NotRequired policy")
		testWithResizePolicy([]v1.ContainerResizePolicy{
			{
				ResourceName: v1.ResourceCPU, RestartPolicy: v1.NotRequired,
			},
			{
				ResourceName: v1.ResourceMemory, RestartPolicy: v1.NotRequired,
			},
		})
	})

	framework.KruiseDescribe("Basic StatefulSet functionality [StatefulSetBasic]", func() {
		ssName := "ss"
		labels := map[string]string{
			"foo": "bar",
			"baz": "blah",
		}
		headlessSvcName := "test"
		var statefulPodMounts, podMounts []v1.VolumeMount
		var ss *appsv1beta1.StatefulSet

		ginkgo.BeforeEach(func() {
			statefulPodMounts = []v1.VolumeMount{{Name: "datadir", MountPath: "/data/"}}
			podMounts = []v1.VolumeMount{{Name: "home", MountPath: "/home"}}
			ss = framework.NewStatefulSet(ssName, ns, headlessSvcName, 2, statefulPodMounts, podMounts, labels)

			ginkgo.By("Creating service " + headlessSvcName + " in namespace " + ns)
			headlessService := framework.CreateServiceSpec(headlessSvcName, "", true, labels)
			_, err := c.CoreV1().Services(ns).Create(context.TODO(), headlessService, metav1.CreateOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		oldResource := v1.ResourceRequirements{
			Requests: map[v1.ResourceName]resource.Quantity{
				v1.ResourceCPU:    resource.MustParse("200m"),
				v1.ResourceMemory: resource.MustParse("200Mi"),
			},
			Limits: map[v1.ResourceName]resource.Quantity{
				v1.ResourceCPU:    resource.MustParse("1"),
				v1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}
		newResource := v1.ResourceRequirements{
			Requests: map[v1.ResourceName]resource.Quantity{
				v1.ResourceCPU:    resource.MustParse("300m"),
				v1.ResourceMemory: resource.MustParse("300Mi"),
			},
			Limits: map[v1.ResourceName]resource.Quantity{
				v1.ResourceCPU:    resource.MustParse("2"),
				v1.ResourceMemory: resource.MustParse("2Gi"),
			},
		}
		testfn := func(ss *appsv1beta1.StatefulSet) {
			newImage := NewNginxImage
			oldImage := ss.Spec.Template.Spec.Containers[0].Image
			currentRevision, updateRevision := ss.Status.CurrentRevision, ss.Status.UpdateRevision
			updateFn := func(set *appsv1beta1.StatefulSet) {
				currentRevision = set.Status.CurrentRevision
				set.Spec.Template.Spec.Containers[0].Image = newImage
				container := &set.Spec.Template.Spec.Containers[0]
				container.Resources = newResource
			}
			sst := framework.NewStatefulSetTester(c, kc)

			validaFn := func(pods []v1.Pod) {
				ss = sst.WaitForStatus(ss)
				updateRevision = ss.Status.UpdateRevision
				baseValidFn(pods, newImage, updateRevision)
				for i := range pods {
					diff.ObjectDiff(pods[i].Spec.Containers[0].Resources, newResource)
				}
			}
			rollbackFn := func(set *appsv1beta1.StatefulSet) {
				set.Spec.Template.Spec.Containers[0].Image = oldImage
				container := &set.Spec.Template.Spec.Containers[0]
				container.Resources = oldResource
			}
			validaFn2 := func(pods []v1.Pod) {
				ss = sst.WaitForStatus(ss)
				baseValidFn(pods, oldImage, currentRevision)
				for i := range pods {
					diff.ObjectDiff(pods[i].Spec.Containers[0].Resources, oldResource)
				}
			}
			rollbackWithUpdateFnTest(c, kc, ns, ss, updateFn, rollbackFn, validaFn, validaFn2)
		}

		ginkgo.It("should perform rolling updates(including resources) and roll backs with pvcs", func() {
			ginkgo.By("Creating a new StatefulSet")
			ss.Spec.UpdateStrategy.RollingUpdate = &appsv1beta1.RollingUpdateStatefulSetStrategy{
				PodUpdatePolicy: appsv1beta1.InPlaceIfPossiblePodUpdateStrategyType,
			}
			ss.Spec.Template.Spec.ReadinessGates = []v1.PodReadinessGate{{ConditionType: appspub.InPlaceUpdateReady}}
			testfn(ss)
		})

		ginkgo.It("should perform rolling updates(including resources) and roll backs", func() {
			ginkgo.By("Creating a new StatefulSet")
			ss = framework.NewStatefulSet("ss2", ns, headlessSvcName, 3, nil, nil, labels)
			ss.Spec.UpdateStrategy.RollingUpdate = &appsv1beta1.RollingUpdateStatefulSetStrategy{
				PodUpdatePolicy: appsv1beta1.InPlaceIfPossiblePodUpdateStrategyType,
			}
			ss.Spec.Template.Spec.ReadinessGates = []v1.PodReadinessGate{{ConditionType: appspub.InPlaceUpdateReady}}
			testfn(ss)
		})

	})
})

func ResourceEqual(spec, status *v1.ResourceRequirements) bool {

	if spec == nil && status == nil {
		return true
	}
	if status == nil || spec == nil {
		return false
	}
	if spec.Requests != nil {
		if status.Requests == nil {
			return false
		}
		if !spec.Requests.Cpu().Equal(*status.Requests.Cpu()) ||
			!spec.Requests.Memory().Equal(*status.Requests.Memory()) {
			return false
		}
	}
	if spec.Limits != nil {
		if status.Limits == nil {
			return false
		}
		if !spec.Limits.Cpu().Equal(*status.Limits.Cpu()) ||
			!spec.Limits.Memory().Equal(*status.Limits.Memory()) {
			return false
		}
	}
	return true
}

func getPodResource(pod *v1.Pod) map[string]*v1.ResourceRequirements {
	containerResource := map[string]*v1.ResourceRequirements{}
	for _, c := range pod.Spec.Containers {
		c := c
		containerResource[c.Name] = &c.Resources
	}
	return containerResource
}

func checkPodResource(pods []*v1.Pod, last map[string]*v1.ResourceRequirements, ignoreContainer []string) (res bool) {
	defer func() {
		if !res && len(last) == 1 {
			pjson, _ := json.Marshal(pods)
			ljson, _ := json.Marshal(last)
			framework.Logf("pod %v, last resource %v", string(pjson), string(ljson))
		}
	}()
	for _, pod := range pods {
		containerResource := getPodResource(pod)
		for _, c := range pod.Spec.Containers {
			if slice.ContainsString(ignoreContainer, c.Name, nil) {
				continue
			}
			lastResource := last[c.Name]
			if ResourceEqual(&c.Resources, lastResource) {
				framework.Logf("container %s resource unchanged", c.Name)
				// resource unchanged
				return false
			}
		}
		for _, cs := range pod.Status.ContainerStatuses {
			cname := cs.Name
			spec := containerResource[cname]
			if !ResourceEqual(spec, cs.Resources) {
				framework.Logf("container %v spec != status", cname)
				// resource spec != status
				return false
			}
		}
	}

	// resource changed and spec = status
	return true
}

func baseValidFn(pods []v1.Pod, image string, revision string) {
	for i := range pods {
		gomega.Expect(pods[i].Spec.Containers[0].Image).To(gomega.Equal(image),
			fmt.Sprintf(" Pod %s/%s has image %s not have image %s",
				pods[i].Namespace,
				pods[i].Name,
				pods[i].Spec.Containers[0].Image,
				image))
		gomega.Expect(pods[i].Labels[apps.StatefulSetRevisionLabel]).To(gomega.Equal(revision),
			fmt.Sprintf("Pod %s/%s revision %s is not equal to revision %s",
				pods[i].Namespace,
				pods[i].Name,
				pods[i].Labels[apps.StatefulSetRevisionLabel],
				revision))
	}
}
func rollbackWithUpdateFnTest(c clientset.Interface, kc kruiseclientset.Interface, ns string, ss *appsv1beta1.StatefulSet,
	updateFn, rollbackFn func(update *appsv1beta1.StatefulSet), validateFn1, validateFn2 func([]v1.Pod)) {
	sst := framework.NewStatefulSetTester(c, kc)
	sst.SetHTTPProbe(ss)
	ss, err := kc.AppsV1beta1().StatefulSets(ns).Create(context.TODO(), ss, metav1.CreateOptions{})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	sst.WaitForRunningAndReady(*ss.Spec.Replicas, ss)
	ss = sst.WaitForStatus(ss)
	currentRevision, updateRevision := ss.Status.CurrentRevision, ss.Status.UpdateRevision
	gomega.Expect(currentRevision).To(gomega.Equal(updateRevision),
		fmt.Sprintf("StatefulSet %s/%s created with update revision %s not equal to current revision %s",
			ss.Namespace, ss.Name, updateRevision, currentRevision))
	pods := sst.GetPodList(ss)
	for i := range pods.Items {
		gomega.Expect(pods.Items[i].Labels[apps.StatefulSetRevisionLabel]).To(gomega.Equal(currentRevision),
			fmt.Sprintf("Pod %s/%s revision %s is not equal to current revision %s",
				pods.Items[i].Namespace,
				pods.Items[i].Name,
				pods.Items[i].Labels[apps.StatefulSetRevisionLabel],
				currentRevision))
	}
	sst.SortStatefulPods(pods)
	err = sst.BreakPodHTTPProbe(ss, &pods.Items[1])
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ss, pods = sst.WaitForPodNotReady(ss, pods.Items[1].Name)
	newImage := NewNginxImage
	oldImage := ss.Spec.Template.Spec.Containers[0].Image

	ginkgo.By(fmt.Sprintf("Updating StatefulSet template: update image from %s to %s", oldImage, newImage))
	gomega.Expect(oldImage).NotTo(gomega.Equal(newImage), "Incorrect test setup: should update to a different image")
	ss, err = framework.UpdateStatefulSetWithRetries(kc, ns, ss.Name, updateFn)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	ginkgo.By("Creating a new revision")
	ss = sst.WaitForStatus(ss)
	currentRevision, updateRevision = ss.Status.CurrentRevision, ss.Status.UpdateRevision
	gomega.Expect(currentRevision).NotTo(gomega.Equal(updateRevision),
		"Current revision should not equal update revision during rolling update")

	ginkgo.By("Updating Pods in reverse ordinal order")
	pods = sst.GetPodList(ss)
	sst.SortStatefulPods(pods)
	err = sst.RestorePodHTTPProbe(ss, &pods.Items[1])
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ss, pods = sst.WaitForPodReady(ss, pods.Items[1].Name)
	ss, pods = sst.WaitForRollingUpdate(ss)
	gomega.Expect(ss.Status.CurrentRevision).To(gomega.Equal(updateRevision),
		fmt.Sprintf("StatefulSet %s/%s current revision %s does not equal update revision %s on update completion",
			ss.Namespace,
			ss.Name,
			ss.Status.CurrentRevision,
			updateRevision))
	validateFn1(pods.Items)

	ginkgo.By("Rolling back to a previous revision")
	err = sst.BreakPodHTTPProbe(ss, &pods.Items[1])
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ss, pods = sst.WaitForPodNotReady(ss, pods.Items[1].Name)
	priorRevision := currentRevision
	ss, err = framework.UpdateStatefulSetWithRetries(kc, ns, ss.Name, rollbackFn)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ss = sst.WaitForStatus(ss)
	currentRevision, updateRevision = ss.Status.CurrentRevision, ss.Status.UpdateRevision
	gomega.Expect(currentRevision).NotTo(gomega.Equal(updateRevision),
		"Current revision should not equal update revision during roll back")
	gomega.Expect(priorRevision).To(gomega.Equal(updateRevision),
		"Prior revision should equal update revision during roll back")

	ginkgo.By("Rolling back update in reverse ordinal order")
	pods = sst.GetPodList(ss)
	sst.SortStatefulPods(pods)
	sst.RestorePodHTTPProbe(ss, &pods.Items[1])
	ss, pods = sst.WaitForPodReady(ss, pods.Items[1].Name)
	ss, pods = sst.WaitForRollingUpdate(ss)
	gomega.Expect(ss.Status.CurrentRevision).To(gomega.Equal(priorRevision),
		fmt.Sprintf("StatefulSet %s/%s current revision %s does not equal prior revision %s on rollback completion",
			ss.Namespace,
			ss.Name,
			ss.Status.CurrentRevision,
			updateRevision))
	validateFn2(pods.Items)
}
