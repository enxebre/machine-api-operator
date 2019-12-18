package vsphere

import (
	"context"
	"fmt"
	"strings"

	machinev1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"
	vsphereapi "github.com/openshift/machine-api-operator/pkg/apis/vsphereprovider/v1alpha1"
	machinecontroller "github.com/openshift/machine-api-operator/pkg/controller/machine"
	"github.com/pkg/errors"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog"
)

const (
	minMemMB     = 2048
	minCPU       = 2
	diskMoveType = string(types.VirtualMachineRelocateDiskMoveOptionsMoveAllDiskBackingsAndConsolidate)
	ethCardType  = "vmxnet3"
)

// Reconciler runs the logic to reconciles a machine resource towards its desired state
type Reconciler struct {
	*machineScope
}

func newReconciler(scope *machineScope) *Reconciler {
	return &Reconciler{
		machineScope: scope,
	}
}

// create creates machine if it does not exists.
func (r *Reconciler) create() error {
	if err := validateMachine(*r.machine, *r.providerSpec); err != nil {
		return fmt.Errorf("%v: failed validating machine provider spec: %v", r.machine.GetName(), err)
	}

	taskRef, err := r.session.GetTask(r.Context, r.providerStatus.TaskRef)
	if err != nil {
		if !isRetrieveMONotFound(r.providerStatus.TaskRef, err) {
			return err
		}
	}
	if taskIsFinished, err := taskIsFinished(taskRef); err != nil || !taskIsFinished {
		if !taskIsFinished {
			return fmt.Errorf("task %v has not finished", taskRef.Reference().Value)
		}
		return err
	}

	if _, err := findVM(r.machineScope); err != nil {
		if !IsNotFound(err) {
			return err
		}
		if r.machineScope.session.IsVC() {
			klog.Infof("%v: cloning", r.machine.GetName())
			return clone(r.machineScope)
		}
		return fmt.Errorf("%v: not connected to a vCenter", r.machine.GetName())
	}

	return nil
}

// update finds a vm and reconciles the machine resource status against it.
func (r *Reconciler) update() error {
	if err := validateMachine(*r.machine, *r.providerSpec); err != nil {
		return fmt.Errorf("%v: failed validating machine provider spec: %v", r.machine.GetName(), err)
	}

	taskRef, err := r.session.GetTask(r.Context, r.providerStatus.TaskRef)
	if err != nil {
		if !isRetrieveMONotFound(r.providerStatus.TaskRef, err) {
			return err
		}
	}
	if taskIsFinished, err := taskIsFinished(taskRef); err != nil || !taskIsFinished {
		if !taskIsFinished {
			return fmt.Errorf("task %v has not finished", taskRef.Value)
		}
		return err
	}

	vmRef, err := findVM(r.machineScope)
	if err != nil {
		if !IsNotFound(err) {
			return err
		}
		if r.machineScope.session.IsVC() {
			return errors.Wrap(err, "catastrophic, machine not found on update")
		}
	}

	vm := &virtualMachine{
		Context: r.machineScope.Context,
		Obj:     object.NewVirtualMachine(r.machineScope.session.Client.Client, vmRef),
		Ref:     vmRef,
	}

	// TODO: we won't always want to reconcile power state
	//  but as per comment in clone() function, powering on right on creation might be problematic
	if ok, err := vm.reconcilePowerState(); err != nil || !ok {
		return err
	}
	return r.reconcileMachineWithCloudState(vm)
}

// exists returns true if machine exists.
func (r *Reconciler) exists() (bool, error) {
	if err := validateMachine(*r.machine, *r.providerSpec); err != nil {
		return false, fmt.Errorf("%v: failed validating machine provider spec: %v", r.machine.GetName(), err)
	}

	if _, err := findVM(r.machineScope); err != nil {
		if !IsNotFound(err) {
			return false, err
		}
		klog.Infof("%v: does not exist", r.machine.GetName())
		return false, nil
	}
	klog.Infof("%v: already exists", r.machine.GetName())
	return true, nil
}

func (r *Reconciler) delete() error {
	// TODO: implement
	return nil
}

// reconcileMachineWithCloudState reconcile machineSpec and status with the latest cloud state
func (r *Reconciler) reconcileMachineWithCloudState(vm *virtualMachine) error {
	klog.V(3).Infof("%v: reconciling machine with cloud state", r.machine.GetName())
	// TODO: reconcile providerID
	// TODO: reconcile task
	klog.V(3).Infof("%v: reconciling network", r.machine.GetName())
	return r.reconcileNetwork(vm)
}

