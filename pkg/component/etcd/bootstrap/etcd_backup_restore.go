// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/gardener/gardener/pkg/component/etcd/etcd"
	staticpodtranslator "github.com/gardener/gardener/pkg/gardenadm/staticpod"
)

const (
	volumeNameBackupBuckets = "backup-buckets"
	volumeNameRestoreTmp    = "restoration-tmp"
	volumeNameEtcdConf      = "etcd-conf"
	etcdConfigFileName      = "etcd.conf.yaml"

	volumeMountPathBackupBuckets = "/root"
	volumeMountPathRestoreTmp    = "/tmp/restorationtmp"
	volumeMountPathEtcdConf      = "/var/etcd/config"
)

// EtcdBackupRestoreConfig contains configuration for running `etcdbrctl initialize` before starting the bootstrap etcd.
//
// The init container is only added when this config is not nil.
type EtcdBackupRestoreConfig struct {
	EtcdbrctlImage        string
	StoreContainer        string
	StorePrefix           string
	BackupBucketsHostPath string
}

func (e *etcdDeployer) shouldRunBackupRestore() bool {
	return e.values.BackupRestore != nil &&
		e.values.BackupRestore.BackupBucketsHostPath != "" &&
		e.values.BackupRestore.StoreContainer != ""
}

func (e *etcdDeployer) backupInitContainer() corev1.Container {
	cfg := *e.values.BackupRestore
	dataDir := staticpodtranslator.StatefulSetVolumeClaimTemplateHostPath(etcd.Name(e.values.Role))

	return corev1.Container{
		Name:            "etcdbrctl-initialize",
		Image:           cfg.EtcdbrctlImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                ptr.To[int64](0),
			RunAsGroup:               ptr.To[int64](0),
			AllowPrivilegeEscalation: ptr.To(false),
		},
		Args: []string{
			"initialize",
			"--storage-provider=Local",
			"--store-container=" + cfg.StoreContainer,
			"--store-prefix=" + cfg.StorePrefix,
			"--data-dir=" + filepath.Join(dataDir, "new.etcd"),
			"--restoration-temp-snapshots-dir=" + volumeMountPathRestoreTmp,
		},
		Env: []corev1.EnvVar{
			{Name: "POD_NAME", Value: "etcd-bootstrap-main"},
			{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: volumeNameBackupBuckets, MountPath: volumeMountPathBackupBuckets},
			{Name: volumeNameData, MountPath: staticpodtranslator.StatefulSetVolumeClaimTemplateHostPath(etcd.Name(e.values.Role))},
			{Name: volumeNameRestoreTmp, MountPath: volumeMountPathRestoreTmp},
			{Name: volumeNameEtcdConf, MountPath: volumeMountPathEtcdConf},
		},
	}
}

func (e *etcdDeployer) backupVolumes() []corev1.Volume {
	hostPath := e.values.BackupRestore.BackupBucketsHostPath

	return []corev1.Volume{
		{Name: volumeNameBackupBuckets, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: hostPath, Type: ptr.To(corev1.HostPathDirectoryOrCreate)}}},
		{Name: volumeNameRestoreTmp, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: volumeNameEtcdConf, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: e.etcdConfigMapName()}, Items: []corev1.KeyToPath{{Key: etcdConfigFileName, Path: etcdConfigFileName}}}}},
	}
}

func (e *etcdDeployer) etcdInitializeConfig() string {
	return `advertise-client-urls:
  etcd-bootstrap-main:
  - https://localhost:2379
auto-compaction-mode: periodic
auto-compaction-retention: 30m
client-transport-security:
  auto-tls: false
  cert-file: /var/etcd/ssl/server/tls.crt
  client-cert-auth: true
  key-file: /var/etcd/ssl/server/tls.key
  trusted-ca-file: /var/etcd/ssl/ca/bundle.crt
data-dir: /var/etcd/data/new.etcd
enable-v2: false
initial-advertise-peer-urls:
  etcd-bootstrap-main:
  - http://localhost:2380
initial-cluster: etcd-bootstrap-main=http://localhost:2380
initial-cluster-state: new
initial-cluster-token: etcd-cluster
listen-client-urls: https://0.0.0.0:2379
listen-peer-urls: http://0.0.0.0:2380
metrics: extensive
name: etcd-config
quota-backend-bytes: 8589934592
snapshot-count: 10000
`
}
