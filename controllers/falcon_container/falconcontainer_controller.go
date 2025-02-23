package falcon

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/crowdstrike/falcon-operator/apis/falcon/v1alpha1"
	"github.com/crowdstrike/falcon-operator/version"
	"github.com/go-logr/logr"
	arv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// FalconContainerReconciler reconciles a FalconContainer object
type FalconContainerReconciler struct {
	client.Client
	Log        logr.Logger
	Scheme     *runtime.Scheme
	RestConfig *rest.Config
}

// SetupWithManager sets up the controller with the Manager.
func (r *FalconContainerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.FalconContainer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.ClusterRoleBinding{}).
		Owns(&arv1.MutatingWebhookConfiguration{}).
		Complete(r)
}

//+kubebuilder:rbac:groups=falcon.crowdstrike.com,resources=falconcontainers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=falcon.crowdstrike.com,resources=falconcontainers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=falcon.crowdstrike.com,resources=falconcontainers/finalizers,verbs=get;update;patch

// +kubebuilder:rbac:groups=image.openshift.io,resources=imagestreams,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=deployments,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=mutatingwebhookconfigurations,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=clusterrolebindings,verbs=get;list;watch;create;update;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.7.2/pkg/reconcile
func (r *FalconContainerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	falconContainer := &v1alpha1.FalconContainer{}

	if err := r.Get(ctx, req.NamespacedName, falconContainer); err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("FalconContainer resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if falconContainer.Status.Conditions == nil || len(falconContainer.Status.Conditions) == 0 {
		err := r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionPending,
			metav1.ConditionFalse,
			v1alpha1.ReasonReqNotMet,
			"FalconContainer progressing")
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	if falconContainer.Status.Version == "" {
		falconContainer.Status.Version = version.Get()
		err := r.Status().Update(ctx, falconContainer)
		if err != nil {
			log.Error(err, "Failed to update FalconContainer status for falconcontainer.Status.Version")
			return ctrl.Result{}, err
		}
	}

	if _, err := r.reconcileNamespace(ctx, log, falconContainer); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile namespace: %v", err)
	}

	// Image being set will override other image based settings
	if falconContainer.Spec.Image != nil && *falconContainer.Spec.Image != "" {
		if _, err := r.setImageTag(ctx, falconContainer); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set Falcon Container Image version: %v", err)
		}
	} else if os.Getenv("RELATED_IMAGE_SIDECAR_SENSOR") != "" && falconContainer.Spec.FalconAPI == nil {
		if _, err := r.setImageTag(ctx, falconContainer); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set Falcon Container Image version: %v", err)
		}
	} else {
		switch falconContainer.Spec.Registry.Type {
		case v1alpha1.RegistryTypeECR:
			if _, err := r.UpsertECRRepo(ctx); err != nil {
				err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile ECR repository: %v", err))
				if err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, fmt.Errorf("failed to reconcile ECR repository: %v", err)
			}
		case v1alpha1.RegistryTypeOpenshift:
			stream, err := r.reconcileImageStream(ctx, log, falconContainer)
			if err != nil {
				err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile Image Stream: %v", err))
				if err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, fmt.Errorf("failed to reconcile Image Stream")
			}
			if stream == nil {
				return ctrl.Result{}, nil
			}
		}

		// Create a CA Bundle ConfigMap if CACertificate attribute is set; overridden by the presence of a CACertificateConfigMap value
		if falconContainer.Spec.Registry.TLS.CACertificateConfigMap == "" && falconContainer.Spec.Registry.TLS.CACertificate != "" {
			if _, err := r.reconcileRegistryCABundleConfigMap(ctx, log, falconContainer); err != nil {
				err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile Registry CA Certificate Bundle ConfigMap: %v", err))
				if err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, fmt.Errorf("failed to reconcile Registry CA Certificate Bundle ConfigMap")
			}
		}

		if r.imageMirroringEnabled(falconContainer) {
			if err := r.PushImage(ctx, log, falconContainer); err != nil {
				err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to refresh Falcon Container image: %v", err))
				if err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, fmt.Errorf("cannot refresh Falcon Container image: %v", err)
			}
		} else {
			updated, err := r.verifyCrowdStrikeRegistry(ctx, log, falconContainer)
			if updated {
				return ctrl.Result{}, nil
			}
			if err != nil {
				err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to verify CrowdStrike Container Image Registry access: %v", err))
				if err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, fmt.Errorf("failed to verify CrowdStrike Container Image Registry access")
			}

			if _, err = r.reconcileRegistrySecrets(ctx, log, falconContainer); err != nil {
				err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile Falcon registry pull token Secrets: %v", err))
				if err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{}, fmt.Errorf("failed to reconcile Falcon registry pull token Secrets: %v", err)
			}
		}
	}

	if _, err := r.reconcileServiceAccount(ctx, log, falconContainer); err != nil {
		err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile Service Account: %v", err))
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Service Account: %v", err)
	}

	if _, err := r.reconcileClusterRoleBinding(ctx, log, falconContainer); err != nil {
		err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile Cluster Role Binding: %v", err))
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Cluster Role Binding: %v", err)
	}

	injectorTLS, err := r.reconcileInjectorTLSSecret(ctx, log, falconContainer)
	if err != nil {
		err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile injector TLS Secret: %v", err))
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, fmt.Errorf("failed to reconcile injector TLS Secret: %v", err)
	}
	caBundle := injectorTLS.Data["ca.crt"]
	if caBundle == nil {
		err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", "CA bundle not present in injector TLS Secret")
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, fmt.Errorf("CA bundle not present in injector TLS Secret")
	}

	if _, err = r.reconcileConfigMap(ctx, log, falconContainer); err != nil {
		err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile injector ConfigMap: %v", err))
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, fmt.Errorf("failed to reconcile injector ConfigMap: %v", err)
	}

	if _, err = r.reconcileDeployment(ctx, log, falconContainer); err != nil {
		err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile injector Deployment: %v", err))
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, fmt.Errorf("failed to reconcile injector Deployment: %v", err)
	}

	if _, err = r.reconcileService(ctx, log, falconContainer); err != nil {
		err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile injector Service: %v", err))
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, fmt.Errorf("failed to reconcile injector Service: %v", err)
	}

	pod, err := r.injectorPodReady(ctx, falconContainer)
	if err != nil && err.Error() != "No Injector pod found in a Ready state" {
		err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to find Ready injector pod: %v", err))
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, fmt.Errorf("failed to find Ready injector pod: %v", err)
	}
	if pod.Name == "" {
		log.Info("Looking for a Ready injector pod", "namespace", r.Namespace())
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if _, err = r.reconcileWebhook(ctx, log, falconContainer, caBundle); err != nil {
		err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionFailed, metav1.ConditionFalse, "Reconciling", fmt.Sprintf("failed to reconcile injector MutatingWebhookConfiguration: %v", err))
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, fmt.Errorf("failed to reconcile injector MutatingWebhookConfiguration: %v", err)
	}

	err = r.StatusUpdate(ctx, req, log, falconContainer, v1alpha1.ConditionSuccess,
		metav1.ConditionTrue,
		v1alpha1.ReasonInstallSucceeded,
		"FalconContainer installation completed")
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *FalconContainerReconciler) StatusUpdate(ctx context.Context, req ctrl.Request, log logr.Logger, falconContainer *v1alpha1.FalconContainer, condType string, status metav1.ConditionStatus, reason string, message string) error {
	meta.SetStatusCondition(&falconContainer.Status.Conditions, metav1.Condition{
		Status:             status,
		Reason:             reason,
		Message:            message,
		Type:               condType,
		ObservedGeneration: falconContainer.GetGeneration(),
	})
	if err := r.Status().Update(ctx, falconContainer); err != nil {
		log.Error(err, "Failed to update FalconContainer status")
		return err
	}

	// Let's re-fetch the memcached Custom Resource after update the status
	// so that we have the latest state of the resource on the cluster and we will avoid
	// raise the issue "the object has been modified, please apply
	// your changes to the latest version and try again" which would re-trigger the reconciliation
	// if we try to update it again in the following operations
	if err := r.Get(ctx, req.NamespacedName, falconContainer); err != nil {
		log.Error(err, "Failed to re-fetch FalconContainer")
		return err
	}
	return nil
}
