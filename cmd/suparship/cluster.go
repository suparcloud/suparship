package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/kube"
)

var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Manage registered workload clusters",
	Long: `Manage the clusters that suparship deploys apps to.

suparship itself runs on a tooling cluster alongside ArgoCD. Workload
clusters are registered here so ArgoCD can deploy to them and suparship
can stream pod logs directly.`,
}

var clusterListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered clusters",
	RunE:  runClusterList,
}

var clusterAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Register a new workload cluster",
	Long: `Register a Kubernetes cluster with suparship.

The cluster credentials are stored in a Secret in the suparship-system
namespace. ArgoCD is notified automatically via a cluster Secret in the
argocd namespace so it can deploy ApplicationSets to the new cluster.

The kubeconfig is NEVER written to Git.`,
	Example: `  # Register the staging cluster
  suparship cluster add \
    --name staging-cluster \
    --display-name "Staging" \
    --api-server https://10.0.0.1:6443 \
    --kubeconfig ~/.kube/staging.yaml`,
	RunE: runClusterAdd,
}

var clusterRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a registered cluster",
	Args:  cobra.ExactArgs(1),
	RunE:  runClusterRemove,
}

func init() {
	clusterCmd.AddCommand(clusterListCmd, clusterAddCmd, clusterRemoveCmd)
	rootCmd.AddCommand(clusterCmd)

	clusterAddCmd.Flags().String("name", "", "cluster identifier (DNS label, required)")
	clusterAddCmd.Flags().String("display-name", "", "human-readable name shown in the UI")
	clusterAddCmd.Flags().String("api-server", "", "Kubernetes API server URL (required)")
	clusterAddCmd.Flags().String("kubeconfig", "", "path to kubeconfig file for this cluster (required)")

	_ = clusterAddCmd.MarkFlagRequired("name")
	_ = clusterAddCmd.MarkFlagRequired("api-server")
	_ = clusterAddCmd.MarkFlagRequired("kubeconfig")
}

func runClusterList(cmd *cobra.Command, _ []string) error {
	store, err := clusterStoreFromFlags(cmd)
	if err != nil {
		return err
	}

	clusters, err := store.ListClusters(cmd.Context())
	if err != nil {
		return fmt.Errorf("listing clusters: %w", err)
	}

	if len(clusters) == 0 {
		fmt.Println("No clusters registered. Use 'suparship cluster add' to register one.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "NAME\tDISPLAY NAME\tAPI SERVER\tSTATUS")
	for _, c := range clusters {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Name, c.DisplayName, c.APIServer, c.Status)
	}
	return nil
}

func runClusterAdd(cmd *cobra.Command, _ []string) error {
	name, _ := cmd.Flags().GetString("name")
	displayName, _ := cmd.Flags().GetString("display-name")
	apiServer, _ := cmd.Flags().GetString("api-server")
	kubeconfigPath, _ := cmd.Flags().GetString("kubeconfig")

	if displayName == "" {
		displayName = name
	}

	kubeconfigBytes, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return fmt.Errorf("reading kubeconfig %q: %w", kubeconfigPath, err)
	}

	cluster := domain.Cluster{
		Name:        name,
		DisplayName: displayName,
		APIServer:   apiServer,
		Status:      "ready",
	}

	if err := domain.ValidateClusterName(name); err != nil {
		return err
	}

	store, err := clusterStoreFromFlags(cmd)
	if err != nil {
		return err
	}

	if err := store.CreateCluster(cmd.Context(), cluster, kubeconfigBytes); err != nil {
		return fmt.Errorf("registering cluster: %w", err)
	}

	fmt.Printf("Cluster %q registered successfully.\n", name)
	fmt.Println("ArgoCD has been notified — ApplicationSets targeting this cluster will deploy shortly.")
	return nil
}

func runClusterRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	store, err := clusterStoreFromFlags(cmd)
	if err != nil {
		return err
	}

	if err := store.DeleteCluster(cmd.Context(), name); err != nil {
		return fmt.Errorf("removing cluster: %w", err)
	}

	fmt.Printf("Cluster %q removed.\n", name)
	return nil
}

// clusterStoreFromFlags builds a K8sClusterStore using the global kubeconfig flags.
func clusterStoreFromFlags(cmd *cobra.Command) (*kube.K8sClusterStore, error) {
	kubeconfigFlag, _ := cmd.Root().PersistentFlags().GetString("kubeconfig")
	kubeContextFlag, _ := cmd.Root().PersistentFlags().GetString("context")

	client, err := k8s.NewClientset(kubeconfigFlag, kubeContextFlag)
	if err != nil {
		return nil, fmt.Errorf("connecting to Kubernetes: %w", err)
	}
	return kube.NewK8sClusterStore(client), nil
}
