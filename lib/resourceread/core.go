package resourceread

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	coreScheme = runtime.NewScheme()
	coreCodecs = serializer.NewCodecFactory(coreScheme)
)

func init() {
	if err := corev1.AddToScheme(coreScheme); err != nil {
		panic(err)
	}
}

// ReadConfigMapV1OrDie reads configmap object from bytes. Panics on error.
func ReadConfigMapV1OrDie(objBytes []byte) *corev1.ConfigMap {
	requiredObj, err := runtime.Decode(coreCodecs.UniversalDecoder(corev1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*corev1.ConfigMap)
}

// ReadServiceAccountV1OrDie reads serviceaccount object from bytes. Panics on error.
func ReadServiceAccountV1OrDie(objBytes []byte) *corev1.ServiceAccount {
	requiredObj, err := runtime.Decode(coreCodecs.UniversalDecoder(corev1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*corev1.ServiceAccount)
}

// ReadSecretV1OrDie reads secret object from bytes. Panics on error.
func ReadSecretV1OrDie(objBytes []byte) *corev1.Secret {
	requiredObj, err := runtime.Decode(coreCodecs.UniversalDecoder(corev1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*corev1.Secret)
}

// ReadServiceV1OrDie reads serviceaccount object from bytes. Panics on error.
func ReadServiceV1OrDie(objBytes []byte) *corev1.Service {
	requiredObj, err := runtime.Decode(coreCodecs.UniversalDecoder(corev1.SchemeGroupVersion), objBytes)
	if err != nil {
		panic(err)
	}
	return requiredObj.(*corev1.Service)
}