func (r *Reconciler) reconcileNetwork(vm *virtualMachine) error {
	currentNetwork, err := vm.getNetworkStatus(r.session.Client.Client)
	if err != nil {
		return fmt.Errorf("error getting network status: %v", err)
	}

	//If the VM is powered on then issue requeues until all of the VM's
	//networks have IP addresses.
	expectNetworkLen, currentNetworkLen := len(r.providerSpec.Network.Devices), len(currentNetwork)
	if expectNetworkLen != currentNetworkLen {
		return errors.Errorf("invalid network count: expected=%d current=%d", expectNetworkLen, currentNetworkLen)
	}

	var ipAddrs []corev1.NodeAddress
	for _, netStatus := range currentNetwork {
		for _, ip := range netStatus.IPAddrs {
			ipAddrs = append(ipAddrs, corev1.NodeAddress{
				Type:    corev1.NodeInternalIP,
				Address: ip,
			})
		}
	}

	klog.V(3).Infof("%v: reconciling network: IP addresses: %v", r.machine.GetName(), ipAddrs)
	r.machine.Status.Addresses = ipAddrs
	return nil
}

func validateMachine(machine machinev1.Machine, providerSpec vsphereapi.VSphereMachineProviderSpec) error {
	if machine.Labels[machinev1.MachineClusterIDLabel] == "" {
		return machinecontroller.InvalidMachineConfiguration("%v: missing %q label", machine.GetName(), machinev1.MachineClusterIDLabel)
	}

	return nil
}

func findVM(s *machineScope) (types.ManagedObjectReference, error) {
	uuid := string(s.machine.UID)
	objRef, err := s.GetSession().FindRefByInstanceUUID(s, uuid)
	if err != nil {
		return types.ManagedObjectReference{}, err
	}
	if objRef == nil {
		return types.ManagedObjectReference{}, errNotFound{instanceUUID: true, uuid: uuid}
	}
	return objRef.Reference(), nil
}

// errNotFound is returned by the findVM function when a VM is not found.
type errNotFound struct {
	instanceUUID bool
	uuid         string
}

func (e errNotFound) Error() string {
	if e.instanceUUID {
		return fmt.Sprintf("vm with instance uuid %s not found", e.uuid)
	}
	return fmt.Sprintf("vm with bios uuid %s not found", e.uuid)
}

func IsNotFound(err error) bool {
	switch err.(type) {
	case errNotFound, *errNotFound:
		return true
	default:
		return false
	}
}

func isRetrieveMONotFound(taskRef string, err error) bool {
	return err.Error() == fmt.Sprintf("ServerFaultCode: The object 'vim.Task:%v' has already been deleted or has not been completely created", taskRef)
}

func clone(s *machineScope) error {
	vmTemplate, err := s.GetSession().FindVM(*s, s.providerSpec.Template)
	if err != nil {
		return err
	}

	var folderPath, datastorePath, resourcepoolPath string
	if s.providerSpec.Workspace != nil {
		folderPath = s.providerSpec.Workspace.Folder
		datastorePath = s.providerSpec.Workspace.Datastore
		resourcepoolPath = s.providerSpec.Workspace.ResourcePool
	}

	folder, err := s.GetSession().Finder.FolderOrDefault(s, folderPath)
	if err != nil {
		return errors.Wrapf(err, "unable to get folder for %q", folderPath)
	}

	datastore, err := s.GetSession().Finder.DatastoreOrDefault(s, datastorePath)
	if err != nil {
		return errors.Wrapf(err, "unable to get datastore for %q", datastorePath)
	}

	resourcepool, err := s.GetSession().Finder.ResourcePoolOrDefault(s, resourcepoolPath)
	if err != nil {
		return errors.Wrapf(err, "unable to get resource pool for %q", resourcepool)
	}

	numCPUs := s.providerSpec.NumCPUs
	if numCPUs < minCPU {
		numCPUs = minCPU
	}
	numCoresPerSocket := s.providerSpec.NumCoresPerSocket
	if numCoresPerSocket == 0 {
		numCoresPerSocket = numCPUs
	}
	memMiB := s.providerSpec.MemoryMiB
	if memMiB == 0 {
		memMiB = minMemMB
	}

	devices, err := vmTemplate.Device(s.Context)
	if err != nil {
		return fmt.Errorf("error getting devices %v", err)
	}

	klog.V(3).Infof("Getting network devices")
	networkSpecs, err := getNetworkSpecs(s, devices)
	if err != nil {
		return fmt.Errorf("error getting network specs: %v", err)
	}

	var deviceSpecs []types.BaseVirtualDeviceConfigSpec
	deviceSpecs = append(deviceSpecs, networkSpecs...)

	spec := types.VirtualMachineCloneSpec{
		Config: &types.VirtualMachineConfigSpec{
			Annotation: s.machine.GetName(),
			// Assign the clone's InstanceUUID the value of the Kubernetes Machine
			// object's UID. This allows lookup of the cloned VM prior to knowing
			// the VM's UUID.
			InstanceUuid: string(s.machine.UID),
			Flags:        newVMFlagInfo(),
			// TODO: set userData
			//ExtraConfig:       extraConfig,
			// TODO: set devices
			DeviceChange:      deviceSpecs,
			NumCPUs:           numCPUs,
			NumCoresPerSocket: numCoresPerSocket,
			MemoryMB:          memMiB,
		},
		Location: types.VirtualMachineRelocateSpec{
			Datastore:    types.NewReference(datastore.Reference()),
			DiskMoveType: diskMoveType,
			Folder:       types.NewReference(folder.Reference()),
			Pool:         types.NewReference(resourcepool.Reference()),
		},
		// This is implicit, but making it explicit as it is important to not
		// power the VM on before its virtual hardware is created and the MAC
		// address(es) used to build and inject the VM with cloud-init metadata
		// are generated.
		PowerOn: false,
	}

	task, err := vmTemplate.Clone(s, folder, s.machine.GetName(), spec)
	if err != nil {
		return errors.Wrapf(err, "error triggering clone op for machine %v", s)
	}

	s.providerStatus.TaskRef = task.Reference().Value
	klog.V(3).Infof("%v: running task: %+v", s.machine.GetName(), s.providerStatus.TaskRef)
	return nil
}

