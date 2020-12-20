package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	versionedclient "istio.io/client-go/pkg/clientset/versioned"

	// kubeconfig auth via gcloud
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

const previewPrefixName = "pr"

func init() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.SetOutput(os.Stdout)
}

func main() {
	createCmd := &cmdCreate{}

	rootCmd.AddCommand(createCmd.New())

	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("failed to rootCmd.Execute: %v", err)
	}
}

var rootCmd = &cobra.Command{
	Use: "preview",
}

func previewName(origin, version string) string {
	return fmt.Sprintf("%s%s-%s", previewPrefixName, version, origin)
}

func getVersionFromPreview(s string) string {
	if !strings.HasPrefix(s, previewPrefixName) {
		return s
	}
	sp := strings.SplitN(s[len(previewPrefixName):], "-", 2)
	return sp[0]
}

func previewVirtualServiceName(originSvc string) string {
	return fmt.Sprintf("%s%s-virtual-service", previewPrefixName, originSvc)
}

func previewGatewayVirtualServiceName(originSvc, version string) string {
	return fmt.Sprintf("%s-gateway-virtual-service", previewName(originSvc, version))
}

func newK8sClient() (*kubernetes.Clientset, error) {
	config, err := getRestConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

func newIstioClient() (*versionedclient.Clientset, error) {
	config, err := getRestConfig()
	if err != nil {
		return nil, err
	}
	return versionedclient.NewForConfig(config)
}

const runInCluster = "RUN_IN_CLUSTER"

func getRestConfig() (*rest.Config, error) {
	if os.Getenv(runInCluster) != "" {
		return rest.InClusterConfig()
	}
	kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}
