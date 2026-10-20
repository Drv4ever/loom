package loom

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// DefaultChunkSize is 1 MiB.
const DefaultChunkSize int64 = 1 << 20

// ChunkInfo describes one fixed-size chunk of a file.
type ChunkInfo struct {
	Index  int    `json:"index"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// FileChunkManifest holds chunk metadata for delta synchronization.
type FileChunkManifest struct {
	Path      string      `json:"path"`
	Size      int64       `json:"size"`
	SHA256    string      `json:"sha256"`
	ChunkSize int64       `json:"chunkSize"`
	Chunks    []ChunkInfo `json:"chunks"`
}

// GenerateChunkManifest reads a file and computes SHA-256 for each chunk and the whole file.
func GenerateChunkManifest(filePath string, chunkSize int64) (*FileChunkManifest, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file for chunking: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	fileSize := stat.Size()

	manifest := &FileChunkManifest{
		Path:      filePath,
		Size:      fileSize,
		ChunkSize: chunkSize,
		Chunks:    make([]ChunkInfo, 0, (fileSize/chunkSize)+1),
	}

	fullHasher := sha256.New()
	buf := make([]byte, chunkSize)
	var offset int64
	index := 0

	for offset < fileSize {
		toRead := chunkSize
		if offset+toRead > fileSize {
			toRead = fileSize - offset
		}

		n, err := io.ReadFull(f, buf[:toRead])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("read chunk %d: %w", index, err)
		}

		chunkData := buf[:n]
		fullHasher.Write(chunkData)

		chunkHash := sha256.Sum256(chunkData)
		manifest.Chunks = append(manifest.Chunks, ChunkInfo{
			Index:  index,
			Offset: offset,
			Size:   int64(n),
			SHA256: hex.EncodeToString(chunkHash[:]),
		})

		offset += int64(n)
		index++
	}

	manifest.SHA256 = hex.EncodeToString(fullHasher.Sum(nil))
	return manifest, nil
}

// FindNeededChunks compares an existing local file against a remote chunk manifest.
// Returns the list of chunk indices that differ or must be transferred.
func FindNeededChunks(existingFilePath string, remote *FileChunkManifest) ([]int, error) {
	if remote == nil {
		return nil, fmt.Errorf("remote manifest is nil")
	}

	// If existing file doesn't exist, all chunks are needed
	f, err := os.Open(existingFilePath)
	if os.IsNotExist(err) {
		needed := make([]int, len(remote.Chunks))
		for i := range remote.Chunks {
			needed[i] = i
		}
		return needed, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	existingSize := stat.Size()

	needed := make([]int, 0)
	buf := make([]byte, remote.ChunkSize)

	for _, remoteChunk := range remote.Chunks {
		if remoteChunk.Offset+remoteChunk.Size > existingSize {
			needed = append(needed, remoteChunk.Index)
			continue
		}

		// Read chunk from existing file
		n, err := f.ReadAt(buf[:remoteChunk.Size], remoteChunk.Offset)
		if err != nil && err != io.EOF {
			needed = append(needed, remoteChunk.Index)
			continue
		}

		localHash := sha256.Sum256(buf[:n])
		if hex.EncodeToString(localHash[:]) != remoteChunk.SHA256 {
			needed = append(needed, remoteChunk.Index)
		}
	}

	return needed, nil
}

// AssemblePatchedFile reconstructs targetPath using existingFilePath for matching chunks
// and incomingChunks for modified chunks.
func AssemblePatchedFile(targetPath string, existingFilePath string, manifest *FileChunkManifest, getChunk func(index int) ([]byte, error)) error {
	var existingFile *os.File
	var err error
	if existingFilePath != "" {
		existingFile, err = os.Open(existingFilePath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("open existing file: %w", err)
		}
		if existingFile != nil {
			defer existingFile.Close()
		}
	}

	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create target file: %w", err)
	}
	defer target.Close()

	fullHasher := sha256.New()
	existingBuf := make([]byte, manifest.ChunkSize)

	for _, chunk := range manifest.Chunks {
		data, err := getChunk(chunk.Index)
		if err != nil {
			// Chunk is not provided by delta stream, read from existing file
			if existingFile == nil {
				return fmt.Errorf("chunk %d missing and no existing file available: %w", chunk.Index, err)
			}
			n, readErr := existingFile.ReadAt(existingBuf[:chunk.Size], chunk.Offset)
			if readErr != nil && readErr != io.EOF {
				return fmt.Errorf("read chunk %d from existing file: %w", chunk.Index, readErr)
			}
			data = existingBuf[:n]
		}

		if int64(len(data)) != chunk.Size {
			return fmt.Errorf("chunk %d size mismatch: got %d, want %d", chunk.Index, len(data), chunk.Size)
		}

		fullHasher.Write(data)
		if _, writeErr := target.Write(data); writeErr != nil {
			return fmt.Errorf("write chunk %d: %w", chunk.Index, writeErr)
		}
	}

	if err := target.Sync(); err != nil {
		return fmt.Errorf("sync target file: %w", err)
	}

	computedHash := hex.EncodeToString(fullHasher.Sum(nil))
	if manifest.SHA256 != "" && computedHash != manifest.SHA256 {
		return fmt.Errorf("assembled file hash mismatch: got %s, want %s", computedHash, manifest.SHA256)
	}

	return nil
}
