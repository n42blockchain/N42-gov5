package sharedexec

import (
	"crypto/sha256"
	"encoding/json"
	"errors"

	"github.com/n42blockchain/N42/common/types"
)

type ResumeSourceProof struct {
	Kind          string                `json:"kind"`
	Identity      Identity              `json:"identity"`
	Manifest      types.Hash            `json:"manifest_sha256"`
	CorpusID      types.Hash            `json:"corpus_id"`
	Genesis       types.Hash            `json:"genesis_file_sha256"`
	Shards        []CorpusShard         `json:"shards"`
	Witnesses     []ResumeSourceWitness `json:"witnesses"`
	MappingSHA256 types.Hash            `json:"mapping_sha256"`
}

// EncodeResumeSourceProof produces audit bytes, never an activation capability.
// The caller must have performed its own source scan in this process.
func EncodeResumeSourceProof(manifestRaw []byte, scan ResumeSourceScan) (ResumeSourceProof, []byte, error) {
	m, err := DecodeCorpusManifest(manifestRaw)
	if err != nil {
		return ResumeSourceProof{}, nil, err
	}
	mapping, err := ResumeMappingID(scan.Senders)
	if err != nil || mapping != scan.MappingSHA256 || len(scan.Witnesses) != int(m.Descriptor.SenderCount) {
		return ResumeSourceProof{}, nil, errors.New("resume source proof mapping differs")
	}
	proof := ResumeSourceProof{Kind: "gov5-resume-source-mapping-v1", Identity: m.Descriptor.Identity, Manifest: types.Hash(sha256.Sum256(manifestRaw)), CorpusID: m.CorpusID, Genesis: m.Descriptor.GenesisFileHash, Shards: m.Shards, Witnesses: scan.Witnesses, MappingSHA256: scan.MappingSHA256}
	raw, err := json.Marshal(proof)
	if err != nil {
		return ResumeSourceProof{}, nil, err
	}
	return proof, append(raw, '\n'), nil
}
