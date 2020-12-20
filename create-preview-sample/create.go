package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	networkingv1beta1 "istio.io/api/networking/v1beta1"
	istionetworking "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type cmdCreate struct {
	originService  string
	previewVersion string
	previewGateway string
	previewURL     string
}

func (c *cmdCreate) New() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "create preview resources",
		RunE:  c.RunE,
	}

	cmd.Flags().StringVarP(&c.previewVersion, "version", "v", "", "preview version (e.g. pull-request ID)")
	cmd.MarkFlagRequired("version")
	cmd.Flags().StringVarP(&c.originService, "service", "s", "", "preview original service name")
	cmd.MarkFlagRequired("service")
	cmd.Flags().StringVarP(&c.previewGateway, "gateway", "g", "my-gateway", "ingressgateway name")
	cmd.Flags().StringVarP(&c.previewURL, "url", "u", "", "preview endpoint url")
	cmd.MarkFlagRequired("previewURL")

	return cmd
}

func (c *cmdCreate) RunE(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	selector, err := createService(ctx, c.originService, c.previewVersion)
	if err != nil {
		return err
	}
	if err := createDeploy(ctx, selector, c.previewVersion); err != nil {
		return err
	}
	if err := createSidecarVirtualService(ctx, c.originService, c.previewVersion); err != nil {
		return err
	}

	if err := createGatewayVirtualService(ctx, c.previewURL, c.previewGateway, c.originService, c.previewVersion); err != nil {
		return err
	}

	return nil
}

const appNamespace = "default"

func createService(ctx context.Context, name, version string) (*deploySelector, error) {
	cs, err := newK8sClient()
	if err != nil {
		return nil, err
	}
	itf := cs.CoreV1().Services(appNamespace)
	svcs, err := itf.List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var baseSvc corev1.Service
	for _, item := range svcs.Items {
		if item.Name == name {
			baseSvc = item
			break
		}
	}

	var selector *deploySelector
	for k, v := range baseSvc.Spec.Selector {
		selector = &deploySelector{
			key:   k,
			value: v,
		}
	}
	if selector == nil {
		return nil, fmt.Errorf("not found selector from service %s", name)
	}

	newSvc := baseSvc.DeepCopy()
	newSvc.Name = previewName(baseSvc.Name, version)
	newSvc.ObjectMeta.ResourceVersion = ""
	newSvc.Labels[selector.key] = previewName(selector.value, version)
	newSvc.Spec.Selector[selector.key] = previewName(selector.value, version)

	if _, err := itf.Create(ctx, newSvc, metav1.CreateOptions{}); err != nil {
		return nil, err
	}
	return selector, nil
}

type deploySelector struct {
	key   string
	value string
}

func createDeploy(ctx context.Context, selector *deploySelector, version string) error {
	cs, err := newK8sClient()
	if err != nil {
		return err
	}
	itf := cs.AppsV1().Deployments(appNamespace)
	deps, err := itf.List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	var baseDeploy appsv1.Deployment
	for _, item := range deps.Items {
		val, ok := item.Spec.Selector.MatchLabels[selector.key]
		if !ok {
			continue
		}
		if val == selector.value {
			baseDeploy = item
			break
		}
	}

	newDeploy := baseDeploy.DeepCopy()
	newDeploy.Name = previewName(baseDeploy.Name, version)
	newDeploy.ResourceVersion = ""
	newDeploy.Spec.Selector.MatchLabels[selector.key] = previewName(selector.value, version)
	if _, ok := newDeploy.Spec.Template.Labels[selector.key]; ok {
		newDeploy.Spec.Template.Labels[selector.key] = previewName(selector.value, version)
	}

	if _, err := itf.Create(ctx, newDeploy, metav1.CreateOptions{}); err != nil {
		return err
	}
	return nil
}

const (
	istioNamespace = "istio-system"
	previewHeader  = "X-PREVIEW"
)

