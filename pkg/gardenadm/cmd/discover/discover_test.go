// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package discover_test

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/gardener/gardener/pkg/gardenadm/cmd"
	. "github.com/gardener/gardener/pkg/gardenadm/cmd/discover"
)

var _ = Describe("Discover", func() {
	Describe("#NewCommand", func() {
		It("should expose 'new' and 'existing' subcommands", func() {
			cmd := NewCommand(&cmd.Options{})

			subs := make(map[string]bool, len(cmd.Commands()))
			for _, sub := range cmd.Commands() {
				subs[sub.Name()] = true
			}

			Expect(subs).To(HaveKey("new"))
			Expect(subs).To(HaveKey("existing"))
		})

		Context("for new Shoot", func() {
			It("should return the expected output", func() {
				Expect(command.Flags().Set("shoot-manifest", shootManifestPath)).To(Succeed())
				Expect(command.Flags().Set("kubeconfig", "some-path-to-kubeconfig")).To(Succeed())
				Expect(command.RunE(command, nil)).To(Succeed())

				expectCommonExports()
			})
		})

		Context("for already existing Shoot", func() {
			var (
				backupBucket *gardencorev1beta1.BackupBucket
				backupEntry  *gardencorev1beta1.BackupEntry
			)

			BeforeEach(func() {
				backupBucket = &gardencorev1beta1.BackupBucket{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-backup-bucket",
					},
					Spec: gardencorev1beta1.BackupBucketSpec{
						ShootRef: &corev1.ObjectReference{
							APIVersion: "core.gardener.cloud/v1beta1",
							Kind:       "Shoot",
							Name:       shoot.Name,
							Namespace:  shoot.Namespace,
						},
					},
				}
				backupEntry = &gardencorev1beta1.BackupEntry{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-backup-entry",
						Namespace: namespaceName,
					},
					Spec: gardencorev1beta1.BackupEntrySpec{
						BucketName: backupBucket.Name,
						ShootRef: &corev1.ObjectReference{
							APIVersion: "core.gardener.cloud/v1beta1",
							Kind:       "Shoot",
							Name:       shoot.Name,
							Namespace:  shoot.Namespace,
						},
					},
				}
			})

			It("should fail when the shoot does not exists", func() {
				Expect(command.Flags().Set("shoot-name", shoot.Name)).To(Succeed())
				Expect(command.Flags().Set("shoot-namespace", shoot.Namespace)).To(Succeed())
				Expect(command.Flags().Set("kubeconfig", "some-path-to-kubeconfig")).To(Succeed())
				Expect(command.Flags().Set("config-dir", ".")).To(Succeed())
				Expect(command.RunE(command, nil)).To(MatchError(ContainSubstring("failed getting Shoot garden-test-project/test-shoot from garden cluster")))

			})

			It("should return the expected output when the shoot exists", func() {
				Expect(fakeClient.Create(ctx, shoot)).To(Succeed())
				Expect(fakeClient.Create(ctx, backupBucket)).To(Succeed())
				Expect(fakeClient.Create(ctx, backupEntry)).To(Succeed())

				Expect(command.Flags().Set("shoot-name", shoot.Name)).To(Succeed())
				Expect(command.Flags().Set("shoot-namespace", shoot.Namespace)).To(Succeed())
				Expect(command.Flags().Set("kubeconfig", "some-path-to-kubeconfig")).To(Succeed())
				Expect(command.Flags().Set("config-dir", ".")).To(Succeed())
				Expect(command.RunE(command, nil)).To(Succeed())

				expectCommonExports()

				Eventually(func() string { return string(stdOut.Contents()) }).Should(SatisfyAll(
					ContainSubstring("Exported BackupBucket/"+backupBucket.Name),
					ContainSubstring("Exported BackupEntry/"+backupEntry.Name),
				))

				for _, path := range []string{
					fmt.Sprintf("backupbucket-%s.yaml", backupBucket.Name),
					fmt.Sprintf("backupentry-%s.yaml", backupEntry.Name),
				} {
					exists, err := fs.Exists(path)
					Expect(err).NotTo(HaveOccurred(), "for path "+path)
					Expect(exists).To(BeTrue(), "for path "+path)
				}
			})
		})
	})
})
