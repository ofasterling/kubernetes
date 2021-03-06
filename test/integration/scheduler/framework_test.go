/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scheduler

import (
	"fmt"
	"testing"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	framework "k8s.io/kubernetes/pkg/scheduler/framework/v1alpha1"
)

// TesterPlugin is common ancestor for a test plugin that allows injection of
// failures and some other test functionalities.
type TesterPlugin struct {
	numReserveCalled   int
	numPrebindCalled   int
	numUnreserveCalled int
	failReserve        bool
	failPrebind        bool
	rejectPrebind      bool
}

type ReservePlugin struct {
	TesterPlugin
}

type PrebindPlugin struct {
	TesterPlugin
}

type UnreservePlugin struct {
	TesterPlugin
}

const (
	reservePluginName   = "reserve-plugin"
	prebindPluginName   = "prebind-plugin"
	unreservePluginName = "unreserve-plugin"
)

var _ = framework.ReservePlugin(&ReservePlugin{})
var _ = framework.PrebindPlugin(&PrebindPlugin{})
var _ = framework.UnreservePlugin(&UnreservePlugin{})

// Name returns name of the plugin.
func (rp *ReservePlugin) Name() string {
	return reservePluginName
}

var resPlugin = &ReservePlugin{}

// Reserve is a test function that returns an error or nil, depending on the
// value of "failReserve".
func (rp *ReservePlugin) Reserve(pc *framework.PluginContext, pod *v1.Pod, nodeName string) *framework.Status {
	rp.numReserveCalled++
	if rp.failReserve {
		return framework.NewStatus(framework.Error, fmt.Sprintf("injecting failure for pod %v", pod.Name))
	}
	return nil
}

// NewReservePlugin is the factory for reserve plugin.
func NewReservePlugin(_ *runtime.Unknown, _ framework.FrameworkHandle) (framework.Plugin, error) {
	return resPlugin, nil
}

var pbdPlugin = &PrebindPlugin{}

// Name returns name of the plugin.
func (pp *PrebindPlugin) Name() string {
	return prebindPluginName
}

// Prebind is a test function that returns (true, nil) or errors for testing.
func (pp *PrebindPlugin) Prebind(pc *framework.PluginContext, pod *v1.Pod, nodeName string) *framework.Status {
	pp.numPrebindCalled++
	if pp.failPrebind {
		return framework.NewStatus(framework.Error, fmt.Sprintf("injecting failure for pod %v", pod.Name))
	}
	if pp.rejectPrebind {
		return framework.NewStatus(framework.Unschedulable, fmt.Sprintf("reject pod %v", pod.Name))
	}
	return nil
}

// reset used to reset numPrebindCalled.
func (pp *PrebindPlugin) reset() {
	pp.numPrebindCalled = 0
}

// NewPrebindPlugin is the factory for prebind plugin.
func NewPrebindPlugin(_ *runtime.Unknown, _ framework.FrameworkHandle) (framework.Plugin, error) {
	return pbdPlugin, nil
}

var unresPlugin = &UnreservePlugin{}

// Name returns name of the plugin.
func (up *UnreservePlugin) Name() string {
	return unreservePluginName
}

// Unreserve is a test function that returns an error or nil, depending on the
// value of "failUnreserve".
func (up *UnreservePlugin) Unreserve(pc *framework.PluginContext, pod *v1.Pod, nodeName string) {
	up.numUnreserveCalled++
}

// reset used to reset numUnreserveCalled.
func (up *UnreservePlugin) reset() {
	up.numUnreserveCalled = 0
}

// NewUnreservePlugin is the factory for unreserve plugin.
func NewUnreservePlugin(_ *runtime.Unknown, _ framework.FrameworkHandle) (framework.Plugin, error) {
	return unresPlugin, nil
}

// TestReservePlugin tests invocation of reserve plugins.
func TestReservePlugin(t *testing.T) {
	// Create a plugin registry for testing. Register only a reserve plugin.
	registry := framework.Registry{reservePluginName: NewReservePlugin}

	// Create the master and the scheduler with the test plugin set.
	context := initTestSchedulerWithOptions(t,
		initTestMaster(t, "reserve-plugin", nil),
		false, nil, registry, false, time.Second)
	defer cleanupTest(t, context)

	cs := context.clientSet
	// Add a few nodes.
	_, err := createNodes(cs, "test-node", nil, 2)
	if err != nil {
		t.Fatalf("Cannot create nodes: %v", err)
	}

	for _, fail := range []bool{false, true} {
		resPlugin.failReserve = fail
		// Create a best effort pod.
		pod, err := createPausePod(cs,
			initPausePod(cs, &pausePodConfig{Name: "test-pod", Namespace: context.ns.Name}))
		if err != nil {
			t.Errorf("Error while creating a test pod: %v", err)
		}

		if fail {
			if err = wait.Poll(10*time.Millisecond, 30*time.Second, podSchedulingError(cs, pod.Namespace, pod.Name)); err != nil {
				t.Errorf("Didn't expected the pod to be scheduled. error: %v", err)
			}
		} else {
			if err = waitForPodToSchedule(cs, pod); err != nil {
				t.Errorf("Expected the pod to be scheduled. error: %v", err)
			}
		}

		if resPlugin.numReserveCalled == 0 {
			t.Errorf("Expected the reserve plugin to be called.")
		}

		cleanupPods(cs, t, []*v1.Pod{pod})
	}
}

