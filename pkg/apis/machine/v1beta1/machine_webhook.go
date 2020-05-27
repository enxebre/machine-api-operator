package v1beta1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	osconfigv1 "github.com/openshift/api/config/v1"
	osclientset "github.com/openshift/client-go/config/clientset/versioned"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/klog"
	"k8s.io/utils/pointer"
	aws "sigs.k8s.io/cluster-api-provider-aws/pkg/apis/awsprovider/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	yaml "sigs.k8s.io/yaml"
)

func getClusterID() (string, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return "", err
	}
	client, err := osclientset.NewForConfig(cfg)
	if err != nil {
		return "", err
	}
	infra, err := client.ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return infra.Status.InfrastructureName, nil
}

func getPlatform() (osconfigv1.PlatformType, error) {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return "", err
	}
	client, err := osclientset.NewForConfig(cfg)
	if err != nil {
		return "", err
	}
	infra, err := client.ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return infra.Status.PlatformStatus.Type, nil
}

type handlerValidationFn func(h *validatorHandler, m *Machine) (bool, utilerrors.Aggregate)
type handlerMutationFn func(h *defaulterHandler, m *Machine) (bool, utilerrors.Aggregate)

// validatorHandler validates Machine resources.
// implements type Handler interface
type validatorHandler struct {
	clusterID         string
	webhookOperations handlerValidationFn
	decoder           *admission.Decoder
}

// defaulterHandler defaults Machine resources.
// implements type Handler interface
type defaulterHandler struct {
	clusterID         string
	webhookOperations handlerMutationFn
	decoder           *admission.Decoder
}

// NewValidator returns a new Validator.
func NewValidator() (*validatorHandler, error) {
	clusterID, err := getClusterID()
	if err != nil {
		return nil, err
	}

	h := &validatorHandler{
		clusterID: clusterID,
	}

	platform, err := getPlatform()
	if err != nil {
		return nil, err
	}

	switch platform {
	case osconfigv1.AWSPlatformType:
		h.webhookOperations = validateAWS
	default:
		// just no-op
		h.webhookOperations = func(h *validatorHandler, m *Machine) (bool, utilerrors.Aggregate) {
			return true, nil
		}
	}
	return h, nil
}

// NewDefaulter returns a new Validator.
func NewDefaulter() (*defaulterHandler, error) {
	clusterID, err := getClusterID()
	if err != nil {
		return nil, err
	}

	h := &defaulterHandler{
		clusterID: clusterID,
	}

	platform, err := getPlatform()
	if err != nil {
		return nil, err
	}

	switch platform {
	case osconfigv1.AWSPlatformType:
		h.webhookOperations = defaultAWS
	default:
		// just no-op
		h.webhookOperations = func(h *defaulterHandler, m *Machine) (bool, utilerrors.Aggregate) {
			return true, nil
		}
	}
	return h, nil
}

// InjectDecoder injects the decoder.
func (v *validatorHandler) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}

// InjectDecoder injects the decoder.
func (v *defaulterHandler) InjectDecoder(d *admission.Decoder) error {
	v.decoder = d
	return nil
}

// Handle handles HTTP requests for admission webhook servers.
func (h *validatorHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	m := &Machine{}

	if err := h.decoder.Decode(req, m); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	klog.Infof("Mutation webhook called for Machine: %s", m.GetName())

	if ok, err := h.webhookOperations(h, m); !ok {
		return admission.Denied(err.Error())
	}

	return admission.Allowed("Machine valid")
}

// Handle handles HTTP requests for admission webhook servers.
func (h *defaulterHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	m := &Machine{}

	if err := h.decoder.Decode(req, m); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	klog.Infof("Mutation webhook called for Machine: %s", m.GetName())

	if ok, err := h.webhookOperations(h, m); !ok {
		return admission.Denied(err.Error())
	}

	marshaledMachine, err := json.Marshal(m)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledMachine)
}

// +kubebuilder:webhook:verbs=create,path=/mutate-machine-openshift-io-v1beta1-machine,mutating=true,failurePolicy=fail,matchPolicy=Equivalent,groups=machine.openshift.io,resources=machines,versions=v1beta1,name=default.machine.machine.openshift.io,sideEffects=None
// +kubebuilder:webhook:verbs=create;update,path=/validate-machine-openshift-io-v1beta1-machine,mutating=false,failurePolicy=fail,matchPolicy=Equivalent,groups=machine.openshift.io,resources=machines,versions=v1beta1,name=validate.machine.machine.openshift.io,sideEffects=None

