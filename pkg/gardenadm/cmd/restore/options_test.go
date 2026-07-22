// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package restore_test

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gardener/gardener/pkg/gardenadm/cmd"
	. "github.com/gardener/gardener/pkg/gardenadm/cmd/restore"
)

var _ = Describe("Options", func() {
	var (
		options   *Options
		configDir string
	)

	BeforeEach(func() {
		var err error
		configDir, err = os.MkdirTemp("", "gardenadm-test-*")
		Expect(err).NotTo(HaveOccurred())

		options = &Options{
			Options: &cmd.Options{},
		}
		options.ConfigDir = configDir

		cloudProfileManifest := `apiVersion: core.gardener.cloud/v1beta1
kind: CloudProfile
metadata:
  name: local
spec:
  type: local
`
		Expect(os.WriteFile(filepath.Join(configDir, "cloudprofile.yaml"), []byte(cloudProfileManifest), 0644)).To(Succeed())

		projectManifest := `apiVersion: core.gardener.cloud/v1beta1
kind: Project
metadata:
  name: test-project
spec:
  namespace: garden-test
`
		Expect(os.WriteFile(filepath.Join(configDir, "project.yaml"), []byte(projectManifest), 0644)).To(Succeed())

		DeferCleanup(func() {
			if configDir != "" {
				Expect(os.RemoveAll(configDir)).To(Succeed())
			}
		})
	})

	createShootManifest := func(credentialsBindingName string, zones []string, isControlPlane bool, statusUID string) {
		var shootManifest strings.Builder
		shootManifest.WriteString(`apiVersion: core.gardener.cloud/v1beta1
kind: Shoot
metadata:
  name: test-shoot
  namespace: garden-test
spec:`)
		if credentialsBindingName != "" {
			shootManifest.WriteString(`
  credentialsBindingName: ` + credentialsBindingName)
		}
		shootManifest.WriteString(`
  provider:
    type: local
    workers:
    - name: control-plane
      minimum: 1
      maximum: 1`)
		if isControlPlane {
			shootManifest.WriteString(`
      controlPlane:
        highAvailability: {}`)
		}
		if len(zones) > 0 {
			shootManifest.WriteString(`
      zones:`)
			for _, zone := range zones {
				shootManifest.WriteString(`
      - ` + zone)
			}
		}
		shootManifest.WriteString("\n")
		if statusUID != "" {
			shootManifest.WriteString(`status:
  uid: ` + statusUID + `
`)
		}

		Expect(os.WriteFile(filepath.Join(configDir, "shoot.yaml"), []byte(shootManifest.String()), 0644)).To(Succeed())
	}

	createShootStateManifest := func() {
		shootStateManifest := `apiVersion: core.gardener.cloud/v1beta1
kind: ShootState
metadata:
  name: test-shoot
  namespace: garden-test
spec:
  gardener:
  extensions:`
		Expect(os.WriteFile(filepath.Join(configDir, "shootstate.yaml"), []byte(shootStateManifest), 0644)).To(Succeed())
	}

	createBackupBucketManifest := func(name string) {
		manifest := `apiVersion: core.gardener.cloud/v1beta1
kind: BackupBucket
metadata:
  name: ` + name + `
spec:
  provider:
    type: local
    region: local
`
		Expect(os.WriteFile(filepath.Join(configDir, "backupbucket.yaml"), []byte(manifest), 0644)).To(Succeed())
	}

	createBackupEntryManifest := func(name, bucketName string) {
		manifest := `apiVersion: core.gardener.cloud/v1beta1
kind: BackupEntry
metadata:
  name: ` + name + `
  namespace: garden-test
spec:
  bucketName: ` + bucketName + `
`
		Expect(os.WriteFile(filepath.Join(configDir, "backupentry.yaml"), []byte(manifest), 0644)).To(Succeed())
	}
	Describe("#ParseArgs", func() {
		It("should return nil", func() {
			Expect(options.ParseArgs(nil)).To(Succeed())
		})
	})

	Describe("#Validate", func() {
		const (
			statusUID        = "abcd-1234-uid"
			backupBucketName = statusUID
			backupEntryName  = "kube-system--" + statusUID
		)

		BeforeEach(func() {
			createShootManifest("test-credentials", nil, true, statusUID)
			createShootStateManifest()
			createBackupBucketManifest(backupBucketName)
			createBackupEntryManifest(backupEntryName, backupBucketName)

			options.BackupDataPath = "/some/path/to/backup"
			options.PriorNodeName = "node-01"
		})

		It("should pass for valid options", func() {
			Expect(options.Validate()).To(Succeed())
		})

		It("should fail when --backup-data-path is not set", func() {
			options.BackupDataPath = ""

			Expect(options.Validate()).To(MatchError(ContainSubstring("must provide --backup-data-path")))
		})

		It("should fail when --prior-node-name is not set", func() {
			options.PriorNodeName = ""

			Expect(options.Validate()).To(MatchError(ContainSubstring("must provide --prior-node-name")))
		})

		It("should fail when config directory does not exist", func() {
			options.ConfigDir = "non-existent-directory"

			Expect(options.Validate()).To(MatchError(ContainSubstring("failed loading resources for gardenadm restore validation")))
		})

		It("should fail when ShootState manifest is missing", func() {
			Expect(os.Remove(filepath.Join(configDir, "shootstate.yaml"))).To(Succeed())

			Expect(options.Validate()).To(MatchError(ContainSubstring("gardenadm restore requires a ShootState resource in the config directory, but none was found")))
		})

		It("should fail when Shoot .status.uid is empty", func() {
			createShootManifest("test-credentials", nil, true, "")

			Expect(options.Validate()).To(MatchError(ContainSubstring("gardenadm restore requires the Shoot manifest in the config directory to have .status.uid set")))
		})

		Context("BackupBucket/BackupEntry resources validation", func() {
			It("should fail when both BackupBucket and BackupEntry manifests are missing", func() {
				Expect(os.Remove(filepath.Join(configDir, "backupbucket.yaml"))).To(Succeed())
				Expect(os.Remove(filepath.Join(configDir, "backupentry.yaml"))).To(Succeed())

				Expect(options.Validate()).To(MatchError(ContainSubstring("gardenadm restore requires both BackupBucket and BackupEntry manifests in the config directory when backup is configured for the Shoot, but neither was found")))
			})

			It("should fail when only the BackupBucket manifest is missing", func() {
				Expect(os.Remove(filepath.Join(configDir, "backupbucket.yaml"))).To(Succeed())

				Expect(options.Validate()).To(MatchError(ContainSubstring("gardenadm restore requires a BackupBucket manifest in the config directory when backup is configured for the Shoot, but none was found")))
			})

			It("should fail when only the BackupEntry manifest is missing", func() {
				Expect(os.Remove(filepath.Join(configDir, "backupentry.yaml"))).To(Succeed())

				Expect(options.Validate()).To(MatchError(ContainSubstring("gardenadm restore requires a BackupEntry manifest in the config directory when backup is configured for the Shoot, but none was found")))
			})

			It("should fail on mismatching BackupBucket name", func() {
				createBackupBucketManifest("wrong-name")
				createBackupEntryManifest(backupEntryName, backupBucketName)

				Expect(options.Validate()).To(MatchError(
					ContainSubstring(`BackupBucket manifest name "wrong-name" does not match the expected name %q (= Shoot .status.uid)`, backupBucketName),
				))
			})

			It("should fail on mismatching BackupEntry name", func() {
				createBackupBucketManifest(backupBucketName)
				createBackupEntryManifest("wrong-name", backupBucketName)

				Expect(options.Validate()).To(MatchError(
					ContainSubstring(`BackupEntry manifest name "wrong-name" does not match the expected name %q (= <controlPlaneNamespace>--<Shoot .status.uid>)`, backupEntryName),
				))
			})

			It("should fail on mismatching BackupEntry .spec.bucketName", func() {
				createBackupBucketManifest(backupBucketName)
				createBackupEntryManifest(backupEntryName, "wrong-bucket")

				Expect(options.Validate()).To(MatchError(ContainSubstring(`BackupEntry manifest .spec.bucketName "wrong-bucket" does not match the BackupBucket manifest name`)))
			})
		})

		When("zone validation with managed infrastructure", func() {
			BeforeEach(func() {
				createShootManifest("test-credentials", nil, true, statusUID)
			})

			It("should reject zone when provided for managed infrastructure", func() {
				options.Zone = "us-east-1a"

				Expect(options.Validate()).To(MatchError(ContainSubstring("zone can't be configured for shoot with managed infrastructure")))
			})

			It("should allow empty zone for managed infrastructure", func() {
				options.Zone = ""

				Expect(options.Validate()).To(Succeed())
				Expect(options.Zone).To(BeEmpty())
			})
		})

		When("zone validation with unmanaged infrastructure", func() {
			When("worker with no zones configured", func() {
				BeforeEach(func() {
					createShootManifest("", nil, true, statusUID)
				})

				It("should reject zone when worker has no zones configured", func() {
					options.Zone = "custom-zone"

					Expect(options.Validate()).To(MatchError(ContainSubstring(`worker "control-plane" has no zones configured, but zone "custom-zone" was provided`)))
				})

				It("should allow empty zone when worker has no zones", func() {
					options.Zone = ""

					Expect(options.Validate()).To(Succeed())
					Expect(options.Zone).To(BeEmpty())
				})
			})

			When("worker with single zone configured", func() {
				BeforeEach(func() {
					createShootManifest("", []string{"zone-1"}, true, statusUID)
				})

				It("should auto-apply the single zone when not provided", func() {
					options.Zone = ""

					Expect(options.Validate()).To(Succeed())
					Expect(options.Zone).To(Equal("zone-1"))
				})

				It("should accept matching zone when provided", func() {
					options.Zone = "zone-1"

					Expect(options.Validate()).To(Succeed())
					Expect(options.Zone).To(Equal("zone-1"))
				})

				It("should reject non-matching zone when provided", func() {
					options.Zone = "zone-2"

					Expect(options.Validate()).To(MatchError(ContainSubstring(`provided zone "zone-2" does not match the configured zones [zone-1] for worker "control-plane"`)))
				})
			})

			When("worker with multiple zones configured", func() {
				BeforeEach(func() {
					createShootManifest("", []string{"zone-1", "zone-2", "zone-3"}, true, statusUID)
				})

				It("should require zone flag when not provided", func() {
					options.Zone = ""

					Expect(options.Validate()).To(MatchError(ContainSubstring(`worker "control-plane" has multiple zones configured [zone-1 zone-2 zone-3], --zone flag is required`)))
				})

				It("should accept valid zone when provided", func() {
					options.Zone = "zone-2"

					Expect(options.Validate()).To(Succeed())
					Expect(options.Zone).To(Equal("zone-2"))
				})

				It("should reject invalid zone when provided", func() {
					options.Zone = "zone-4"

					Expect(options.Validate()).To(MatchError(ContainSubstring(`provided zone "zone-4" does not match the configured zones [zone-1 zone-2 zone-3] for worker "control-plane"`)))
				})
			})
		})
	})

	Describe("#Complete", func() {
		It("should return nil", func() {
			Expect(options.Complete()).To(Succeed())
		})
	})
})
