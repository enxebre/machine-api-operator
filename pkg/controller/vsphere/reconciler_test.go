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

package vsphere

import (
	"context"
	"crypto/tls"
	"fmt"
	"reflect"
	"testing"

	"github.com/vmware/govmomi/vim25/mo"

	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	vsphereapi "github.com/openshift/machine-api-operator/pkg/apis/vsphereprovider/v1alpha1"
	"github.com/openshift/machine-api-operator/pkg/controller/vsphere/session"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func initSimulator(t *testing.T) (*simulator.Model, *session.Session, *simulator.Server) {
	model := simulator.VPX()
	model.Host = 0
	err := model.Create()
	if err != nil {
		t.Fatal(err)
	}
	model.Service.TLS = new(tls.Config)

	server := model.Service.NewServer()
	pass, _ := server.URL.User.Password()

	authSession, err := session.GetOrCreate(
		context.TODO(),
		server.URL.Host, "",
		server.URL.User.Username(), pass)
	if err != nil {
		t.Fatal(err)
	}
	// create folder
	folders, err := authSession.Datacenter.Folders(context.TODO())
	if err != nil {
		t.Fatal(err)
	}
	_, err = folders.VmFolder.CreateFolder(context.TODO(), "custom-folder")
	if err != nil {
		t.Fatal(err)
	}

	return model, authSession, server
}

func TestClone(t *testing.T) {
	model, _, server := initSimulator(t)
	defer model.Remove()
	defer server.Close()

	password, _ := server.URL.User.Password()
	namespace := "test"
	vm := simulator.Map.Any("VirtualMachine").(*simulator.VirtualMachine)
	credentialsSecret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: namespace,
		},
		Data: map[string][]byte{
			credentialsSecretUser:     []byte(server.URL.User.Username()),
			credentialsSecretPassword: []byte(password),
		},
	}

	testCases := []struct {
		testCase    string
		machine     func(t *testing.T) *machinev1.Machine
		expectError bool
		cloneVM     bool
	}{
		{
			testCase: "clone machine from default values",
			machine: func(t *testing.T) *machinev1.Machine {
				providerSpec := vsphereapi.VSphereMachineProviderSpec{
					CredentialsSecret: &corev1.LocalObjectReference{
						Name: "test",
					},
					Workspace: &vsphereapi.Workspace{
						Server: server.URL.Host,
					},
				}
				raw, err := vsphereapi.RawExtensionFromProviderSpec(&providerSpec)
				if err != nil {
					t.Fatal(err)
				}
				return &machinev1.Machine{
					ObjectMeta: metav1.ObjectMeta{
						UID:       "1",
						Name:      "defaultFolder",
						Namespace: namespace,
					},
					Spec: machinev1.MachineSpec{
						ProviderSpec: machinev1.ProviderSpec{
							Value: raw,
						},
					},
				}
			},
			expectError: false,
			cloneVM:     true,
		},
		{
			testCase: "does not clone machine if folder does not exist",
			machine: func(t *testing.T) *machinev1.Machine {
				providerSpec := vsphereapi.VSphereMachineProviderSpec{
					CredentialsSecret: &corev1.LocalObjectReference{
						Name: "test",
					},
					Workspace: &vsphereapi.Workspace{
						Server: server.URL.Host,
						Folder: "does-not-exists",
					},
				}
				raw, err := vsphereapi.RawExtensionFromProviderSpec(&providerSpec)
				if err != nil {
					t.Fatal(err)
				}
				return &machinev1.Machine{
					ObjectMeta: metav1.ObjectMeta{
						UID:       "2",
						Name:      "missingFolder",
						Namespace: namespace,
					},
					Spec: machinev1.MachineSpec{
						ProviderSpec: machinev1.ProviderSpec{
							Value: raw,
						},
					},
				}
			},
			expectError: true,
		},
		{
			testCase: "clone machine in specific folder",
			machine: func(t *testing.T) *machinev1.Machine {
				providerSpec := vsphereapi.VSphereMachineProviderSpec{
					CredentialsSecret: &corev1.LocalObjectReference{
						Name: "test",
					},

					Workspace: &vsphereapi.Workspace{
						Server: server.URL.Host,
						Folder: "custom-folder",
					},
				}
				raw, err := vsphereapi.RawExtensionFromProviderSpec(&providerSpec)
				if err != nil {
					t.Fatal(err)
				}
				return &machinev1.Machine{
					ObjectMeta: metav1.ObjectMeta{
						UID:       "3",
						Name:      "customFolder",
						Namespace: namespace,
					},
					Spec: machinev1.MachineSpec{
						ProviderSpec: machinev1.ProviderSpec{
							Value: raw,
						},
					},
				}
			},
			expectError: false,
			cloneVM:     true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testCase, func(t *testing.T) {
			machineScope, err := newMachineScope(machineScopeParams{
				client:  fake.NewFakeClientWithScheme(scheme.Scheme, &credentialsSecret),
				Context: context.TODO(),
				machine: tc.machine(t),
			})
			if err != nil {
				t.Fatal(err)
			}
			machineScope.providerSpec.Template = vm.Name

			if err := clone(machineScope); (err != nil) != tc.expectError {
				t.Errorf("Got: %v. Expected: %v", err, tc.expectError)
			}
			if tc.cloneVM {
				model.Machine++
			}
			if model.Machine != model.Count().Machine {
				t.Errorf("Unexpected number of machines. Expected: %v, got: %v", model.Machine, model.Count().Machine)
			}
		})
	}
}

