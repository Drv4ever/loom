package loom

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestChunkManifestAndDeltaReconstruction(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "large.bin")

	// Create a 3.5 MiB file with deterministic data
	chunkSize := int64(1 << 20) // 1 MiB
	totalSize := int64(3.5 * float64(chunkSize))
	content := make([]byte, totalSize)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}

	manifest, err := GenerateChunkManifest(sourcePath, chunkSize)
	if err != nil {
		t.Fatalf("GenerateChunkManifest: %v", err)
	}
	if len(manifest.Chunks) != 4 {
		t.Fatalf("expected 4 chunks for 3.5 MiB file, got %d", len(manifest.Chunks))
	}

	// 1. Existing file identical -> 0 needed chunks
	needed, err := FindNeededChunks(sourcePath, manifest)
	if err != nil {
		t.Fatalf("FindNeededChunks identical: %v", err)
	}
	if len(needed) != 0 {
		t.Fatalf("expected 0 needed chunks for identical file, got %v", needed)
	}

	// 2. Modify 10 bytes in chunk 2 (between 2MB and 3MB)
	destPath := filepath.Join(dir, "dest.bin")
	if err := os.WriteFile(destPath, content, 0600); err != nil {
		t.Fatal(err)
	}

	// Make edit in sourcePath at offset 2.5 MiB
	editOffset := int64(2.5 * float64(chunkSize))
	for i := int64(0); i < 10; i++ {
		content[editOffset+i] = 0xFF
	}
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}

	newManifest, err := GenerateChunkManifest(sourcePath, chunkSize)
	if err != nil {
		t.Fatalf("GenerateChunkManifest after edit: %v", err)
	}

	// Find needed chunks against destination
	needed, err = FindNeededChunks(destPath, newManifest)
	if err != nil {
		t.Fatalf("FindNeededChunks after 10-byte edit: %v", err)
	}

	// Exactly 1 chunk (chunk index 2) should be needed!
	if len(needed) != 1 || needed[0] != 2 {
		t.Fatalf("expected exactly [2] needed, got %v", needed)
	}

	// Assemble patch
	assembledPath := filepath.Join(dir, "assembled.bin")
	// Delta map providing only the needed chunk
	chunk2Offset := newManifest.Chunks[2].Offset
	chunk2Size := newManifest.Chunks[2].Size
	chunk2Data := content[chunk2Offset : chunk2Offset+chunk2Size]

	err = AssemblePatchedFile(assembledPath, destPath, newManifest, func(idx int) ([]byte, error) {
		if idx == 2 {
			return chunk2Data, nil
		}
		return nil, errors.New("chunk not transmitted")
	})
	if err != nil {
		t.Fatalf("AssemblePatchedFile: %v", err)
	}

	assembledData, err := os.ReadFile(assembledPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(assembledData, content) {
		t.Fatalf("assembled file does not match modified content")
	}
}
