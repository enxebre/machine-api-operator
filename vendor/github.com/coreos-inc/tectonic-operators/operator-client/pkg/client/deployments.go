package client

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang/glog"
	appsv1beta2 "k8s.io/api/apps/v1beta2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	deploymentRolloutPollInterval = time.Second
)

// GetDeployment returns the Deployment object for the given namespace and name.
func (c *Client) GetDeployment(namespace, name string) (*appsv1beta2.Deployment, error) {
	glog.V(4).Infof("[GET Deployment]: %s:%s", namespace, name)
	return c.AppsV1beta2().Deployments(namespace).Get(name, metav1.GetOptions{})
}

// CreateDeployment creates the Deployment object.
func (c *Client) CreateDeployment(dep *appsv1beta2.Deployment) (*appsv1beta2.Deployment, error) {
	glog.V(4).Infof("[CREATE Deployment]: %s:%s", dep.Namespace, dep.Name)
	return c.AppsV1beta2().Deployments(dep.Namespace).Create(dep)
}

// DeleteDeployment deletes the Deployment object.
func (c *Client) DeleteDeployment(namespace, name string, options *metav1.DeleteOptions) error {
	glog.V(4).Infof("[DELETE Deployment]: %s:%s", namespace, name)
	return c.AppsV1beta2().Deployments(namespace).Delete(name, options)
}

// UpdateDeployment updates a Deployment object by performing a 2-way patch between the existing
// Deployment and the result of the UpdateFunction.
//
// Returns the latest Deployment and true if it was updated, or an error.
func (c *Client) UpdateDeployment(dep *appsv1beta2.Deployment) (*appsv1beta2.Deployment, bool, error) {
	return c.PatchDeployment(nil, dep)
}

// PatchDeployment updates a Deployment object by performing a 3-way patch merge between the existing
// Deployment and `original` and `modified` manifests.
//
// Returns the latest Deployment and true if it was updated, or an error.
func (c *Client) PatchDeployment(original, modified *appsv1beta2.Deployment) (*appsv1beta2.Deployment, bool, error) {
	namespace, name := modified.Namespace, modified.Name
	glog.V(4).Infof("[PATCH Deployment]: %s:%s", namespace, name)

	current, err := c.AppsV1beta2().Deployments(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return nil, false, err
	}
	if modified == nil {
		return nil, false, errors.New("modified cannot be nil")
	}
	if original == nil {
		original = current // Emulate 2-way merge.
	}
	current.TypeMeta = modified.TypeMeta // make sure the type metas won't conflict.
	patchBytes, err := createThreeWayMergePatchPreservingCommands(original, modified, current)
	if err != nil {
		return nil, false, err
	}
	updated, err := c.AppsV1beta2().Deployments(namespace).Patch(name, types.StrategicMergePatchType, patchBytes)
	if err != nil {
		return nil, false, err
	}
	return updated, current.GetResourceVersion() != updated.GetResourceVersion(), nil
}

// RollingUpdateDeployment performs a rolling update on the given Deployment. It requires that the
// Deployment uses the RollingUpdateDeploymentStrategyType update strategy.
func (c *Client) RollingUpdateDeployment(dep *appsv1beta2.Deployment) (*appsv1beta2.Deployment, bool, error) {
	return c.RollingUpdateDeploymentMigrations(dep.Namespace, dep.Name, Update(dep), UpdateOpts{})
}

// RollingUpdateDeploymentMigrations performs a rolling update on the given Deployment. It
// requires that the Deployment uses the RollingUpdateDeploymentStrategyType update strategy.
//
// RollingUpdateDeploymentMigrations will run any before / during / after migrations that have been
// specified in the upgrade options.
func (c *Client) RollingUpdateDeploymentMigrations(namespace, name string, f UpdateFunction, opts UpdateOpts) (*appsv1beta2.Deployment, bool, error) {
	glog.V(4).Infof("[ROLLING UPDATE Deployment]: %s:%s", namespace, name)
	return c.RollingPatchDeploymentMigrations(namespace, name, updateToPatch(f), opts)
}

// RollingPatchDeployment performs a 3-way patch merge followed by rolling update on the given
// Deployment. It requires that the Deployment uses the RollingUpdateDeploymentStrategyType update
// strategy.
//
// RollingPatchDeployment will run any before / after migrations that have been specified in the
// upgrade options.
func (c *Client) RollingPatchDeployment(original, modified *appsv1beta2.Deployment) (*appsv1beta2.Deployment, bool, error) {
	return c.RollingPatchDeploymentMigrations(modified.Namespace, modified.Name, Patch(original, modified), UpdateOpts{})
}

