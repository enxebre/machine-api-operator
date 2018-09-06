package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"time"

	"github.com/golang/glog"
	"k8s.io/client-go/kubernetes/scheme"

	"strconv"

	opclient "github.com/coreos-inc/tectonic-operators/operator-client/pkg/client"
	optypes "github.com/coreos-inc/tectonic-operators/operator-client/pkg/types"
	xotypes "github.com/coreos-inc/tectonic-operators/x-operator/pkg/types"
	"github.com/coreos-inc/tectonic-operators/x-operator/pkg/xoperator"
	"github.com/openshift/machine-api-operator/pkg/render"
	machineAPI "github.com/openshift/machine-api-operator/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clusterv1 "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
	"sigs.k8s.io/cluster-api/pkg/client/clientset_generated/clientset"
	clusterApiScheme "sigs.k8s.io/cluster-api/pkg/client/clientset_generated/clientset/scheme"
)

var (
	kubeconfig  string
	manifestDir string
	configPath  string
)

func init() {
	flag.Set("logtostderr", "true")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Kubeconfig file to access a remote cluster. Warning: For testing only, do not use in production.")
	flag.StringVar(&manifestDir, "manifest-dir", "/manifests", "Path to dir with manifest templates.")
	flag.StringVar(&configPath, "config", "/etc/mao-config/config", "Cluster config file from which to obtain configuration options")
	flag.Parse()
}

const (
	providerAWS     = "aws"
	providerLibvirt = "libvirt"
)

func main() {

	// Hack to deploy cluster and machineSet objects
	// TODO: manage the machineSet object by the operator
	go deployMachineSet()

	// TODO: drop x-operator library
	// Integrate with https://github.com/openshift/cluster-version-operator when is ready
	// Consider reuse https://github.com/openshift/library-go/tree/master/pkg/operator/resource
	if err := xoperator.Run(xoperator.Config{
		Client: opclient.NewClient(kubeconfig),
		LeaderElectionConfig: xoperator.LeaderElectionConfig{
			Kubeconfig: kubeconfig,
			Namespace:  optypes.TectonicNamespace,
		},
		OperatorName:   machineAPI.MachineAPIOperatorName,
		AppVersionName: machineAPI.MachineAPIVersionName,
		Renderer:       rendererFromFile,
	}); err != nil {
		glog.Fatalf("Failed to run machine-api operator: %v", err)
	}
}

// rendererFromFile reads the config object on demand from the path and then passes it to the
// renderer.
func rendererFromFile() []xotypes.UpgradeSpec {
	config, err := render.Config(configPath)
	if err != nil {
		glog.Exitf("Error reading machine-api config: %v", err)
	}
	return render.MakeRenderer(config, manifestDir)()
}

func getConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		glog.V(4).Infof("Loading kube client config from path %q", kubeconfig)
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		glog.V(4).Infof("Using in-cluster kube client config")
		return rest.InClusterConfig()
	}
}

func deployMachineSet() {
	//Cluster API client
	config, err := getConfig(kubeconfig)
	if err != nil {
		glog.Fatalf("Error building kube config %#v", err)
	}
	client, err := clientset.NewForConfig(config)
	clusterApiScheme.AddToScheme(scheme.Scheme)
	decode := scheme.Codecs.UniversalDeserializer().Decode
	v1alphaClient := client.ClusterV1alpha1()

	operatorConfig, err := render.Config(configPath)
	if err != nil {
		glog.Fatalf("Error reading machine-api-operator config: %v", err)
	}

	var machinesFolder string
	var replicas int
	if operatorConfig.Provider == providerAWS {
		machinesFolder = "machines/aws"
		if replicas, err = strconv.Atoi(operatorConfig.AWS.Replicas); err != nil {
			glog.Fatalf("Error getting replicas: %v", err)
		}
	} else if operatorConfig.Provider == providerLibvirt {
		machinesFolder = "machines/libvirt"
		if replicas, err = strconv.Atoi(operatorConfig.Libvirt.Replicas); err != nil {
			glog.Fatalf("Error getting replicas: %v", err)
		}
	}

	// Create Cluster object
	clusterTemplateData, err := ioutil.ReadFile(fmt.Sprintf("%s/cluster.yaml", machinesFolder)) // just pass the file name
	if err != nil {
		glog.Fatalf("Error reading %#v", err)
	}

	clusterPopulatedData, err := render.Manifests(operatorConfig, clusterTemplateData)
	if err != nil {
		glog.Fatalf("Unable to render manifests %q: %v", clusterTemplateData, err)
	}

	clusterObj, _, err := decode([]byte(clusterPopulatedData), nil, nil)
	if err != nil {
		glog.Fatalf("Error decoding %#v", err)
	}
	cluster := clusterObj.(*clusterv1.Cluster)

	// Create MachineSet object
	machineSetTemplateData, err := ioutil.ReadFile(fmt.Sprintf("%s/machine-set.yaml", machinesFolder)) // just pass the file name
	if err != nil {
		glog.Fatalf("Error reading %#v", err)
	}

	machineSetPopulatedData, err := render.Manifests(operatorConfig, machineSetTemplateData)
	if err != nil {
		glog.Fatalf("Unable to render manifests %q: %v", machineSetTemplateData, err)
	}

	machineSetObj, _, err := decode([]byte(machineSetPopulatedData), nil, nil)
	if err != nil {
		glog.Fatalf("Error decoding %#v", err)
	}
	machineSet := machineSetObj.(*clusterv1.MachineSet)

	// Create Machine objects
	machineData, err := ioutil.ReadFile(fmt.Sprintf("%s/machine.yaml", machinesFolder)) // just pass the file name
	if err != nil {
		glog.Fatalf("Error reading %#v", err)
	}

	for {
		glog.Info("Trying to deploy Cluster object")
		if _, err := v1alphaClient.Clusters("default").Create(cluster); err != nil {
			glog.Infof("Cannot create cluster, retrying in 5 secs: %v", err)
		} else {
			glog.Info("Created Cluster object")
			glog.Info("Trying to deploy Machine objects for adoption")
			for i := 0; i < replicas; i++ {
				operatorConfig.Index = i
				machinePopulatedData, err := render.Manifests(operatorConfig, machineData)
				if err != nil {
					glog.Fatalf("Unable to render manifests %q: %v", machinePopulatedData, err)
				}

				machineObj, _, err := decode([]byte(machinePopulatedData), nil, nil)
				if err != nil {
					glog.Fatalf("Error decoding %#v", err)
				}
				machine := machineObj.(*clusterv1.Machine)

				_, err = v1alphaClient.Machines("default").Create(machine)
				if err != nil {
					glog.Infof("Cannot create Machine, retrying in 5 secs: %v", err)
				} else {
					glog.Infof("Created Machine object %d successfully", i)
				}
			}

			glog.Info("Trying to deploy MachineSet object")
			_, err = v1alphaClient.MachineSets("default").Create(machineSet)
			if err != nil {
				glog.Infof("Cannot create MachineSet, retrying in 5 secs: %v", err)
			} else {
				glog.Info("Created MachineSet object Successfully")
				return
			}

		}
		time.Sleep(5 * time.Second)
	}
}
