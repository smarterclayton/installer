package cluster

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"

	"github.com/openshift/installer/pkg/asset"
	"github.com/openshift/installer/pkg/asset/cluster/aws"
	"github.com/openshift/installer/pkg/asset/cluster/libvirt"
	"github.com/openshift/installer/pkg/asset/cluster/openstack"
	"github.com/openshift/installer/pkg/asset/installconfig"
	"github.com/openshift/installer/pkg/asset/password"
	"github.com/openshift/installer/pkg/terraform"
	"github.com/openshift/installer/pkg/types"
)

const (
	// metadataFileName is name of the file where clustermetadata is stored.
	metadataFileName = "metadata.json"
)

var (
	// kubeadminPasswordPath is the path where kubeadmin user password is stored.
	kubeadminPasswordPath = filepath.Join("auth", "kubeadmin-password")
)

// Cluster uses the terraform executable to launch a cluster
// with the given terraform tfvar and generated templates.
type Cluster struct {
	FileList []*asset.File
}

var _ asset.WritableAsset = (*Cluster)(nil)

// Name returns the human-friendly name of the asset.
func (c *Cluster) Name() string {
	return "Cluster"
}

// Dependencies returns the direct dependency for launching
// the cluster.
func (c *Cluster) Dependencies() []asset.Asset {
	return []asset.Asset{
		&installconfig.InstallConfig{},
		&TerraformVariables{},
		&password.KubeadminPassword{},
	}
}

// Generate launches the cluster and generates the terraform state file on disk.
func (c *Cluster) Generate(parents asset.Parents) (err error) {
	installConfig := &installconfig.InstallConfig{}
	terraformVariables := &TerraformVariables{}
	kubeadminPassword := &password.KubeadminPassword{}
	parents.Get(installConfig, terraformVariables, kubeadminPassword)

	if installConfig.Config.Platform.None != nil {
		return errors.New("cluster cannot be created with platform set to 'none'")
	}

	// Copy the terraform.tfvars to a temp directory where the terraform will be invoked within.
	tmpDir, err := ioutil.TempDir("", "openshift-install-")
	if err != nil {
		return errors.Wrap(err, "failed to create temp dir for terraform execution")
	}
	defer os.RemoveAll(tmpDir)

	terraformVariablesFile := terraformVariables.Files()[0]
	if err := ioutil.WriteFile(filepath.Join(tmpDir, terraformVariablesFile.Filename), terraformVariablesFile.Data, 0600); err != nil {
		return errors.Wrap(err, "failed to write terraform.tfvars file")
	}

	metadata := &types.ClusterMetadata{
		ClusterName: installConfig.Config.ObjectMeta.Name,
	}

	defer func() {
		if data, err2 := json.Marshal(metadata); err2 == nil {
			c.FileList = append(c.FileList, &asset.File{
				Filename: metadataFileName,
				Data:     data,
			})
		} else {
			err2 = errors.Wrap(err2, "failed to Marshal ClusterMetadata")
			if err == nil {
				err = err2
			} else {
				logrus.Error(err2)
			}
		}
		c.FileList = append(c.FileList, &asset.File{
			Filename: kubeadminPasswordPath,
			Data:     []byte(kubeadminPassword.Password),
		})
		// serialize metadata and stuff it into c.FileList
	}()

	switch {
	case installConfig.Config.Platform.AWS != nil:
		metadata.ClusterPlatformMetadata.AWS = aws.Metadata(installConfig.Config)
	case installConfig.Config.Platform.Libvirt != nil:
		metadata.ClusterPlatformMetadata.Libvirt = libvirt.Metadata(installConfig.Config)
	case installConfig.Config.Platform.OpenStack != nil:
		metadata.ClusterPlatformMetadata.OpenStack = openstack.Metadata(installConfig.Config)
	default:
		return fmt.Errorf("no known platform")
	}

	logrus.Infof("Creating cluster...")
	stateFile, err := terraform.Apply(tmpDir, installConfig.Config.Platform.Name())
	if err != nil {
		err = errors.Wrap(err, "failed to create cluster")
	}

	data, err2 := ioutil.ReadFile(stateFile)
	if err2 == nil {
		c.FileList = append(c.FileList, &asset.File{
			Filename: terraform.StateFileName,
			Data:     data,
		})
	} else {
		if err == nil {
			err = err2
		} else {
			logrus.Errorf("Failed to read tfstate: %v", err2)
		}
	}

	return err
}

// Files returns the FileList generated by the asset.
func (c *Cluster) Files() []*asset.File {
	return c.FileList
}

// Load returns error if the tfstate file is already on-disk, because we want to
// prevent user from accidentally re-launching the cluster.
func (c *Cluster) Load(f asset.FileFetcher) (found bool, err error) {
	_, err = f.FetchByName(terraform.StateFileName)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	return true, fmt.Errorf("%q already exists.  There may already be a running cluster", terraform.StateFileName)
}

// LoadMetadata loads the cluster metadata from an asset directory.
func LoadMetadata(dir string) (cmetadata *types.ClusterMetadata, err error) {
	raw, err := ioutil.ReadFile(filepath.Join(dir, metadataFileName))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read %s file", metadataFileName)
	}

	if err = json.Unmarshal(raw, &cmetadata); err != nil {
		return nil, errors.Wrapf(err, "failed to Unmarshal data from %s file to types.ClusterMetadata", metadataFileName)
	}

	return cmetadata, err
}
