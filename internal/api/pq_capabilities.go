package api

// GetPQCapabilities lets wallets distinguish a configured Falcon transaction
// network from a legacy endpoint. No transaction or signing material is sent.
func (s *ConsensusAPI) GetPQCapabilities() map[string]interface{} {
	out := map[string]interface{}{"protocol": "n42-pq-v1", "transactionType": "0x05", "algorithm": "Falcon-512",
		"signatureEncoding": "padded-666", "publicKeyMode": 0, "enabled": false}
	if s == nil || s.api == nil || s.api.GetChainConfig() == nil {
		return out
	}
	cfg := s.api.GetChainConfig()
	if cfg.ChainID != nil {
		out["chainId"] = "0x" + cfg.ChainID.Text(16)
	}
	if s.api.BlockChain() == nil {
		return out
	}
	head := s.api.BlockChain().CurrentBlock()
	if head != nil && head.Number64() != nil {
		out["enabled"] = cfg.IsPQTx(head.Number64().ToBig())
	}
	return out
}
