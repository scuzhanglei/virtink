package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/namsral/flag"
)

const (
	KindVersion       = "v0.14.0"
	SkaffoldVersion   = "v2.0.0"
	KuttlVersion      = "0.12.1"
	KubectlVersion    = "v1.24.0"
	toolBinaryBaseDir = "/var/run/virtink/e2e/bin"
)

func main() {
	var clusterName string
	var kubeconfig string
	var forceCreateCluster bool

	flag.StringVar(&clusterName, "cluster-name", clusterName, "KinD cluster name for running E2E tests")
	flag.StringVar(&kubeconfig, "kubeconfig", kubeconfig, "kubeconfig of cluster for running E2E tests")
	flag.BoolVar(&forceCreateCluster, "force-create-cluster", false, "force Create a new kind cluster")
	flag.Parse()

	if err := buildImages(); err != nil {
		log.Fatalf("build images: %ss", err)
	}

	if err := installTools(); err != nil {
		log.Fatalf("install tools: %ss", err)
	}
	if kubeconfig == "" {
		var err error
		kubeconfig, err = ensureKindClusters(clusterName, forceCreateCluster)
		if err != nil {
			log.Fatalf("create kind cluster: %ss", err)
		}
	}

	if err := deployCommponents(kubeconfig); err != nil {
		log.Fatalf("deploy components: %ss", err)
	}

	if err := runTestCases(kubeconfig); err != nil {
		log.Fatalf("kuttl test: %ss", err)
	}
}

type Tool struct {
	name        string
	version     string
	downloadURL string
}

func installTools() error {
	tools := []Tool{
		{
			name:        "kind",
			version:     "v0.14.0",
			downloadURL: "https://kind.sigs.k8s.io/dl/$(version)/kind-$(GOOS)-$(GOARCH)",
		}, {
			name:        "skaffold",
			version:     "v2.0.0",
			downloadURL: "https://storage.googleapis.com/skaffold/releases/latest/skaffold-$(GOOS)-$(GOARCH)",
		}, {
			name:        "kuttl",
			version:     "0.12.1",
			downloadURL: "https://github.com/kudobuilder/kuttl/releases/download/v$(version)/kubectl-kuttl_$(version)_$(GOOS)_x86_64", //TODO x86_64
		}, {
			name:        "kubectl",
			version:     "v1.24.0",
			downloadURL: "https://dl.k8s.io/release/$(version)/bin/$(GOOS)/$(GOARCH)/kubectl",
		},
	}
	for _, tool := range tools {
		if err := installTool(tool); err != nil {
			return err
		}
	}
	return nil
}

func isToolInstalled(name, version string) (bool, error) {
	binPath := filepath.Join(toolBinaryBaseDir, "name")
	if _, err := os.Stat(binPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	versionOutput, err := exec.Command(binPath, "version").CombinedOutput()
	if err != nil {
		return false, err
	}
	if strings.Contains(string(versionOutput), version) {
		return true, nil
	}
	return false, nil
}

func installTool(tool Tool) error {
	installed, err := isToolInstalled(tool.name, tool.version)
	if err != nil {
		return err
	}
	if installed {
		return nil
	}

	if err := os.MkdirAll(toolBinaryBaseDir, 0755); err != nil {
		return err
	}

	binaryPath := filepath.Join(toolBinaryBaseDir, tool.name)
	if err := os.RemoveAll(binaryPath); err != nil {
		return err
	}

	downloadURL := strings.NewReplacer("$(version)", tool.version, "$(GOOS)", os.Getenv("GOOS"), "$(GOARCH)", os.Getenv("GOARCH")).Replace(tool.downloadURL)
	if _, err := exec.Command("curl", "-sLo", binaryPath, downloadURL).CombinedOutput(); err != nil {
		return err
	}
	if _, err := exec.Command("chmod", "+x", binaryPath).CombinedOutput(); err != nil {
		return err
	}
	return nil
}

type Image struct {
	tag        string
	dockerfile string
	buildArgs  []string
}

func buildImages() error {
	images := []Image{{
		tag:        "virt-controller:e2e",
		dockerfile: "build/virt-controller/Dockerfile",
		buildArgs:  []string{"PRERUNNER_IMAGE=virt-prerunner:e2e"},
	}, {
		tag:        "virt-daemon:e2e",
		dockerfile: "build/virt-daemon/Dockerfile",
	}, {
		tag:        "virt-prerunner:e2e",
		dockerfile: "build/virt-prerunner/Dockerfile",
	}}
	for _, image := range images {
		buildArgs := []string{"buildx", "build", "-t", image.tag, "-f", image.dockerfile, "--load", "."}
		for _, arg := range image.buildArgs {
			buildArgs = append(buildArgs, "--build-arg", arg)
		}
		if err := runCommand(exec.Command("docker", buildArgs...)); err != nil {
			return err
		}
	}
	return nil
}

func runCommand(cmd *exec.Cmd) error {
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	// TODO
	fmt.Println(cmd.String())
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run command %q: %s", cmd.String(), err)
	}
	return nil
}

