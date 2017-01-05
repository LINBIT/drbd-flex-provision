/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package volume

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"reflect"
	"strconv"
	"strings"
	"syscall"

	"github.com/golang/glog"
	"github.com/kubernetes-incubator/nfs-provisioner/controller"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api/resource"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/pkg/types"
	"k8s.io/client-go/pkg/util/uuid"
)

const (
	// Name of the file where an nfsProvisioner will store its identity
	identityFile = "nfs-provisioner.identity"

	// are we allowed to set this? else make up our own
	annCreatedBy = "kubernetes.io/createdby"
	createdBy    = "nfs-dynamic-provisioner"

	// A PV annotation for the entire ganesha EXPORT block or /etc/exports
	// block, needed for deletion.
	annExportBlock = "EXPORT_block"
	// A PV annotation for the exportId of this PV's backing ganesha/kernel export
	// , needed for ganesha deletion and used for deleting the entry in exportIds
	// map so the id can be reassigned.
	annExportId = "Export_Id"

	// A PV annotation for the project quota info block, needed for quota
	// deletion.
	annProjectBlock = "Project_block"
	// A PV annotation for the project quota id, needed for quota deletion
	annProjectId = "Project_Id"

	// VolumeGidAnnotationKey is the key of the annotation on the PersistentVolume
	// object that specifies a supplemental GID.
	VolumeGidAnnotationKey = "pv.beta.kubernetes.io/gid"

	// A PV annotation for the identity of the nfsProvisioner that provisioned it
	annProvisionerId = "Provisioner_Id"

	podIPEnv     = "POD_IP"
	serviceEnv   = "SERVICE_NAME"
	namespaceEnv = "POD_NAMESPACE"
	nodeEnv      = "NODE_NAME"
)

func NewNFSProvisioner(exportDir string, client kubernetes.Interface, useGanesha bool, ganeshaConfig string, rootSquash bool, enableXfsQuota bool) controller.Provisioner {
	var exporter exporter
	if useGanesha {
		exporter = newGaneshaExporter(ganeshaConfig, rootSquash)
	} else {
		exporter = newKernelExporter(rootSquash)
	}
	var quotaer quotaer
	var err error
	if enableXfsQuota {
		quotaer, err = newXfsQuotaer(exportDir)
		if err != nil {
			glog.Fatalf("Error creating xfs quotaer! %v", err)
		}
	} else {
		quotaer = newDummyQuotaer()
	}
	return newNFSProvisionerInternal(exportDir, client, exporter, quotaer)
}

func newNFSProvisionerInternal(exportDir string, client kubernetes.Interface, exporter exporter, quotaer quotaer) *nfsProvisioner {
	if _, err := os.Stat(exportDir); os.IsNotExist(err) {
		glog.Fatalf("exportDir %s does not exist!", exportDir)
	}

	var identity types.UID
	identityPath := path.Join(exportDir, identityFile)
	if _, err := os.Stat(identityPath); os.IsNotExist(err) {
		identity = uuid.NewUUID()
		err := ioutil.WriteFile(identityPath, []byte(identity), 0600)
		if err != nil {
			glog.Fatalf("Error writing identity file %s! %v", identityPath, err)
		}
	} else {
		read, err := ioutil.ReadFile(identityPath)
		if err != nil {
			glog.Fatalf("Error reading identity file %s! %v", identityPath, err)
		}
		identity = types.UID(strings.TrimSpace(string(read)))
	}

	provisioner := &nfsProvisioner{
		exportDir:    exportDir,
		client:       client,
		exporter:     exporter,
		quotaer:      quotaer,
		identity:     identity,
		podIPEnv:     podIPEnv,
		serviceEnv:   serviceEnv,
		namespaceEnv: namespaceEnv,
		nodeEnv:      nodeEnv,
	}

	return provisioner
}

type nfsProvisioner struct {
	// The directory to create PV-backing directories in
	exportDir string

	// Client, needed for getting a service cluster IP to put as the NFS server of
	// provisioned PVs
	client kubernetes.Interface

	// The exporter to use for exporting NFS shares
	exporter exporter

	// The quotaer to use for setting per-share/directory/project quotas
	quotaer quotaer

	// Identity of this nfsProvisioner, generated & persisted to exportDir or
	// recovered from there. Used to mark provisioned PVs
	identity types.UID

	// Environment variables the provisioner pod needs valid values for in order to
	// put a service cluster IP as the server of provisioned NFS PVs, passed in
	// via downward API. If serviceEnv is set, namespaceEnv must be too.
	podIPEnv     string
	serviceEnv   string
	namespaceEnv string
	nodeEnv      string
}

var _ controller.Provisioner = &nfsProvisioner{}

