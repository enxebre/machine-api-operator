package lastapplied

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	annotationLastApplied     = "tectonic-operators.coreos.com/last-applied"
	annotationLastAppliedHash = "tectonic-operators.coreos.com/last-applied-hash"
)

var (
	// ErrorLastAppliedAnnotationMissing is returned when the manifest is missing the
	// `annotationLastApplied` annotation.
	ErrorLastAppliedAnnotationMissing = errors.New("manifest is missing annotation " + annotationLastApplied)
	// ErrorLastAppliedHashAnnotationMissing is returned when the manifest is missing the
	// `annotationLastAppliedHash` annotation.
	ErrorLastAppliedHashAnnotationMissing = errors.New("manifest is missing annotation " + annotationLastAppliedHash)
	// ErrorLastAppliedInvalidHash is returned when the hash stored in the `annotationLastAppliedHash`
	// annotation does not match the manifest stored in the `annotationLastApplied` annotation.
	ErrorLastAppliedInvalidHash = errors.New("manifest hash does not match")
	// salt is 80 random bytes to prevent users from trivially recreating the hash. It is not meant to
	// be secure; it only slightly improves the reliability of the validation check.
	// The salt was generated by `od -vAn -N80 -tu1 < /dev/urandom`. Do not change it.
	salt = []byte{
		193, 50, 64, 152, 164, 96, 74, 14, 79, 75, 224, 68, 250, 204, 170, 211,
		195, 55, 43, 86, 137, 42, 220, 57, 176, 144, 99, 43, 189, 109, 222, 104,
		166, 16, 144, 45, 88, 165, 235, 93, 235, 245, 65, 161, 128, 126, 29, 251,
		246, 89, 92, 193, 124, 180, 204, 24, 23, 94, 97, 63, 25, 137, 255, 60,
		169, 247, 9, 120, 209, 86, 25, 112, 232, 235, 48, 98, 249, 56, 238, 195,
	}
)

// Set sets the value of the `annotationLastApplied` to the manifest that is passed in as the second
// argument. It also sets the hash in `annotationLastAppliedHash`.
//
// Note that both of these annotations are cleared before they are stored in last applied to prevent
// combinatorial explosions.
func Set(objToSet, objToMarshal metav1.Object) error {
	annotationsToSet := objToSet.GetAnnotations()
	if annotationsToMarshal := objToMarshal.GetAnnotations(); annotationsToMarshal != nil {
		delete(annotationsToMarshal, annotationLastApplied)
		delete(annotationsToMarshal, annotationLastAppliedHash)
	}
	if annotationsToSet == nil {
		annotationsToSet = make(map[string]string)
		objToSet.SetAnnotations(annotationsToSet)
	}

	manifest, err := json.Marshal(objToMarshal)
	if err != nil {
		return fmt.Errorf("error rendering JSON: %v", err)
	}

	hash, err := hashManifest(manifest)
	if err != nil {
		return err
	}

	annotationsToSet[annotationLastApplied] = string(manifest)
	annotationsToSet[annotationLastAppliedHash] = hash

	return nil
}

// Get gets the value of the `annotationLastApplied` annotation if it exists. If validate is true it
// also checks that the value of the `annotationLastAppliedHash` hash matches the annotation.
//
// Returns the manifest contained in the annotation on success, or an error on failure. Errors that
// can be checked for include ErrorLastAppliedAnnotationMissing,
// ErrorLastAppliedHashAnnotationMissing, and ErrorLastAppliedInvalidHash (the latter two only apply
// when `validate` is `true`).
func Get(obj metav1.Object, validate bool) (metav1.Object, error) {
	annotations := obj.GetAnnotations()
	manifest, ok := annotations[annotationLastApplied]
	if !ok {
		return nil, ErrorLastAppliedAnnotationMissing
	}

	if validate {
		hash, ok := annotations[annotationLastAppliedHash]
		if !ok {
			return nil, ErrorLastAppliedHashAnnotationMissing
		}

		computed, err := hashManifest([]byte(manifest))
		if err != nil {
			return nil, err
		}

		if hash != computed {
			return nil, ErrorLastAppliedInvalidHash
		}
	}

	unmarshaled := reflect.New(reflect.TypeOf(obj).Elem()).Interface().(metav1.Object)
	if err := json.Unmarshal([]byte(manifest), unmarshaled); err != nil {
		return nil, fmt.Errorf("error unmarshaling manifest: %v", err)
	}
	return unmarshaled, nil
}

// Copy copies the last-applied annotation from src to dst.
func Copy(dst, src metav1.Object) {
	srcAnnotations := src.GetAnnotations()
	dstAnnotations := dst.GetAnnotations()
	if dstAnnotations == nil {
		dstAnnotations = make(map[string]string)
		dst.SetAnnotations(dstAnnotations)
	}

	// Copy or delete the hash and annotation.
	annotation, ok := srcAnnotations[annotationLastApplied]
	if !ok {
		delete(dstAnnotations, annotationLastApplied)
	} else {
		dstAnnotations[annotationLastApplied] = annotation
	}

	hash, ok := srcAnnotations[annotationLastAppliedHash]
	if !ok {
		delete(dstAnnotations, annotationLastAppliedHash)
	} else {
		dstAnnotations[annotationLastAppliedHash] = hash
	}
}

// hashManifest returns a base64-encoded SHA-256 hash of the manifest + salt.
func hashManifest(manifest []byte) (string, error) {
	hasher := sha256.New()
	_, err := hasher.Write(manifest)
	if err != nil {
		return "", fmt.Errorf("error computing hash: %v", err)
	}
	hash := hasher.Sum(salt)
	return base64.StdEncoding.EncodeToString(hash), nil
}
