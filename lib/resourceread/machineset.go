package resourceread

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	clusterv1alpha "sigs.k8s.io/cluster-api/pkg/apis/cluster/v1alpha1"
)

var (
	clusterAPIScheme = runtime.NewScheme()
	clusterAPICodecs = serializer.NewCodecFactory(clusterAPIScheme)
)

func init() {
	if err := clusterv1alpha.AddToScheme(clusterAPIScheme); err != nil {
		panic(err)
	}
}

// ReadMachineConfigV1OrDie reads MachineConfig object from bytes. Panics on error.
func ReadMachineSetV1alphaOrDie(objBytes []byte) *clusterv1alpha.MachineSet {
	requiredObj, err := runtime.Decode(mcfgCodecs.UniversalDecoder(clusterv1alpha.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*clusterv1alpha.MachineSet)
}