// Provision creates a volume i.e. the storage asset and returns a PV object for
// the volume.
func (p *nfsProvisioner) Provision(options controller.VolumeOptions) (*v1.PersistentVolume, error) {
	server, path, supGroup, exportBlock, exportId, projectBlock, projectId, err := p.createVolume(options)
	if err != nil {
		return nil, err
	}

	annotations := make(map[string]string)
	annotations[annCreatedBy] = createdBy
	annotations[annExportBlock] = exportBlock
	annotations[annExportId] = strconv.FormatUint(uint64(exportId), 10)
	annotations[annProjectBlock] = projectBlock
	annotations[annProjectId] = strconv.FormatUint(uint64(projectId), 10)
	if supGroup != 0 {
		annotations[VolumeGidAnnotationKey] = strconv.FormatUint(supGroup, 10)
	}
	annotations[annProvisionerId] = string(p.identity)

	pv := &v1.PersistentVolume{
		ObjectMeta: v1.ObjectMeta{
			Name:        options.PVName,
			Labels:      map[string]string{},
			Annotations: annotations,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: options.PersistentVolumeReclaimPolicy,
			AccessModes:                   options.PVC.Spec.AccessModes,
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)],
			},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				NFS: &v1.NFSVolumeSource{
					Server:   server,
					Path:     path,
					ReadOnly: false,
				},
			},
		},
	}

	return pv, nil
}

// createVolume creates a volume i.e. the storage asset. It creates a unique
// directory under /export and exports it. Returns the server IP, the path, a
// zero/non-zero supplemental group, the block it added to either the ganesha
// config or /etc/exports, and the exportId
// TODO return values
func (p *nfsProvisioner) createVolume(options controller.VolumeOptions) (string, string, uint64, string, uint16, string, uint16, error) {
	gid, err := p.validateOptions(options)
	if err != nil {
		return "", "", 0, "", 0, "", 0, fmt.Errorf("error validating options for volume: %v", err)
	}

	server, err := p.getServer()
	if err != nil {
		return "", "", 0, "", 0, "", 0, fmt.Errorf("error getting NFS server IP for volume: %v", err)
	}

	path := path.Join(p.exportDir, options.PVName)

	err = p.createDirectory(options.PVName, gid)
	if err != nil {
		return "", "", 0, "", 0, "", 0, fmt.Errorf("error creating directory for volume: %v", err)
	}

	exportBlock, exportId, err := p.createExport(options.PVName)
	if err != nil {
		os.RemoveAll(path)
		return "", "", 0, "", 0, "", 0, fmt.Errorf("error creating export for volume: %v", err)
	}

	projectBlock, projectId, err := p.createQuota(options.PVName, options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)])
	if err != nil {
		os.RemoveAll(path)
		return "", "", 0, "", 0, "", 0, fmt.Errorf("error creating quota for volume: %v", err)
	}

	return server, path, 0, exportBlock, exportId, projectBlock, projectId, nil
}

func (p *nfsProvisioner) validateOptions(options controller.VolumeOptions) (string, error) {
	gid := "none"
	for k, v := range options.Parameters {
		switch strings.ToLower(k) {
		case "gid":
			if strings.ToLower(v) == "none" {
				gid = "none"
			} else if i, err := strconv.ParseUint(v, 10, 64); err == nil && i != 0 {
				gid = v
			} else {
				return "", fmt.Errorf("invalid value for parameter gid: %v. valid values are: 'none' or a non-zero integer", v)
			}
		default:
			return "", fmt.Errorf("invalid parameter: %q", k)
		}
	}

	// TODO implement options.ProvisionerSelector parsing
	// pv.Labels MUST be set to match claim.spec.selector
	// gid selector? with or without pv annotation?
	if options.PVC.Spec.Selector != nil {
		return "", fmt.Errorf("claim.Spec.Selector is not supported")
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(p.exportDir, &stat); err != nil {
		return "", fmt.Errorf("error calling statfs on %v: %v", p.exportDir, err)
	}
	capacity := options.PVC.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)]
	requestBytes := capacity.Value()
	available := int64(stat.Bavail) * int64(stat.Bsize)
	if requestBytes > available {
		return "", fmt.Errorf("insufficient available space %v bytes to satisfy claim for %v bytes", available, requestBytes)
	}

	return gid, nil
}