func TestGetPowerState(t *testing.T) {
	model, session, server := initSimulator(t)
	defer model.Remove()
	defer server.Close()

	simulatorVM := simulator.Map.Any("VirtualMachine").(*simulator.VirtualMachine)
	ref := simulatorVM.VirtualMachine.Reference()

	testCases := []struct {
		testCase string
		vm       func(t *testing.T) *virtualMachine
		expected types.VirtualMachinePowerState
	}{
		{
			testCase: "powered off",
			vm: func(t *testing.T) *virtualMachine {
				vm := &virtualMachine{
					Context: context.TODO(),
					Obj:     object.NewVirtualMachine(session.Client.Client, ref),
					Ref:     ref,
				}
				_, err := vm.Obj.PowerOff(vm.Context)
				if err != nil {
					t.Fatal(err)
				}
				return vm
			},
			expected: types.VirtualMachinePowerStatePoweredOff,
		},
		{
			testCase: "powered on",
			vm: func(t *testing.T) *virtualMachine {
				vm := &virtualMachine{
					Context: context.TODO(),
					Obj:     object.NewVirtualMachine(session.Client.Client, ref),
					Ref:     ref,
				}
				_, err := vm.Obj.PowerOn(vm.Context)
				if err != nil {
					t.Fatal(err)
				}
				return vm
			},

			expected: types.VirtualMachinePowerStatePoweredOn,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.testCase, func(t *testing.T) {
			got, err := tc.vm(t).getPowerState()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.expected {
				t.Errorf("Got: %v, expected: %v", got, tc.expected)
			}
		})
	}
}

