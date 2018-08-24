package render

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	// Kind is the TypeMeta.Kind for the OperatorConfig.
	Kind = "MachineAPIOperatorConfig"
	// APIVersion is the TypeMeta.APIVersion for the OperatorConfig.
	APIVersion = "v1"
)

// OperatorConfig contains configuration for KAO managed add-ons
type OperatorConfig struct {
	metav1.TypeMeta `json:",inline"`
	LibvirtURI      string `json:"libvirtURI"`
}
