package utils

import (
	"bytes"
	"fmt"
	coreops "github.com/portworx/sched-ops/k8s/core"
	"github.com/sirupsen/logrus"
	"os"
	"regexp"
	"strings"

	"github.com/hashicorp/go-version"
	v1 "k8s.io/api/core/v1"

	testutil "github.com/libopenstorage/operator/pkg/util/test"
)

// MakeDNS1123Compatible will make the given string a valid DNS1123 name, which is the same
// validation that Kubernetes uses for its object names.
// Borrowed from
// https://gitlab.com/gitlab-org/gitlab-runner/-/blob/0e2ae0001684f681ff901baa85e0d63ec7838568/executors/kubernetes/util.go#L268
func MakeDNS1123Compatible(name string) string {
	const (
		DNS1123NameMaximumLength         = 63
		DNS1123NotAllowedCharacters      = "[^-a-z0-9]"
		DNS1123NotAllowedStartCharacters = "^[^a-z0-9]+"
	)

	name = strings.ToLower(name)

	nameNotAllowedChars := regexp.MustCompile(DNS1123NotAllowedCharacters)
	name = nameNotAllowedChars.ReplaceAllString(name, "")

	nameNotAllowedStartChars := regexp.MustCompile(DNS1123NotAllowedStartCharacters)
	name = nameNotAllowedStartChars.ReplaceAllString(name, "")

	if len(name) > DNS1123NameMaximumLength {
		name = name[0:DNS1123NameMaximumLength]
	}

	return name
}

// GetPxVersionFromSpecGenURL gets the px version to install or upgrade,
// e.g. return version 2.9 for https://edge-install.portworx.com/2.9
func GetPxVersionFromSpecGenURL(url string) *version.Version {
	splitURL := strings.Split(url, "/")
	v, _ := version.NewVersion(splitURL[len(splitURL)-1])
	return v
}

func addDefaultEnvVars(origEnvVarList []v1.EnvVar, specGenURL string) ([]v1.EnvVar, error) {
	var envVarList []v1.EnvVar

	// Set release manifest URL and Docker credentials in case of edge-install.portworx.com
	if strings.Contains(specGenURL, "edge") {
		releaseManifestURL, err := testutil.ConstructPxReleaseManifestURL(specGenURL)
		if err != nil {
			return nil, err
		}

		// Add release manifest URL to Env Vars
		envVarList = append(envVarList, v1.EnvVar{Name: testutil.PxReleaseManifestURLEnvVarName, Value: releaseManifestURL})
	}

	// Add Portworx image properties, if specified
	if PxDockerUsername != "" && PxDockerPassword != "" {
		envVarList = append(envVarList,
			[]v1.EnvVar{
				{Name: testutil.PxRegistryUserEnvVarName, Value: PxDockerUsername},
				{Name: testutil.PxRegistryPasswordEnvVarName, Value: PxDockerPassword},
			}...)
	}
	if PxImageOverride != "" {
		envVarList = append(envVarList, v1.EnvVar{Name: testutil.PxImageEnvVarName, Value: PxImageOverride})
	}

	return mergeEnvVars(origEnvVarList, envVarList), nil
}

// mergeEnvVars will overwrite existing or add new env variables
func mergeEnvVars(origList, newList []v1.EnvVar) []v1.EnvVar {
	envMap := make(map[string]v1.EnvVar)
	var mergedList []v1.EnvVar
	for _, env := range origList {
		envMap[env.Name] = env
	}
	for _, env := range newList {
		envMap[env.Name] = env
	}
	for _, env := range envMap {
		mergedList = append(mergedList, *(env.DeepCopy()))
	}
	return mergedList
}

func RunPxCmd(command ...string) (string, string, error) {
	pl, err := coreops.Instance().ListPods(map[string]string{"name": "portworx"})
	if err != nil {
		return "", "", err
	}
	if len(pl.Items) == 0 {
		return "", "", fmt.Errorf("no pods found")
	}
	return RunPxCmdInPod(&pl.Items[0], command...)
}

func RunPxCmdInPod(pod *v1.Pod, input ...string) (string, string, error) {
	var out, stderr bytes.Buffer
	if pod == nil || len(input) <= 0 {
		return "", "", os.ErrInvalid
	}
	command := []string{"nsenter", "--mount=/host_proc/1/ns/mnt", "--", "/bin/bash", "-c"}
	command = append(command, input...)

	if logrus.IsLevelEnabled(logrus.DebugLevel) {
		logrus.Debugf("run on %s via %s: `%s`", pod.Spec.NodeName, pod.Name, strings.Join(command, " "))
	}
	request := &coreops.RunCommandInPodExRequest{
		Command:       command,
		PODName:       pod.Name,
		ContainerName: "portworx",
		Namespace:     pod.Namespace,
		UseTTY:        false,
		Stdin:         nil,
		Stdout:        &out,
		Stderr:        &stderr,
	}

	retErr := coreops.Instance().RunCommandInPodEx(request)
	if retErr != nil {
		return "", "", retErr
	}
	return out.String(), stderr.String(), nil
}