func getNetworkSpecs(s *machineScope, devices object.VirtualDeviceList) ([]types.BaseVirtualDeviceConfigSpec, error) {
	var deviceSpecs []types.BaseVirtualDeviceConfigSpec
	// Remove any existing NICs
	for _, dev := range devices.SelectByType((*types.VirtualEthernetCard)(nil)) {
		deviceSpecs = append(deviceSpecs, &types.VirtualDeviceConfigSpec{
			Device:    dev,
			Operation: types.VirtualDeviceConfigSpecOperationRemove,
		})
	}

	// Add new NICs based on the machine config.
	key := int32(-100)
	for i := range s.providerSpec.Network.Devices {
		netSpec := &s.providerSpec.Network.Devices[i]
		klog.V(3).Infof("Setting device: %v", netSpec.NetworkName)

		ref, err := s.GetSession().Finder.Network(s.Context, netSpec.NetworkName)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to find network %q", netSpec.NetworkName)
		}

		backing, err := ref.EthernetCardBackingInfo(s.Context)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to create new ethernet card backing info for network %q", netSpec.NetworkName)
		}

		dev, err := object.EthernetCardTypes().CreateEthernetCard(ethCardType, backing)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to create new ethernet card %q for network %q", ethCardType, netSpec.NetworkName)
		}

		// Get the actual NIC object. This is safe to assert without a check
		// because "object.EthernetCardTypes().CreateEthernetCard" returns a
		// "types.BaseVirtualEthernetCard" as a "types.BaseVirtualDevice".
		nic := dev.(types.BaseVirtualEthernetCard).GetVirtualEthernetCard()
		// Assign a temporary device key to ensure that a unique one will be
		// generated when the device is created.
		nic.Key = key

		deviceSpecs = append(deviceSpecs, &types.VirtualDeviceConfigSpec{
			Device:    dev,
			Operation: types.VirtualDeviceConfigSpecOperationAdd,
		})
		klog.V(3).Info("created network device, eth-card-type: %v, network-spec: %v", ethCardType, netSpec)
		key--
	}

	return deviceSpecs, nil
}

func newVMFlagInfo() *types.VirtualMachineFlagInfo {
	diskUUIDEnabled := true
	return &types.VirtualMachineFlagInfo{
		DiskUuidEnabled: &diskUUIDEnabled,
	}
}

func taskIsFinished(task *mo.Task) (bool, error) {
	if task == nil {
		return true, nil
	}

	// Otherwise the course of action is determined by the state of the task.
	klog.V(3).Infof("task: %v, state: %v, description-id: %v", task.Reference().Value, task.Info.State, task.Info.DescriptionId)
	switch task.Info.State {
	case types.TaskInfoStateQueued:
		klog.V(3).Info("task is still pending")
		return false, nil
	case types.TaskInfoStateRunning:
		klog.V(3).Info("task is still running")
		return false, nil
	case types.TaskInfoStateSuccess:
		klog.V(3).Info("task has succeeded")
		return true, nil
	case types.TaskInfoStateError:
		klog.V(3).Info("task has failed")
		return true, nil
	default:
		return false, errors.Errorf("task: %v, unknown state %v", task.Reference().Value, task.Info.State)
	}
}

