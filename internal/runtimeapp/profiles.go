package runtimeapp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/mss-boot-io/mss-knowledge/internal/config"
	"github.com/mss-boot-io/mss-knowledge/internal/ports"
)

// ProcessingProfiles converts validated process configuration into immutable
// parser, chunker, embedding, and index profile values shared by all binaries.
func ProcessingProfiles(cfg config.Config) (ports.ProcessingProfileIDs, ports.ChunkProfile, ports.EmbeddingProfile) {
	ids := ports.ProcessingProfileIDs{
		Parser:    cfg.Processing.ParserProfileID,
		Chunker:   cfg.Processing.ChunkerProfileID,
		Embedding: cfg.Processing.EmbeddingProfileID,
		Index:     cfg.Processing.IndexProfileID,
	}
	chunk := ports.ChunkProfile{
		Name:           "structural",
		Version:        cfg.Processing.ChunkerProfileID,
		TargetTokens:   cfg.Processing.ChunkTargetTokens,
		MinimumTokens:  cfg.Processing.ChunkMinimumTokens,
		MaximumTokens:  cfg.Processing.ChunkMaximumTokens,
		OverlapTokens:  cfg.Processing.ChunkOverlapTokens,
		PreserveCode:   true,
		PreserveTables: true,
	}
	fingerprintMaterial := strings.Join([]string{
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		fmt.Sprintf("dimension=%d", cfg.Embedding.Dimension),
		"vector=FLOAT32",
		"normalize=true",
	}, "\x00")
	digest := sha256.Sum256([]byte(fingerprintMaterial))
	embedding := ports.EmbeddingProfile{
		Provider:      cfg.Embedding.Provider,
		ModelID:       cfg.Embedding.Model,
		ModelRevision: "1",
		Dimension:     cfg.Embedding.Dimension,
		VectorType:    "FLOAT32",
		Normalize:     true,
		BatchSize:     64,
		Fingerprint:   hex.EncodeToString(digest[:]),
	}
	return ids, chunk, embedding
}
