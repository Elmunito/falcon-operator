package falcon_container_deployer

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	falconv1alpha1 "github.com/crowdstrike/falcon-operator/apis/falcon/v1alpha1"
)

type FalconContainerDeployer struct {
	Ctx context.Context
	client.Client
	Log      logr.Logger
	Instance *falconv1alpha1.FalconConfig
}

func (d *FalconContainerDeployer) PhasePending() (ctrl.Result, error) {
	stream, err := d.UpsertImageStream()
	if err != nil {
		return d.Error("failed to upsert Image Stream", err)
	}
	if stream == nil {
		// It takes few moment for the ImageStream to be ready (shortly after it has been created)
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}

	d.Instance.Status.ErrorMessage = ""
	d.Instance.Status.Phase = falconv1alpha1.PhaseBuilding

	err = d.Client.Status().Update(d.Ctx, d.Instance)
	return ctrl.Result{}, err

}
