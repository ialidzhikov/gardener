// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
//
// SPDX-License-Identifier: Apache-2.0

package discover_test

import (
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

		It("should also export backup bucket and backup entry when they exist for the shoot", func() {
			backupBucket := &gardencorev1beta1.BackupBucket{
				ObjectMeta: metav1.ObjectMeta{Name: "test-backup-bucket"},
			}
			coreBackupEntry := &gardencorev1beta1.BackupEntry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-backup-entry",
					Namespace: namespaceName,
				},
				Spec: gardencorev1beta1.BackupEntrySpec{
					BucketName: backupBucket.Name,
					ShootRef: &corev1.ObjectReference{
						Name:      shoot.Name,
						Namespace: shoot.Namespace,
					},
				},
			}

			Expect(fakeClient.Create(ctx, backupBucket)).To(Succeed())
			Expect(fakeClient.Create(ctx, coreBackupEntry)).To(Succeed())

			Expect(command.Flags().Set("kubeconfig", "some-path-to-kubeconfig")).To(Succeed())
			Expect(command.RunE(command, []string{shootManifestPath})).To(Succeed())

			Eventually(func() string { return string(stdOut.Contents()) }).Should(SatisfyAll(
				ContainSubstring("Exported BackupBucket/"+backupBucket.Name),
				ContainSubstring("Exported BackupEntry/"+coreBackupEntry.Name),
			))

			for _, path := range []string{
				fmt.Sprintf("backupbucket-%s.yaml", backupBucket.Name),
				fmt.Sprintf("backupentry-%s.yaml", coreBackupEntry.Name),
			} {
				exists, err := fs.Exists(path)
				Expect(err).NotTo(HaveOccurred(), "for path "+path)
				Expect(exists).To(BeTrue(), "for path "+path)
			}
		})
	})
})
