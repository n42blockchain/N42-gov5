package types

import chain "github.com/n42blockchain/N42/common/types"

// ModelManifest is an identity claim, not proof of model correctness.
type ModelManifest struct {
	Version             uint32     `json:"version"`
	ModelID             string     `json:"model_id"`
	Family              string     `json:"family"`
	ModelVersion        string     `json:"model_version"`
	WeightsHash         chain.Hash `json:"weights_hash"`
	TokenizerHash       chain.Hash `json:"tokenizer_hash"`
	SupportedTasks      []string   `json:"supported_tasks"`
	SupportedSchemas    []string   `json:"supported_schemas"`
	Precision           string     `json:"precision"`
	ContextLimit        uint32     `json:"context_limit"`
	CalibrationManifest chain.Hash `json:"calibration_manifest"`
	HardwareProfile     string     `json:"hardware_profile"`
	Provider            string     `json:"provider"`
	Timestamp           uint64     `json:"timestamp"`
	Signature           string     `json:"signature"`
}