func createGatewayVirtualService(ctx context.Context, url, gateway, originSvcName, version string) error {
	ics, err := newIstioClient()
	if err != err {
		return err
	}
	itf := ics.NetworkingV1beta1().VirtualServices(istioNamespace)

	previewSvcName := fmt.Sprintf("%s.%s.svc.cluster.local", originSvcName, appNamespace)
	previewHost := previewName(previewSvcName, version)
	vs := &istionetworking.VirtualService{
		TypeMeta: metav1.TypeMeta{
			Kind:       "VirtualService",
			APIVersion: "networking.istio.io/v1beta1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      previewGatewayVirtualServiceName(originSvcName, version),
			Namespace: istioNamespace,
		},
		Spec: networkingv1beta1.VirtualService{
			Hosts:    []string{url},
			Gateways: []string{gateway},
			Http: []*networkingv1beta1.HTTPRoute{{
				Name: previewHost,
				Route: []*networkingv1beta1.HTTPRouteDestination{{
					Headers: &networkingv1beta1.Headers{
						Request: &networkingv1beta1.Headers_HeaderOperations{
							Add: map[string]string{
								previewHeader: previewName(originSvcName, version),
							},
						},
					},
					Destination: &networkingv1beta1.Destination{
						Host: previewHost,
					},
				}},
			}},
		},
	}

	if _, err := itf.Create(ctx, vs, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create new virtual-service: %w", err)
	}
	return nil
}

func createSidecarVirtualService(ctx context.Context, originSvcName, version string) error {
	var (
		originHost  = fmt.Sprintf("%s.%s.svc.cluster.local", originSvcName, appNamespace)
		previewHost = previewName(originHost, version)
	)

	ics, err := newIstioClient()
	if err != err {
		return err
	}
	vss, err := ics.NetworkingV1beta1().VirtualServices(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	var baseResource *istionetworking.VirtualService
	for _, i := range vss.Items {
		// NOTE: ignore `mesh` gateway pattern
		if len(i.Spec.Gateways) != 0 {
			continue
		}

		for _, h := range i.Spec.Hosts {
			if h == originHost {
				baseResource = i.DeepCopy()
			}
		}
	}

	itf := ics.NetworkingV1beta1().VirtualServices(istioNamespace)

	// origin host用sidecar virtual serviceの新規作成
	if baseResource == nil {
		base := &istionetworking.VirtualService{
			TypeMeta: metav1.TypeMeta{
				Kind:       "VirtualService",
				APIVersion: "networking.istio.io/v1beta1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      previewVirtualServiceName(originSvcName),
				Namespace: istioNamespace,
			},
			Spec: networkingv1beta1.VirtualService{
				Hosts: []string{originHost},
				Http: []*networkingv1beta1.HTTPRoute{{
					Name: originHost,
					Route: []*networkingv1beta1.HTTPRouteDestination{{
						Destination: &networkingv1beta1.Destination{
							Host: originHost,
						},
					}},
				}},
			},
		}
		ret, err := itf.Create(ctx, base, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		baseResource = ret
	}

	route := &networkingv1beta1.HTTPRoute{
		Name: previewHost,
		Match: []*networkingv1beta1.HTTPMatchRequest{{
			Headers: map[string]*networkingv1beta1.StringMatch{
				previewHeader: {
					MatchType: &networkingv1beta1.StringMatch_Prefix{
						Prefix: previewName("", version),
					},
				},
			},
		}},
		Route: []*networkingv1beta1.HTTPRouteDestination{{
			Destination: &networkingv1beta1.Destination{
				Host: previewHost,
			},
		}},
	}
	baseResource.Spec.Http = append(baseResource.Spec.Http, route)
	baseResource.ManagedFields = []metav1.ManagedFieldsEntry{}
	baseResource.TypeMeta = metav1.TypeMeta{
		Kind:       "VirtualService",
		APIVersion: "networking.istio.io/v1beta1",
	}

	// 通常サービスへのルーティングを末尾に移動させる
	sort.Slice(baseResource.Spec.Http, func(i, _ int) bool {
		return strings.HasPrefix(baseResource.Spec.Http[i].Name, previewPrefixName)
	})

	bytes, err := json.Marshal(baseResource)
	if err != nil {
		return err
	}

	force := true
	opts := metav1.PatchOptions{
		FieldManager: "preview",
		Force:        &force,
	}
	if _, err := itf.Patch(ctx, baseResource.Name, types.ApplyPatchType, bytes, opts); err != nil {
		return err
	}
	return nil
}