type virtualMachine struct {
	context.Context
	Ref types.ManagedObjectReference
	Obj *object.VirtualMachine
}

func (vm *virtualMachine) reconcilePowerState() (bool, error) {
	powerState, err := vm.getPowerState()
	if err != nil {
		return false, err
	}
	switch powerState {
	case types.VirtualMachinePowerStatePoweredOff:
		klog.Infof("%v: powering on", vm.Obj.Reference().Value)
		_, err := vm.powerOnVM()
		if err != nil {
			return false, errors.Wrapf(err, "failed to trigger power on op for vm %q", vm)
		}
		// TODO: store task in providerStatus/conditions?
		klog.Infof("%v: requeue to wait for power on state", vm.Obj.Reference().Value)
		return false, nil
	case types.VirtualMachinePowerStatePoweredOn:
		klog.Infof("%v: powered on", vm.Obj.Reference().Value)
	default:
		return false, errors.Errorf("unexpected power state %q for vm %q", powerState, vm)
	}

	return true, nil
}

func (vm *virtualMachine) powerOnVM() (string, error) {
	task, err := vm.Obj.PowerOn(vm.Context)
	if err != nil {
		return "", err
	}
	return task.Reference().Value, nil
}

func (vm *virtualMachine) powerOffVM() (string, error) {
	task, err := vm.Obj.PowerOff(vm.Context)
	if err != nil {
		return "", err
	}
	return task.Reference().Value, nil
}

func (vm *virtualMachine) getPowerState() (types.VirtualMachinePowerState, error) {
	powerState, err := vm.Obj.PowerState(vm.Context)
	if err != nil {
		return "", err
	}

	switch powerState {
	case types.VirtualMachinePowerStatePoweredOn:
		return types.VirtualMachinePowerStatePoweredOn, nil
	case types.VirtualMachinePowerStatePoweredOff:
		return types.VirtualMachinePowerStatePoweredOff, nil
	case types.VirtualMachinePowerStateSuspended:
		return types.VirtualMachinePowerStateSuspended, nil
	default:
		return "", errors.Errorf("unexpected power state %q for vm %v", powerState, vm)
	}
}

type NetworkStatus struct {
	// Connected is a flag that indicates whether this network is currently
	// connected to the VM.
	Connected bool

	// IPAddrs is one or more IP addresses reported by vm-tools.
	IPAddrs []string

	// MACAddr is the MAC address of the network device.
	MACAddr string

	// NetworkName is the name of the network.
	NetworkName string
}

func (vm *virtualMachine) getNetworkStatus(client *vim25.Client) ([]NetworkStatus, error) {
	var obj mo.VirtualMachine
	var pc = property.DefaultCollector(client)
	var props = []string{
		"config.hardware.device",
		"guest.net",
	}

	if err := pc.RetrieveOne(vm.Context, vm.Ref, props, &obj); err != nil {
		return nil, errors.Wrapf(err, "unable to fetch props %v for vm %v", props, vm.Ref)
	}
	klog.V(3).Infof("Getting network status: object reference: %v", obj.Reference().Value)
	if obj.Config == nil {
		return nil, errors.New("config.hardware.device is nil")
	}

	var netStatusList []NetworkStatus
	for _, device := range obj.Config.Hardware.Device {
		if dev, ok := device.(types.BaseVirtualEthernetCard); ok {
			nic := dev.GetVirtualEthernetCard()
			klog.V(3).Infof("Getting network status: device: %v, macAddress: %v", nic.DeviceInfo.GetDescription().Summary, nic.MacAddress)
			netStatus := NetworkStatus{
				MACAddr: nic.MacAddress,
			}
			if obj.Guest != nil {
				klog.V(3).Infof("Getting network status: getting guest info")
				for _, i := range obj.Guest.Net {
					klog.V(3).Infof("Getting network status: getting guest info: network: %+v", i)
					if strings.EqualFold(nic.MacAddress, i.MacAddress) {
						//TODO: sanitizeIPAddrs
						netStatus.IPAddrs = i.IpAddress
						netStatus.NetworkName = i.Network
						netStatus.Connected = i.Connected
					}
				}
			}
			netStatusList = append(netStatusList, netStatus)
		}
	}

	return netStatusList, nil
}
