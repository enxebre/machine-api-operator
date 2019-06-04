package nodelink

import (
	"context"
	"fmt"
	"reflect"

	mapiv1beta1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	machineAnnotationKey = "machine.openshift.io/machine"
)

// Add creates a new Nodelink Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	reconciler := newReconciler(mgr)
	return add(mgr, reconciler, reconciler.nodeRequestFromMachine)
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) *ReconcileNodeLink {
	return &ReconcileNodeLink{
		client:              mgr.GetClient(),
		scheme:              mgr.GetScheme(),
		providerIDToMachine: make(map[string]*mapiv1beta1.Machine),
		providerIDToNode:    make(map[string]*corev1.Node),
	}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler, mapFn handler.ToRequestsFunc) error {
	// Create a new controller
	c, err := controller.New("nodelink-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	//Watch for changes to Node
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch for changes to Machines and enqueue if it exists the backed node
	err = c.Watch(&source.Kind{Type: &mapiv1beta1.Machine{}}, &handler.EnqueueRequestsFromMapFunc{ToRequests: mapFn})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileNodeLink implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileNodeLink{}

// ReconcileNodeLink reconciles a Node object
type ReconcileNodeLink struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client              client.Client
	scheme              *runtime.Scheme
	providerIDToMachine map[string]*mapiv1beta1.Machine
	providerIDToNode    map[string]*corev1.Node
}

// Reconcile reads that state of the cluster for a Node object and makes changes based on the state read
// and what is in the Node.Spec
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileNodeLink) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	klog.Infof("Reconciling Node %v", request)

	// Fetch the Node instance
	node := &corev1.Node{}
	err := r.client.Get(context.TODO(), request.NamespacedName, node)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		klog.Errorf("Error getting node: %v", err)
		return reconcile.Result{}, fmt.Errorf("error getting node: %v", err)
	}

	machine, err := r.findMachineFromNode(*node)
	if err != nil {
		klog.Errorf("Failed to find machine from node %q: %v", node.GetName(), err)
		return reconcile.Result{}, fmt.Errorf("failed to find machine from node %q: %v", node.GetName(), err)
	}

	if machine == nil {
		klog.Warningf("Machine for node %q not found", node.GetName())
		return reconcile.Result{}, nil
	}

	modNode := node.DeepCopy()
	if modNode.Annotations == nil {
		modNode.Annotations = map[string]string{}
	}
	modNode.Annotations[machineAnnotationKey] = fmt.Sprintf("%s/%s", machine.GetNamespace(), machine.GetName())

	if modNode.Labels == nil {
		modNode.Labels = map[string]string{}
	}

	for k, v := range machine.Spec.Labels {
		klog.V(4).Infof("Copying label %s = %s", k, v)
		modNode.Labels[k] = v
	}

	addTaintsToNode(modNode, machine)

	if !reflect.DeepEqual(node, modNode) {
		klog.V(3).Infof("Node %q has changed, updating", modNode.GetName())
		if err := r.client.Update(context.Background(), modNode); err != nil {
			return reconcile.Result{}, fmt.Errorf("error updating node: %v", err)
		}
	}

	// TODO(alberto): bring nodeRef logic here and drop cluster-api node controller
	return reconcile.Result{}, nil
}

// nodeRequestFromMachine returns a reconcile.request for the node backed by the received machine
func (r *ReconcileNodeLink) nodeRequestFromMachine(o handler.MapObject) []reconcile.Request {
	klog.V(3).Infof("Watched machine event, finding node to reconcile.Request")
	// get machine
	machine := &mapiv1beta1.Machine{}
	if err := r.client.Get(
		context.Background(),
		client.ObjectKey{
			Namespace: o.Meta.GetNamespace(),
			Name:      o.Meta.GetName(),
		},
		machine,
	); err != nil {
		klog.Errorf("No-op: Unable to retrieve machine %s/%s from store: %v", o.Meta.GetNamespace(), o.Meta.GetName(), err)
		return []reconcile.Request{}
	}

	// update cache
	if machine.Spec.ProviderID != nil {
		if machine.DeletionTimestamp != nil {
			delete(r.providerIDToMachine, *machine.Spec.ProviderID)
			klog.V(3).Infof("No-op: Machine %q has a deletion timestamp", o.Meta.GetName())
			return []reconcile.Request{}
		}

		klog.V(3).Infof("Storing providerID for machine %q: %q in cache", machine.GetName(), *machine.Spec.ProviderID)
		r.providerIDToMachine[*machine.Spec.ProviderID] = machine
	}

	// find node
	node, err := r.findNodeFromMachine(*machine)
	if err != nil {
		klog.Errorf("No-op: Failed to find node for machine %q: %v", machine.GetName(), err)
		return []reconcile.Request{}
	}
	if node != nil {
		return []reconcile.Request{
			{
				NamespacedName: client.ObjectKey{
					Namespace: node.GetNamespace(),
					Name:      node.GetName(),
				},
			},
		}
	}

	klog.V(3).Infof("No-op: Node for machine %q not found", machine.GetName())
	return []reconcile.Request{}
}