func getCommandOutput(cmd *exec.Cmd) (string, error) {
	fmt.Println(cmd.String())
	cmd.Stdin = os.Stdin
	out, err := cmd.CombinedOutput()
	output := string(out)
	if err != nil {
		return output, fmt.Errorf("run command %q: %s: %s", cmd, err, output)
	}
	return output, nil
}

func splitCommand(cmdStr string) *exec.Cmd {
	args := strings.Split(cmdStr, " ")
	newArgs := []string{}
	for _, arg := range args {
		if arg != "" {
			newArgs = append(newArgs, arg)
		}
	}
	return exec.Command(newArgs[0], newArgs[1:]...)
}

func ensureKindClusters(clusterName string, reCreate bool) (string, error) {
	kubeconfig := "./tmp/virtink-e2e-cluster.kubeconfig"
	output, err := getCommandOutput(exec.Command("./bin/kind", "get", "clusters"))
	if err != nil {
		return "", err
	}
	// TODO reCreate
	if strings.Contains(output, clusterName) {
		//TODO check cluster is ready?
		return kubeconfig, nil
	}

	if _, err := getCommandOutput(exec.Command("./bin/kind", "create", "cluster", "--config", "test/e2e/config/kind/config.yaml", "--name", clusterName, "--kubeconfig", kubeconfig)); err != nil {
		return "", err
	}
	return kubeconfig, nil
}

func deployCommponents(kubeconfig string) error {
	var kubectlCmd = func(cmdStr string) *exec.Cmd {
		cmd := splitCommand(cmdStr)
		cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfig))
		return cmd
	}

	if err := runCommand(kubectlCmd("./bin/kubectl apply -f https://projectcalico.docs.tigera.io/archive/v3.23/manifests/calico.yaml")); err != nil {
		return err
	}

	if _, err := getCommandOutput(kubectlCmd("./bin/kubectl wait -n kube-system deployment calico-kube-controllers --for condition=Available --timeout 60s")); err != nil {
		return err
	}

	if err := runCommand(kubectlCmd("./bin/kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.8.2/cert-manager.yaml")); err != nil {
		return err
	}
	// TODO check ready

	if err := runCommand(kubectlCmd("./bin/kubectl apply -f https://github.com/kubevirt/containerized-data-importer/releases/download/v1.53.0/cdi-operator.yaml")); err != nil {
		return err
	}
	if err := runCommand(kubectlCmd("./bin/kubectl wait -n cdi deployment cdi-operator --for condition=Available --timeout -1s")); err != nil {
		return err
	}
	if err := runCommand(kubectlCmd("./bin/kubectl apply -f https://github.com/kubevirt/containerized-data-importer/releases/download/v1.53.0/cdi-cr.yaml")); err != nil {
		return err
	}
	if err := runCommand(kubectlCmd("./bin/kubectl wait cdi.cdi.kubevirt.io cdi --for condition=Available --timeout -1s")); err != nil {
		return err
	}

	if err := runCommand(kubectlCmd("./bin/kubectl apply -f test/e2e/config/rook-nfs/crds.yaml")); err != nil {
		return err
	}
	if err := runCommand(kubectlCmd("./bin/kubectl wait crd nfsservers.nfs.rook.io --for condition=Established")); err != nil {
		return err
	}
	if err := runCommand(kubectlCmd("./bin/kubectl apply -f test/e2e/config/rook-nfs/")); err != nil {
		return err
	}

	virtinkManifest := "/tmp/virtink-e2e.yaml"
	renderVirtinkCmd := splitCommand(fmt.Sprintf("./bin/skaffold render --offline=true --default-repo= --digest-source=tag --images virt-controller:e2e,virt-daemon:e2e --output %s", virtinkManifest))
	renderVirtinkCmd.Env = os.Environ()
	renderVirtinkCmd.Env = append(renderVirtinkCmd.Env, fmt.Sprintf("PATH=%s", "/mnt/data/codes/go/src/github.com/smartxworks/virtink/bin"))
	if _, err := getCommandOutput(renderVirtinkCmd); err != nil {
		return err
	}

	if err := runCommand(kubectlCmd(fmt.Sprintf("./bin/kubectl apply -f %s", virtinkManifest))); err != nil {
		return err
	}
	if err := runCommand(kubectlCmd("./bin/kubectl wait -n virtink-system deployment virt-controller --for condition=Available --timeout -1s")); err != nil {
		return err
	}
	return nil
}

func runTestCases(kubeconfig string) error {
	kuttlCmd := splitCommand("./bin/kuttl test --config test/e2e/kuttl-test.yaml")
	kuttlCmd.Env = os.Environ()
	kuttlCmd.Env = append(kuttlCmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfig))
	if err := runCommand(kuttlCmd); err != nil {
		return err
	}
	return nil
}