func TestTaskIsFinished(t *testing.T) {
	model, session, server := initSimulator(t)
	defer model.Remove()
	defer server.Close()

	obj := simulator.Map.Any("VirtualMachine").(*simulator.VirtualMachine)
	// Validate VM is powered on
	if obj.Runtime.PowerState != "poweredOn" {
		t.Fatal(obj.Runtime.PowerState)
	}
	vm := object.NewVirtualMachine(session.Client.Client, obj.Reference())
	task, err := vm.PowerOff(context.TODO())
	if err != nil {
		t.Fatal(err)
	}
	var moTask mo.Task
	moRef := types.ManagedObjectReference{
		Type:  "Task",
		Value: task.Reference().Value,
	}
	if err := session.RetrieveOne(context.TODO(), moRef, []string{"info"}, &moTask); err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		testCase    string
		moTask      func() *mo.Task
		expectError bool
		finished    bool
	}{
		{
			testCase: "existing taskRef",
			moTask: func() *mo.Task {
				return &moTask
			},
			expectError: false,
			finished:    true,
		},
		{
			testCase: "nil task",
			moTask: func() *mo.Task {
				return nil
			},
			expectError: false,
			finished:    true,
		},
		{
			testCase: "task succeeded is finished",
			moTask: func() *mo.Task {
				moTask.Info.State = types.TaskInfoStateSuccess
				return &moTask
			},
			expectError: false,
			finished:    true,
		},
		{
			testCase: "task error is finished",
			moTask: func() *mo.Task {
				moTask.Info.State = types.TaskInfoStateError
				return &moTask
			},
			expectError: false,
			finished:    true,
		},
		{
			testCase: "task running is not finished",
			moTask: func() *mo.Task {
				moTask.Info.State = types.TaskInfoStateRunning
				return &moTask
			},
			expectError: false,
			finished:    false,
		},
		{
			testCase: "task with unknown state errors",
			moTask: func() *mo.Task {
				moTask.Info.State = types.TaskInfoState("unknown")
				return &moTask
			},
			expectError: true,
			finished:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.testCase, func(t *testing.T) {
			finished, err := taskIsFinished(tc.moTask())
			if (err != nil) != tc.expectError {
				t.Errorf("Expected error: %v, got: %v", tc.expectError, err)
			}
			if finished != tc.finished {
				t.Errorf("Expected finished: %v, got: %v", tc.finished, finished)
			}
		})
	}
}

func TestGetNetworkDevices(t *testing.T) {
	model, session, server := initSimulator(t)
	defer model.Remove()
	defer server.Close()

	managedObj := simulator.Map.Any("VirtualMachine").(*simulator.VirtualMachine)
	objVM := object.NewVirtualMachine(session.Client.Client, managedObj.Reference())

	devices, err := objVM.Device(context.TODO())
	if err != nil {
		t.Fatal(err)
	}

	// checking network has been created by default
	_, err = session.Finder.Network(context.TODO(), "VM Network")
	if err != nil {
		t.Fatal(err)
	}

	testCases := []struct {
		testCase     string
		providerSpec *vsphereapi.VSphereMachineProviderSpec
		expected     func(gotDevices []types.BaseVirtualDeviceConfigSpec) bool
	}{
		{
			testCase:     "no Network",
			providerSpec: &vsphereapi.VSphereMachineProviderSpec{},
			expected: func(gotDevices []types.BaseVirtualDeviceConfigSpec) bool {
				if len(gotDevices) != 1 {
					return false
				}
				if gotDevices[0].GetVirtualDeviceConfigSpec().Operation != types.VirtualDeviceConfigSpecOperationRemove {
					return false
				}
				return true
			},
		},
		{
			testCase: "one Network",
			providerSpec: &vsphereapi.VSphereMachineProviderSpec{
				Network: vsphereapi.NetworkSpec{
					Devices: []vsphereapi.NetworkDeviceSpec{
						{
							NetworkName: "VM Network",
						},
					},
				},
			},
			expected: func(gotDevices []types.BaseVirtualDeviceConfigSpec) bool {
				if len(gotDevices) != 2 {
					return false
				}
				if gotDevices[0].GetVirtualDeviceConfigSpec().Operation != types.VirtualDeviceConfigSpecOperationRemove {
					return false
				}
				if gotDevices[1].GetVirtualDeviceConfigSpec().Operation != types.VirtualDeviceConfigSpecOperationAdd {
					return false
				}
				return true
			},
		},
	}
	// TODO: verify GetVirtualDeviceConfigSpec().Device values

	for _, tc := range testCases {
		t.Run(tc.testCase, func(t *testing.T) {
			machineScope := &machineScope{
				Context:      context.TODO(),
				providerSpec: tc.providerSpec,
				session:      session,
			}
			networkDevices, err := getNetworkDevices(machineScope, devices)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.expected(networkDevices) {
				t.Errorf("Got unexpected networkDevices len (%v) or operations (%v)",
					len(networkDevices),
					printOperations(networkDevices))
			}
		})
	}
}

