package runc

import (
	"encoding/json"
	"os"
	"path"
)

// Directory structure of a checkpoint:
// .
// ├── checkpoint      // checkpoint directory
// ├── config.dump
// ├── rootfs-diff.tar // rw layer
// └── spec.dump
type CheckpointOpts struct {
	// Checkpoint digest to restore container state
	Checkpoint string
}

const (
	AnnotationGRITCheckpoint = "grit.dev/checkpoint"
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

	checkpointPath := s.Annotations[AnnotationGRITCheckpoint]
	if checkpointPath == "" {
		return nil, nil
	}
	return &CheckpointOpts{
		Checkpoint: checkpointPath,
	}, nil
}
