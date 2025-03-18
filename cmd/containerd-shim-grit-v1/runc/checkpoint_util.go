package runc

import (
	"encoding/json"
	"os"
	"path"

	crmetadata "github.com/checkpoint-restore/checkpointctl/lib"
)

type CheckpointOpts struct {
	// $CheckpointBaseDir/
	// ├── checkpoint/
	// │   ├── pages-1.img
	// │   └── ...
	// ├── rootfs-diff.tar
	// ├── config.dump
	// └── spec.dump
	CheckpointBaseDir string
}

func (c *CheckpointOpts) GetCheckpointPath() string {
	return path.Join(c.CheckpointBaseDir, crmetadata.CheckpointDirectory)
}

func (c *CheckpointOpts) GetRootFsDiffTar() string {
	return path.Join(c.CheckpointBaseDir, crmetadata.RootFsDiffTar)
}

const (
	AnnotationGRITCheckpoint = "grit.dev/checkpoint"
	AnnotationContainerType  = "io.kubernetes.cri.container-type"
	AnnotationContainerName  = "io.kubernetes.cri.container-name"
)

// spec is a shallow version of [oci.Spec] containing only the
// fields we need. We use a shallow struct to reduce
// the overhead of unmarshalling.
type spec struct {
	// Annotations contains arbitrary metadata for the container.
	Annotations map[string]string `json:"annotations,omitempty"`
}

func readCRSpec(bundle string) (*spec, error) {
	configFileName := path.Join(bundle, "config.json")
	f, err := os.Open(configFileName)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var s spec
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ReadCheckpointOpts reads the checkpoint options from the container oci spec.
func ReadCheckpointOpts(bundle string) (*CheckpointOpts, error) {
	s, err := readCRSpec(bundle)
	if err != nil {
		return nil, err
	}

	containType := s.Annotations[AnnotationContainerType]
	if containType != "container" {
		return nil, nil
	}

	checkpointPath := s.Annotations[AnnotationGRITCheckpoint]
	if checkpointPath == "" {
		return nil, nil
	}
	containerName := s.Annotations[AnnotationContainerName]
	return &CheckpointOpts{
		CheckpointBaseDir: path.Join(checkpointPath, containerName),
	}, nil
}