// getServer gets the server IP to put in a provisioned PV's spec.
func (p *nfsProvisioner) getServer() (string, error) {
	// Use either `hostname -i` or podIPEnv as the fallback server
	var fallbackServer string
	podIP := os.Getenv(p.podIPEnv)
	if podIP == "" {
		out, err := exec.Command("hostname", "-i").Output()
		if err != nil {
			return "", fmt.Errorf("hostname -i failed with error: %v, output: %s", err, out)
		}
		fallbackServer = string(out)
	} else {
		fallbackServer = podIP
	}

	// Try to use the service's cluster IP as the server if serviceEnv is
	// specified. If not, try to use nodeName if nodeEnv is specified (assume the
	// pod is using hostPort). If not again, use fallback here.
	serviceName := os.Getenv(p.serviceEnv)
	if serviceName == "" {
		nodeName := os.Getenv(p.nodeEnv)
		if nodeName == "" {
			glog.Infof("service env %s isn't set and neither is node env %s, using `hostname -i`/pod IP %s as NFS server IP", p.serviceEnv, p.nodeEnv, fallbackServer)
			return fallbackServer, nil
		}
		glog.Infof("service env %s isn't set and node env %s is, using node name %s as NFS server IP", p.serviceEnv, p.nodeEnv, nodeName)
		return nodeName, nil
	}

	// From this point forward, rather than fallback & provision non-persistent
	// where persistent is expected, just return an error.
	namespace := os.Getenv(p.namespaceEnv)
	if namespace == "" {
		return "", fmt.Errorf("service env %s is set but namespace env %s isn't; no way to get the service cluster IP", p.serviceEnv, p.namespaceEnv)
	}
	service, err := p.client.Core().Services(namespace).Get(serviceName)
	if err != nil {
		return "", fmt.Errorf("error getting service %s=%s in namespace %s=%s", p.serviceEnv, serviceName, p.namespaceEnv, namespace)
	}

	// Do some validation of the service before provisioning useless volumes
	valid := false
	type endpointPort struct {
		port     int32
		protocol v1.Protocol
	}
	expectedPorts := map[endpointPort]bool{
		endpointPort{2049, v1.ProtocolTCP}:  true,
		endpointPort{20048, v1.ProtocolTCP}: true,
		endpointPort{111, v1.ProtocolUDP}:   true,
		endpointPort{111, v1.ProtocolTCP}:   true,
	}
	endpoints, err := p.client.Core().Endpoints(namespace).Get(serviceName)
	for _, subset := range endpoints.Subsets {
		if len(subset.Addresses) != 1 {
			continue
		}
		if subset.Addresses[0].IP != fallbackServer {
			continue
		}
		actualPorts := make(map[endpointPort]bool)
		for _, port := range subset.Ports {
			actualPorts[endpointPort{port.Port, port.Protocol}] = true
		}
		if !reflect.DeepEqual(expectedPorts, actualPorts) {
			continue
		}
		valid = true
		break
	}
	if !valid {
		return "", fmt.Errorf("service %s=%s is not valid; check that it has for ports %v one endpoint, this pod's IP %v", p.serviceEnv, serviceName, expectedPorts, fallbackServer)
	}
	if service.Spec.ClusterIP == v1.ClusterIPNone {
		return "", fmt.Errorf("service %s=%s is valid but it doesn't have a cluster IP", p.serviceEnv, serviceName)
	}

	return service.Spec.ClusterIP, nil
}

// createDirectory creates the given directory in exportDir with appropriate
// permissions and ownership according to the given gid parameter string.
func (p *nfsProvisioner) createDirectory(directory, gid string) error {
	// TODO quotas
	path := path.Join(p.exportDir, directory)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return fmt.Errorf("the path already exists")
	}

	perm := os.FileMode(0777)
	if gid != "none" {
		// Execute permission is required for stat, which kubelet uses during unmount.
		perm = os.FileMode(0071)
	}
	if err := os.MkdirAll(path, perm); err != nil {
		return err
	}
	// Due to umask, need to chmod
	cmd := exec.Command("chmod", strconv.FormatInt(int64(perm), 8), path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(path)
		return fmt.Errorf("chmod failed with error: %v, output: %s", err, out)
	}

	if gid != "none" {
		groupId, _ := strconv.ParseUint(gid, 10, 64)
		cmd = exec.Command("chgrp", strconv.FormatUint(groupId, 10), path)
		out, err = cmd.CombinedOutput()
		if err != nil {
			os.RemoveAll(path)
			return fmt.Errorf("chgrp failed with error: %v, output: %s", err, out)
		}
	}

	return nil
}

// createExport creates the export by adding a block to the appropriate config
// file and exporting it
func (p *nfsProvisioner) createExport(directory string) (string, uint16, error) {
	path := path.Join(p.exportDir, directory)

	block, exportId, err := p.exporter.AddExportBlock(path)
	if err != nil {
		return "", 0, fmt.Errorf("error adding export block for path %s: %v", path, err)
	}

	err = p.exporter.Export(path)
	if err != nil {
		p.exporter.RemoveExportBlock(block, exportId)
		return "", 0, fmt.Errorf("error exporting export block %s: %v", block, err)
	}

	return block, exportId, nil
}

// createQuota creates a quota for the directory by adding a project to
// represent the directory and setting a quota on it
func (p *nfsProvisioner) createQuota(directory string, capacity resource.Quantity) (string, uint16, error) {
	path := path.Join(p.exportDir, directory)

	limit := strconv.FormatInt(capacity.Value(), 10)

	block, projectId, err := p.quotaer.AddProject(path, limit)
	if err != nil {
		return "", 0, fmt.Errorf("error adding project for path %s: %v", path, err)
	}

	err = p.quotaer.SetQuota(projectId, path, limit)
	if err != nil {
		p.quotaer.RemoveProject(block, projectId)
		return "", 0, fmt.Errorf("error setting quota for path %s: %v", path, err)
	}

	return block, projectId, nil
}