// findNodeFromMachine tries to a node from by providerIP and fallback to find by IP
func (r *ReconcileNodeLink) findNodeFromMachine(machine mapiv1beta1.Machine) (*corev1.Node, error) {
	klog.V(3).Infof("Finding node from machine %q", machine.GetName())
	node, err := r.findNodeFromMachineByProviderID(machine)
	if err != nil {
		return nil, fmt.Errorf("failed to find node from machine %q by ProviderID: %v", machine.GetName(), err)
	}
	if node != nil {
		return node, nil
	}

	node, err = r.findNodeFromMachineByIP(machine)
	if err != nil {
		return nil, fmt.Errorf("failed to find node from machine %q by internal IP: %v", machine.GetName(), err)
	}
	return node, nil
}

func (r *ReconcileNodeLink) findNodeFromMachineByProviderID(machine mapiv1beta1.Machine) (*corev1.Node, error) {
	klog.V(3).Infof("Finding node from machine %q by providerID", machine.GetName())
	if machine.Spec.ProviderID == nil {
		klog.Warningf("Machine %q has no providerID", machine.GetName())
		return nil, nil
	}

	node, ok := r.providerIDToNode[*machine.Spec.ProviderID]
	if ok {
		klog.V(3).Infof("Found node %q for machine %q with providerID %q from cache", node.GetName(), machine.GetName(), node.Spec.ProviderID)
		return node, nil
	}

	nodeList := &corev1.NodeList{}
	if err := r.client.List(
		context.TODO(),
		&client.ListOptions{},
		nodeList,
	); err != nil {
		return nil, fmt.Errorf("failed getting node list: %v", err)
	}

	for i := range nodeList.Items {
		if nodeList.Items[i].Spec.ProviderID == *machine.Spec.ProviderID {
			klog.V(3).Infof("Found node %q for machine %q with providerID %q", nodeList.Items[i].GetName(), machine.GetName(), nodeList.Items[i].Spec.ProviderID)
			r.providerIDToNode[*machine.Spec.ProviderID] = &nodeList.Items[i]
			return r.providerIDToNode[*machine.Spec.ProviderID], nil
		}
	}
	return nil, nil
}

func (r *ReconcileNodeLink) findNodeFromMachineByIP(machine mapiv1beta1.Machine) (*corev1.Node, error) {
	klog.V(3).Infof("Finding node from machine %q by IP", machine.GetName())
	// TODO(alberto) create hash cached for IPs
	nodeList := &corev1.NodeList{}
	if err := r.client.List(
		context.TODO(),
		&client.ListOptions{},
		nodeList,
	); err != nil {
		return nil, fmt.Errorf("failed getting node list: %v", err)
	}

	var machineInternalAddress string
	for _, a := range machine.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			machineInternalAddress = a.Address
			klog.V(3).Infof("Found internal IP for machine %q: %q", machine.GetName(), machineInternalAddress)
			break
		}
	}

	if machineInternalAddress == "" {
		klog.Warningf("not found internal IP for machine %q", machine.GetName())
		return nil, nil
	}

	for _, node := range nodeList.Items {
		for _, a := range node.Status.Addresses {
			// Use the internal IP to look for matches:
			if a.Type == corev1.NodeInternalIP {
				if a.Address == machineInternalAddress {
					klog.V(3).Infof("Found node %q for machine %q with internal IP %q", node.GetName(), machine.GetName(), a.Address)
					return &node, nil
				}
				break
			}
		}
	}

	klog.V(3).Infof("not found node match for machine %q with internal IP %q", machine.GetName(), machineInternalAddress)
	return nil, nil
}