// TestPrebindPlugin tests invocation of prebind plugins.
func TestPrebindPlugin(t *testing.T) {
	// Create a plugin registry for testing. Register only a reserve plugin.
	registry := framework.Registry{prebindPluginName: NewPrebindPlugin}

	// Create the master and the scheduler with the test plugin set.
	context := initTestSchedulerWithOptions(t,
		initTestMaster(t, "prebind-plugin", nil),
		false, nil, registry, false, time.Second)
	defer cleanupTest(t, context)

	cs := context.clientSet
	// Add a few nodes.
	_, err := createNodes(cs, "test-node", nil, 2)
	if err != nil {
		t.Fatalf("Cannot create nodes: %v", err)
	}

	tests := []struct {
		fail   bool
		reject bool
	}{
		{
			fail:   false,
			reject: false,
		},
		{
			fail:   true,
			reject: false,
		},
		{
			fail:   false,
			reject: true,
		},
		{
			fail:   true,
			reject: true,
		},
	}

	for i, test := range tests {
		pbdPlugin.failPrebind = test.fail
		pbdPlugin.rejectPrebind = test.reject
		// Create a best effort pod.
		pod, err := createPausePod(cs,
			initPausePod(cs, &pausePodConfig{Name: "test-pod", Namespace: context.ns.Name}))
		if err != nil {
			t.Errorf("Error while creating a test pod: %v", err)
		}

		if test.fail {
			if err = wait.Poll(10*time.Millisecond, 30*time.Second, podSchedulingError(cs, pod.Namespace, pod.Name)); err != nil {
				t.Errorf("test #%v: Expected a scheduling error, but didn't get it. error: %v", i, err)
			}
		} else {
			if test.reject {
				if err = waitForPodUnschedulable(cs, pod); err != nil {
					t.Errorf("test #%v: Didn't expected the pod to be scheduled. error: %v", i, err)
				}
			} else {
				if err = waitForPodToSchedule(cs, pod); err != nil {
					t.Errorf("test #%v: Expected the pod to be scheduled. error: %v", i, err)
				}
			}
		}

		if pbdPlugin.numPrebindCalled == 0 {
			t.Errorf("Expected the prebind plugin to be called.")
		}

		cleanupPods(cs, t, []*v1.Pod{pod})
	}
}

// TestUnreservePlugin tests invocation of un-reserve plugin
func TestUnreservePlugin(t *testing.T) {
	// TODO: register more plugin which would trigger un-reserve plugin
	registry := framework.Registry{
		unreservePluginName: NewUnreservePlugin,
		prebindPluginName:   NewPrebindPlugin,
	}

	// Create the master and the scheduler with the test plugin set.
	context := initTestSchedulerWithOptions(t,
		initTestMaster(t, "unreserve-plugin", nil),
		false, nil, registry, false, time.Second)
	defer cleanupTest(t, context)

	cs := context.clientSet
	// Add a few nodes.
	_, err := createNodes(cs, "test-node", nil, 2)
	if err != nil {
		t.Fatalf("Cannot create nodes: %v", err)
	}

	tests := []struct {
		prebindFail   bool
		prebindReject bool
	}{
		{
			prebindFail:   false,
			prebindReject: false,
		},
		{
			prebindFail:   true,
			prebindReject: false,
		},
		{
			prebindFail:   false,
			prebindReject: true,
		},
		{
			prebindFail:   true,
			prebindReject: true,
		},
	}

	for i, test := range tests {
		pbdPlugin.failPrebind = test.prebindFail
		pbdPlugin.rejectPrebind = test.prebindReject

		// Create a best effort pod.
		pod, err := createPausePod(cs,
			initPausePod(cs, &pausePodConfig{Name: "test-pod", Namespace: context.ns.Name}))
		if err != nil {
			t.Errorf("Error while creating a test pod: %v", err)
		}

		if test.prebindFail {
			if err = wait.Poll(10*time.Millisecond, 30*time.Second, podSchedulingError(cs, pod.Namespace, pod.Name)); err != nil {
				t.Errorf("test #%v: Expected a scheduling error, but didn't get it. error: %v", i, err)
			}
			if unresPlugin.numUnreserveCalled == 0 || unresPlugin.numUnreserveCalled != pbdPlugin.numPrebindCalled {
				t.Errorf("test #%v: Expected the unreserve plugin to be called %d times, was called %d times.", i, pbdPlugin.numPrebindCalled, unresPlugin.numUnreserveCalled)
			}
		} else {
			if test.prebindReject {
				if err = waitForPodUnschedulable(cs, pod); err != nil {
					t.Errorf("test #%v: Didn't expected the pod to be scheduled. error: %v", i, err)
				}
				if unresPlugin.numUnreserveCalled == 0 || unresPlugin.numUnreserveCalled != pbdPlugin.numPrebindCalled {
					t.Errorf("test #%v: Expected the unreserve plugin to be called %d times, was called %d times.", i, pbdPlugin.numPrebindCalled, unresPlugin.numUnreserveCalled)
				}
			} else {
				if err = waitForPodToSchedule(cs, pod); err != nil {
					t.Errorf("test #%v: Expected the pod to be scheduled. error: %v", i, err)
				}
				if unresPlugin.numUnreserveCalled > 0 {
					t.Errorf("test #%v: Didn't expected the unreserve plugin to be called, was called %d times.", i, unresPlugin.numUnreserveCalled)
				}
			}
		}
		unresPlugin.reset()
		pbdPlugin.reset()
		cleanupPods(cs, t, []*v1.Pod{pod})
	}
}