func printOperations(networkDevices []types.BaseVirtualDeviceConfigSpec) string {
	var output string
	for i := range networkDevices {
		output += fmt.Sprintf("device: %v has operation: %v, ", i, string(networkDevices[i].GetVirtualDeviceConfigSpec().Operation))
	}
	return output
}

func TestGetNetworkStatusList(t *testing.T) {
	model, session, server := initSimulator(t)
	defer model.Remove()
	defer server.Close()

	managedObj := simulator.Map.Any("VirtualMachine").(*simulator.VirtualMachine)
	defaultFakeIPs := []string{"127.0.0.1"}
	managedObj.Guest.Net[0].IpAddress = defaultFakeIPs
	managedObjRef := object.NewVirtualMachine(session.Client.Client, managedObj.Reference()).Reference()

	vm := &virtualMachine{
		Context: context.TODO(),
		Obj:     object.NewVirtualMachine(session.Client.Client, managedObjRef),
		Ref:     managedObjRef,
	}

	defaultFakeMAC := "00:0c:29:33:34:38"
	expectedNetworkStatusList := []NetworkStatus{
		{
			IPAddrs:   defaultFakeIPs,
			Connected: true,
			MACAddr:   defaultFakeMAC,
		},
	}

	// validations
	networkStatusList, err := vm.getNetworkStatusList(session.Client.Client)
	if err != nil {
		t.Fatal(err)
	}

	if len(networkStatusList) != 1 {
		t.Errorf("Expected networkStatusList len to be 1, got %v", len(networkStatusList))
	}
	if !reflect.DeepEqual(networkStatusList, expectedNetworkStatusList) {
		t.Errorf("Expected: %v, got: %v", networkStatusList, expectedNetworkStatusList)
	}
	// TODO: add more cases by adding network devices to the NewVirtualMachine() object
}

func TestReconcileNetwork(t *testing.T) {
	model, session, server := initSimulator(t)
	defer model.Remove()
	defer server.Close()

	managedObj := simulator.Map.Any("VirtualMachine").(*simulator.VirtualMachine)
	managedObj.Guest.Net[0].IpAddress = []string{"127.0.0.1"}
	managedObjRef := object.NewVirtualMachine(session.Client.Client, managedObj.Reference()).Reference()

	vm := &virtualMachine{
		Context: context.TODO(),
		Obj:     object.NewVirtualMachine(session.Client.Client, managedObjRef),
		Ref:     managedObjRef,
	}

	expectedAddresses := []corev1.NodeAddress{
		{
			Type:    corev1.NodeInternalIP,
			Address: "127.0.0.1",
		},
	}
	r := &Reconciler{
		machineScope: &machineScope{
			Context: context.TODO(),
			session: session,
			machine: &machinev1.Machine{
				Status: machinev1.MachineStatus{},
			},
			providerSpec: &vsphereapi.VSphereMachineProviderSpec{
				Network: vsphereapi.NetworkSpec{
					Devices: []vsphereapi.NetworkDeviceSpec{
						{
							NetworkName: "dummy",
						},
					},
				},
			},
		},
	}
	if err := r.reconcileNetwork(vm); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(expectedAddresses, r.machineScope.machine.Status.Addresses) {
		t.Errorf("Expected: %v, got: %v", expectedAddresses, r.machineScope.machine.Status.Addresses)
	}
	// TODO: add more cases by adding network devices to the NewVirtualMachine() object
}

// TODO TestCreate()
// TODO TestUpdate()
// TODO TestExist()
// TODO TestReconcileMachineWithCloudState()
// See for extending behaviour example https://github.com/vmware/govmomi/blob/master/simulator/example_extend_test.go#L33:6