func (r *ReconcileNodeLink) findMachineFromNode(node corev1.Node) (*mapiv1beta1.Machine, error) {
	klog.V(3).Infof("Finding machine from node %q", node.GetName())
	machine, err := r.findMachineFromNodeByProviderID(node)
	if err != nil {
		return nil, fmt.Errorf("failed to find machine from node %q by ProviderID: %v", node.GetName(), err)
	}
	if machine != nil {
		return machine, nil
	}

	machine, err = r.findMachineFromNodeByIP(node)
	if err != nil {
		return nil, fmt.Errorf("failed to find node from machine %q by internal IP: %v", machine.GetName(), err)
	}
	return machine, nil
}

func (r *ReconcileNodeLink) findMachineFromNodeByProviderID(node corev1.Node) (*mapiv1beta1.Machine, error) {
	klog.V(3).Infof("Finding machine from node %q by ProviderID", node.GetName())
	if node.Spec.ProviderID == "" {
		klog.Warningf("Node %q has no providerID", node.GetName())
		return nil, nil
	}

	machine, ok := r.providerIDToMachine[node.Spec.ProviderID]
	if ok {
		klog.V(3).Infof("Found machine %q for node %q with providerID %q from cache", machine.GetName(), node.GetName(), node.Spec.ProviderID)
		return machine, nil
	}

	machineList := &mapiv1beta1.MachineList{}
	if err := r.client.List(
		context.TODO(),
		&client.ListOptions{},
		machineList,
	); err != nil {
		return nil, fmt.Errorf("failed getting node list: %v", err)
	}
	for i := range machineList.Items {
		if machineList.Items[i].Spec.ProviderID == nil {
			continue
		}
		if *machineList.Items[i].Spec.ProviderID == node.Spec.ProviderID {
			klog.V(3).Infof("Found machine %q for node %q with providerID %q", machineList.Items[i].GetName(), node.GetName(), node.Spec.ProviderID)
			r.providerIDToMachine[node.Spec.ProviderID] = &machineList.Items[i]
			return r.providerIDToMachine[node.Spec.ProviderID], nil
		}
	}
	return nil, nil
}

func (r *ReconcileNodeLink) findMachineFromNodeByIP(node corev1.Node) (*mapiv1beta1.Machine, error) {
	klog.V(3).Infof("Finding machine from node %q by IP", node.GetName())
	// TODO create hash cached on mapFn and lookup here
	machineList := &mapiv1beta1.MachineList{}
	if err := r.client.List(
		context.TODO(),
		&client.ListOptions{},
		machineList,
	); err != nil {
		return nil, fmt.Errorf("failed getting node list: %v", err)
	}

	var nodeInternalAddress string
	for _, a := range node.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			nodeInternalAddress = a.Address
			klog.V(3).Infof("Found internal IP for node %q: %q", node.GetName(), nodeInternalAddress)
			break
		}
	}

	if nodeInternalAddress == "" {
		klog.Warningf("not found internal IP for node %q", node.GetName())
		return nil, nil
	}

	for i := range machineList.Items {
		for _, a := range machineList.Items[i].Status.Addresses {
			// Use the internal IP to look for matches:
			if a.Type == corev1.NodeInternalIP {
				if a.Address == nodeInternalAddress {
					klog.V(3).Infof("Found machine %q for node %q with internal IP %q", machineList.Items[i].GetName(), node.GetName(), a.Address)
					return &machineList.Items[i], nil
				}
				break
			}
		}
	}

	klog.V(3).Infof("not found node match for node %q with internal IP %q", node.GetName(), nodeInternalAddress)
	return nil, nil
}

// addTaintsToNode adds taints from machine object to the node object
// Taints are to be an authoritative list on the machine spec per cluster-api comments.
// However, we believe many components can directly taint a node and there is no direct source of truth that should enforce a single writer of taints
func addTaintsToNode(node *corev1.Node, machine *mapiv1beta1.Machine) {
	for _, mTaint := range machine.Spec.Taints {
		klog.V(4).Infof("Adding taint %v from machine %q to node %q", mTaint, machine.GetName(), node.GetName())
		alreadyPresent := false
		for _, nTaint := range node.Spec.Taints {
			if nTaint.Key == mTaint.Key && nTaint.Effect == mTaint.Effect {
				klog.V(4).Infof("Skipping to add machine taint, %v, to the node. Node already has a taint with same key and effect", mTaint)
				alreadyPresent = true
				break
			}
		}
		if !alreadyPresent {
			node.Spec.Taints = append(node.Spec.Taints, mTaint)
		}
	}
}