// RollingPatchDeploymentMigrations performs a 3-way patch merge followed by rolling update on
// the given Deployment. It requires that the Deployment uses the RollingUpdateDeploymentStrategyType
// update strategy.
//
// RollingPatchDeploymentMigrations will run any before / after migrations that have been specified
// in the upgrade options.
func (c *Client) RollingPatchDeploymentMigrations(namespace, name string, f PatchFunction, opts UpdateOpts) (*appsv1beta2.Deployment, bool, error) {
	glog.V(4).Infof("[ROLLING PATCH Deployment]: %s:%s", namespace, name)

	current, err := c.AppsV1beta2().Deployments(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return nil, false, err
	}
	if err := checkDeploymentRollingUpdateEnabled(current); err != nil {
		return nil, false, err
	}

	if opts.Migrations != nil && len(opts.Migrations.Before) != 0 {
		if err := opts.Migrations.RunBeforeMigrations(c, namespace, name); err != nil {
			return nil, false, err
		}
		// Get object again as things may have changed during migrations.
		current, err = c.GetDeployment(namespace, name)
		if err != nil {
			return nil, false, err
		}
	}

	originalObj, modifiedObj, err := f(current.DeepCopy())
	if err != nil {
		return nil, false, err
	}
	// Check for nil interfaces.
	if modifiedObj == nil {
		return nil, false, errors.New("modified cannot be nil")
	}
	if originalObj == nil {
		originalObj = current // Emulate 2-way merge.
	}
	original, modified := originalObj.(*appsv1beta2.Deployment), modifiedObj.(*appsv1beta2.Deployment)
	// Check for nil pointers.
	if modified == nil {
		return nil, false, errors.New("modified cannot be nil")
	}
	if original == nil {
		original = current // Emulate 2-way merge.
	}
	current.TypeMeta = modified.TypeMeta // make sure the type metas won't conflict.
	patchBytes, err := createThreeWayMergePatchPreservingCommands(original, modified, current)
	if err != nil {
		return nil, false, err
	}
	updated, err := c.AppsV1beta2().Deployments(namespace).Patch(name, types.StrategicMergePatchType, patchBytes)
	if err != nil {
		return nil, false, err
	}
	if err = c.waitForDeploymentRollout(updated); err != nil {
		return nil, false, err
	}

	if opts.Migrations != nil && len(opts.Migrations.After) != 0 {
		if err := opts.Migrations.RunAfterMigrations(c, namespace, name); err != nil {
			return nil, false, err
		}
		// Get object again as things may have changed during migrations.
		updated, err = c.GetDeployment(namespace, name)
		if err != nil {
			return nil, false, err
		}
	}

	return updated, current.GetResourceVersion() != updated.GetResourceVersion(), nil
}

func checkDeploymentRollingUpdateEnabled(dep *appsv1beta2.Deployment) error {
	enabled := dep.Spec.Strategy.Type == appsv1beta2.RollingUpdateDeploymentStrategyType || dep.Spec.Strategy.Type == "" // Deployments rolling update by default
	if !enabled {
		return fmt.Errorf("Deployment %s/%s does not have rolling update strategy enabled", dep.GetNamespace(), dep.GetName())
	}
	return nil
}

func (c *Client) waitForDeploymentRollout(dep *appsv1beta2.Deployment) error {
	return wait.PollInfinite(deploymentRolloutPollInterval, func() (bool, error) {
		d, err := c.GetDeployment(dep.Namespace, dep.Name)
		if err != nil {
			// Do not return error here, as we could be updating the API Server itself, in which case we
			// want to continue waiting.
			glog.Errorf("error getting Deployment %s during rollout: %v", dep.Name, err)
			return false, nil
		}
		if d.Generation <= d.Status.ObservedGeneration && d.Status.UpdatedReplicas == d.Status.Replicas && d.Status.UnavailableReplicas == 0 {
			return true, nil
		}
		return false, nil
	})
}

// CreateOrRollingUpdateDeployment creates the Deployment if it doesn't exist. If the Deployment
// already exists, it will update the Deployment and wait for it to rollout. Returns true if the
// Deployment was created or updated, false if there was no update.
func (c *Client) CreateOrRollingUpdateDeployment(dep *appsv1beta2.Deployment) (*appsv1beta2.Deployment, bool, error) {
	glog.V(4).Infof("[CREATE OR ROLLING UPDATE Deployment]: %s:%s", dep.Namespace, dep.Name)

	_, err := c.GetDeployment(dep.Namespace, dep.Name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, false, err
		}
		created, err := c.CreateDeployment(dep)
		if err != nil {
			return nil, false, err
		}
		return created, true, err
	}
	return c.RollingUpdateDeployment(dep)
}

// ListDeploymentsWithLabels returns a list of deployments that matches the label selector.
// An empty list will be returned if no such deployments is found.
func (c *Client) ListDeploymentsWithLabels(namespace string, labels labels.Set) (*appsv1beta2.DeploymentList, error) {
	glog.V(4).Infof("[LIST Deployments] in %s, labels: %v", namespace, labels)

	opts := metav1.ListOptions{LabelSelector: labels.String()}
	return c.AppsV1beta2().Deployments(namespace).List(opts)
}