func defaultAWS(h *defaulterHandler, m *Machine) (bool, utilerrors.Aggregate) {
	klog.Infof("Defaulting aws providerSpec")

	var errs []error
	providerSpec := new(aws.AWSMachineProviderConfig)
	if err := yaml.Unmarshal(m.Spec.ProviderSpec.Value.Raw, &providerSpec); err != nil {
		errs = append(errs, err)
	}

	if m.GetLabels() == nil {
		m.Labels = make(map[string]string)
	}
	m.Labels["machine.openshift.io/cluster-api-cluster"] = h.clusterID

	if providerSpec.InstanceType == "" {
		providerSpec.InstanceType = "m4.large"
	}
	if providerSpec.IAMInstanceProfile == nil {
		providerSpec.IAMInstanceProfile = &aws.AWSResourceReference{ID: pointer.StringPtr(fmt.Sprintf("%s-%s-profile", h.clusterID, "worker"))}
	}
	if providerSpec.UserDataSecret == nil {
		providerSpec.UserDataSecret = &corev1.LocalObjectReference{Name: "worker-user-data"}
	}

	if providerSpec.CredentialsSecret == nil {
		providerSpec.CredentialsSecret = &corev1.LocalObjectReference{Name: "aws-cloud-credentials"}
	}

	if providerSpec.SecurityGroups == nil {
		providerSpec.SecurityGroups = []aws.AWSResourceReference{
			{
				Filters: []aws.Filter{
					{
						Name:   "tag:Name",
						Values: []string{fmt.Sprintf("%s-%s-sg", h.clusterID, "worker")},
					},
				},
			},
		}
	}

	if providerSpec.Subnet.ARN == nil && providerSpec.Subnet.ID == nil && providerSpec.Subnet.Filters == nil {
		providerSpec.Subnet.Filters = []aws.Filter{
			{
				Name:   "tag:Name",
				Values: []string{fmt.Sprintf("%s-private-%s", h.clusterID, providerSpec.Placement.AvailabilityZone)},
			},
		}
	}

	if len(errs) > 0 {
		return false, utilerrors.NewAggregate(errs)
	}

	m.Spec.ProviderSpec.Value = &runtime.RawExtension{Object: providerSpec}
	return true, nil
}

func validateAWS(h *validatorHandler, m *Machine) (bool, utilerrors.Aggregate) {
	klog.Infof("Validating aws providerSpec")

	var errs []error
	providerSpec := new(aws.AWSMachineProviderConfig)
	if err := yaml.Unmarshal(m.Spec.ProviderSpec.Value.Raw, &providerSpec); err != nil {
		errs = append(errs, err)
	}

	if providerSpec.AMI.ARN == nil && providerSpec.AMI.Filters == nil && providerSpec.AMI.ID == nil {
		errs = append(
			errs,
			field.Required(
				field.NewPath("providerSpec", "AMI"),
				"expected either providerSpec.AMI.ARN or providerSpec.AMI.Filters or providerSpec.AMI.ID to be populated",
			),
		)
	}

	if providerSpec.InstanceType == "" {
		errs = append(
			errs,
			field.Required(
				field.NewPath("providerSpec", "InstanceType"),
				"expected providerSpec.InstanceType to be populated",
			),
		)
	}
	if providerSpec.IAMInstanceProfile == nil {
		errs = append(
			errs,
			field.Required(
				field.NewPath("providerSpec", "IAMInstanceProfile"),
				"expected providerSpec.IAMInstanceProfile to be populated",
			),
		)
	}
	if providerSpec.UserDataSecret == nil {
		errs = append(
			errs,
			field.Required(
				field.NewPath("providerSpec", "UserDataSecret"),
				"expected providerSpec.UserDataSecret to be populated",
			),
		)
	}

	if providerSpec.CredentialsSecret == nil {
		errs = append(
			errs,
			field.Required(
				field.NewPath("providerSpec", "CredentialsSecret"),
				"expected providerSpec.CredentialsSecret to be populated",
			),
		)
	}

	if providerSpec.SecurityGroups == nil {
		errs = append(
			errs,
			field.Required(
				field.NewPath("providerSpec", "SecurityGroups"),
				"expected providerSpec.SecurityGroups to be populated",
			),
		)
	}

	if providerSpec.Subnet.ARN == nil && providerSpec.Subnet.ID == nil && providerSpec.Subnet.Filters == nil && providerSpec.Placement.AvailabilityZone == "" {
		errs = append(
			errs,
			field.Required(
				field.NewPath("providerSpec", "Subnet"),
				"expected either providerSpec.Subnet.ARN or providerSpec.Subnet.ID or providerSpec.Subnet.Filters or providerSpec.Placement.AvailabilityZone to be populated",
			),
		)
	}

	if len(errs) > 0 {
		return false, utilerrors.NewAggregate(errs)
	}

	return true, nil
}
