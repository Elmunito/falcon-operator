package falcon

import (
	"context"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	falconv1alpha1 "github.com/crowdstrike/falcon-operator/apis/falcon/v1alpha1"
	"github.com/crowdstrike/falcon-operator/pkg/falcon_container_deployer"
)

func (r *FalconConfigReconciler) phasePendingReconcile(ctx context.Context, instance *falconv1alpha1.FalconConfig, logger logr.Logger) (ctrl.Result, error) {
	logger.Info("Phase: Pending")
	d := falcon_container_deployer.FalconContainerDeployer{
		Ctx:      ctx,
		Client:   r.Client,
		Log:      logger,
		Instance: instance,
	}

	return d.PhasePending()
}
